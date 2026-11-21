package webui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// How the board receives bodies and delivers answers around its work turns.
//
// None of these tests measures time. A client that stalls is a raw connection
// that simply stops; "by now" is always a wait for the board to report the
// state the test is about to rely on; and a connection the board must cut off
// is cut off by giving the board a clock that is an hour behind, so every
// deadline it sets on the connection has already passed. The schedule those
// deadlines follow is checked on its own with a clock the test holds.

// startBoardStack serves handler through the board's whole stack on a
// loopback port and stops it when the test ends.
func startBoardStack(t *testing.T, handler http.Handler, limits WorkLimits, bounds transferLimits) (*boardStack, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stack := newBoardStack(listener, handler, limits, bounds)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- stack.serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-served
	})
	return stack, listener.Addr().String()
}

// behindClock is a clock an hour behind, so every deadline the board computes
// from it is already past.
func behindClock() time.Time { return time.Now().Add(-time.Hour) }

// awaitTransfers waits until the board reports a state the test names.
func awaitTransfers(t *testing.T, board *transfers, describe string, ready func(transferState) bool) {
	t.Helper()
	// Hang guard, not a measurement.
	deadline := time.After(30 * time.Second)
	for {
		state := board.state()
		if ready(state) {
			return
		}
		select {
		case <-board.changed:
		case <-deadline:
			t.Fatalf("the board never reached %s; it is at %+v", describe, state)
		}
	}
}

func awaitConnections(t *testing.T, limit *connectionLimit, describe string, ready func(connectionState) bool) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		state := limit.state()
		if ready(state) {
			return
		}
		select {
		case <-limit.changed:
		case <-deadline:
			t.Fatalf("the connection limit never reached %s; it is at %+v", describe, state)
		}
	}
}

// rawRequest opens a connection and sends the start of a request on it: the
// head, and as much of the body as the test wants sent.
func rawRequest(t *testing.T, address, method, path string, length int, sent []byte) net.Conn {
	t.Helper()
	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	head := method + " " + path + " HTTP/1.1\r\nHost: " + address + "\r\n"
	if length >= 0 {
		head += "Content-Type: application/json\r\nContent-Length: " + strconv.Itoa(length) + "\r\n"
	}
	if _, err := connection.Write(append([]byte(head+"\r\n"), sent...)); err != nil {
		t.Fatal(err)
	}
	return connection
}

// transferRoutes answers the routes these tests use: answers of a size the
// path names, a write that reports how much body it read, and a write held
// until the test lets it go.
type transferRoutes struct {
	entered chan string
	release chan struct{}
}

func newTransferRoutes() *transferRoutes {
	return &transferRoutes{entered: make(chan string, 1024), release: make(chan struct{})}
}

func (routes *transferRoutes) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	routes.entered <- request.Method + " " + request.URL.Path
	switch {
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/answer/"):
		size, _ := strconv.Atoi(strings.TrimPrefix(request.URL.Path, "/answer/"))
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write(bytes.Repeat([]byte("a"), size))
	case request.Method == http.MethodGet && request.URL.Path == "/healthz":
		_, _ = io.WriteString(writer, "alive")
	case request.URL.Path == "/held":
		_, _ = io.Copy(io.Discard, request.Body)
		<-routes.release
		_, _ = io.WriteString(writer, "released")
	case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/write/"):
		read, _ := io.Copy(io.Discard, request.Body)
		size, _ := strconv.Atoi(strings.TrimPrefix(request.URL.Path, "/write/"))
		_, _ = fmt.Fprintf(writer, "read %d\n", read)
		_, _ = writer.Write(bytes.Repeat([]byte("w"), size))
	default:
		http.NotFound(writer, request)
	}
}

func get(t *testing.T, address, path string) (int, string) {
	t.Helper()
	response, err := http.Get("http://" + address + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read GET %s: %v", path, err)
	}
	return response.StatusCode, string(body)
}

func post(t *testing.T, client *http.Client, address, path string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://"+address+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return response
}

// requireBusyAnswer checks a refusal on the wire says what it is.
func requireBusyAnswer(t *testing.T, response *http.Response, says string) {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read refusal: %v", err)
	}
	if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Retry-After") != strconv.Itoa(workRetryAfterSeconds) {
		t.Fatalf("answer = %d with Retry-After %q, want %d with %d; body = %s",
			response.StatusCode, response.Header.Get("Retry-After"), http.StatusServiceUnavailable, workRetryAfterSeconds, body)
	}
	var document ErrorDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode refusal: %v; body = %s", err, body)
	}
	if !strings.HasPrefix(document.Error.Message, "The board is busy") || !strings.Contains(document.Error.Message, says) {
		t.Fatalf("refusal says %q, want the board's busy sentence about %s", document.Error.Message, says)
	}
}

func TestBoardGivesBackItsTurnBeforeTheReaderTakesTheAnswer(t *testing.T) {
	t.Parallel()
	// Production mutation: a turn held until the answer has been delivered is a
	// turn a reader that stops reading keeps for as long as it keeps the
	// connection open. With the board's sixteen, sixteen stalled attachment
	// downloads locked out every read and every write.
	bounds := defaultTransferLimits()
	const stuck = 16 << 20 // Far more than any loopback socket buffers.
	bounds.answerBytes = stuck + 1<<20
	routes := newTransferRoutes()
	stack, address := startBoardStack(t, routes, WorkLimits{MaxRequests: 1}, bounds)

	reader := rawRequest(t, address, http.MethodGet, "/answer/"+strconv.Itoa(stuck), -1, nil)
	awaitTransfers(t, stack.transfers, "the stalled answer being delivered", func(state transferState) bool {
		return state.Delivering == 1 && state.AnswerBytes > 0
	})
	if state := stack.work.state(); state.Busy != 0 {
		t.Fatalf("work while the only answer is being delivered = %+v, want no turn taken", state)
	}
	// The board's one turn is free for the next request, read or write.
	if status, body := get(t, address, "/answer/5"); status != http.StatusOK || body != "aaaaa" {
		t.Fatalf("a read behind a stalled reader = %d %q, want 200 \"aaaaa\"", status, body)
	}
	response := post(t, nil, address, "/write/0", []byte(`{}`))
	written, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(written) != "read 2\n" {
		t.Fatalf("a write behind a stalled reader = %d %q, want 200", response.StatusCode, written)
	}
	if state := stack.transfers.state(); state.Delivering != 1 {
		t.Fatalf("the stalled answer finished before the others were served (%+v), so this proves nothing", state)
	}
	reader.Close()
	awaitTransfers(t, stack.transfers, "the stalled answer given up", func(state transferState) bool {
		return state.Delivering == 0 && state.AnswerBytes == 0
	})
}

func TestBoardCutsOffAReaderThatFallsBehind(t *testing.T) {
	t.Parallel()
	// Production mutation: an answer written with no deadline waits on a
	// reader that never reads for as long as the connection stays open,
	// holding its bytes against the answer budget all that time.
	bounds := defaultTransferLimits()
	bounds.now = behindClock
	const size = 16 << 20
	bounds.answerBytes = size + 1<<20
	stack, address := startBoardStack(t, newTransferRoutes(), WorkLimits{MaxRequests: 1}, bounds)

	reader := rawRequest(t, address, http.MethodGet, "/answer/"+strconv.Itoa(size), -1, nil)
	received, err := io.Copy(io.Discard, reader)
	if received >= size {
		t.Fatalf("a reader the board was to cut off received the whole %d-byte answer", received)
	}
	if err != nil && !strings.Contains(err.Error(), "reset") {
		t.Fatalf("reading a cut-off answer: %v", err)
	}
	awaitTransfers(t, stack.transfers, "the cut-off answer given back", func(state transferState) bool {
		return state.Delivering == 0 && state.AnswerBytes == 0
	})
	awaitConnections(t, stack.listener, "the cut-off connection closed", func(state connectionState) bool {
		return state.Open == 0
	})
}

func TestBoardTakesNoTurnForABodyStillArriving(t *testing.T) {
	t.Parallel()
	// Production mutation: a turn taken before the body is read is a turn a
	// sender that stalls holds until it gives up. With the board's eight write
	// turns, eight stalled uploads blocked every write for half a minute.
	routes := newTransferRoutes()
	stack, address := startBoardStack(t, routes, WorkLimits{MaxRequests: 2}, defaultTransferLimits())

	sender := rawRequest(t, address, http.MethodPost, "/write/0", 100000, []byte(`{"title":"`))
	awaitTransfers(t, stack.transfers, "the stalled body being received", func(state transferState) bool {
		return state.Receiving == 1
	})
	if state := stack.work.state(); state.Busy != 0 || state.Waiting != 0 {
		t.Fatalf("work while a body is still arriving = %+v, want no turn taken or asked for", state)
	}
	// The board's one write turn is free for a write whose body has arrived.
	response := post(t, nil, address, "/write/0", []byte(`{"title":"honest"}`))
	written, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(written) != "read 18\n" {
		t.Fatalf("a write beside a stalled upload = %d %q, want 200 \"read 18\"", response.StatusCode, written)
	}
	if state := stack.transfers.state(); state.Receiving != 1 {
		t.Fatalf("the stalled body finished arriving (%+v), so this proves nothing", state)
	}
	sender.Close()
	awaitTransfers(t, stack.transfers, "the stalled body given up", func(state transferState) bool {
		return state.Receiving == 0 && state.BodyBytes == 0
	})
	close(routes.entered)
	for route := range routes.entered {
		if route != "POST /write/0" {
			t.Fatalf("the handler was entered for %s", route)
		}
	}
	if len(routes.entered) != 0 {
		t.Fatal("unreachable")
	}
}

func TestBoardCutsOffASenderThatFallsBehind(t *testing.T) {
	t.Parallel()
	// Production mutation: a body read with no deadline waits on a sender that
	// stopped for as long as the connection stays open.
	bounds := defaultTransferLimits()
	bounds.now = behindClock
	routes := newTransferRoutes()
	stack, address := startBoardStack(t, routes, WorkLimits{MaxRequests: 2}, bounds)

	sender := rawRequest(t, address, http.MethodPost, "/write/0", 100000, []byte(`{"title":"`))
	answer, err := io.ReadAll(sender)
	if len(answer) != 0 {
		t.Fatalf("a sender the board cut off was answered %q", answer)
	}
	if err != nil && !strings.Contains(err.Error(), "reset") {
		t.Fatalf("reading from a cut-off sender's connection: %v", err)
	}
	awaitConnections(t, stack.listener, "the cut-off connection closed", func(state connectionState) bool {
		return state.Open == 0
	})
	if state := stack.transfers.state(); state.Receiving != 0 || state.BodyBytes != 0 {
		t.Fatalf("transfers after the cut = %+v, want nothing held", state)
	}
	if len(routes.entered) != 0 {
		t.Fatalf("the handler was entered for a body that never arrived: %s", <-routes.entered)
	}
}

func TestPaceCutsOffAStallAndATrickleButNotAReaderKeepingUp(t *testing.T) {
	t.Parallel()
	// Production mutation: a deadline from the stall alone lets a client that
	// moves one byte every nine seconds hold its connection and its bytes for
	// days; one from the schedule alone lets a client that sent a megabyte
	// fast and then stopped hold them for two minutes.
	start := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	now := start
	limits := defaultTransferLimits()
	limits.now = func() time.Time { return now }
	schedule := limits.pace()
	at := func(seconds float64) time.Time { return start.Add(time.Duration(seconds * float64(time.Second))) }

	// Fresh: the stall bounds the first chunk.
	if got, want := schedule.deadline(transferChunk), at(10); !got.Equal(want) {
		t.Fatalf("first chunk deadline = %v, want %v", got, want)
	}
	// A megabyte sent in five seconds, then nothing: cut ten seconds later,
	// not when the schedule would have allowed.
	now, schedule.done = at(5), 1<<20
	if got, want := schedule.deadline(transferChunk), at(15); !got.Equal(want) {
		t.Fatalf("deadline after a fast megabyte = %v, want %v", got, want)
	}
	// A trickle: a hundred KiB in two minutes never pauses for ten seconds, but
	// it is far behind 8 KiB a second, so its deadline has already passed.
	now, schedule.done = at(120), 100<<10
	if got := schedule.deadline(transferChunk); !got.Before(now) {
		t.Fatalf("a trickle's deadline = %v, want it before now %v", got, now)
	}
	// A reader keeping 8 KiB a second is never behind, and a whole megabyte at
	// that pace has until 138 seconds.
	for second := 0; second < 128; second++ {
		now, schedule.done = at(float64(second)), int64(second)*transferRate
		if got := schedule.deadline(transferChunk); !got.After(now) {
			t.Fatalf("at %ds a reader keeping pace has deadline %v, want it after now", second, got)
		}
	}
	now, schedule.done = start, 0
	if got, want := schedule.deadline(1<<20), at(138); got.After(want) || !got.Equal(at(10)) {
		t.Fatalf("a megabyte's whole deadline = %v, want the stall %v first", got, want)
	}
	now = at(130)
	if got, want := schedule.deadline(1<<20), at(138); !got.Equal(want) {
		t.Fatalf("a megabyte at 8 KiB/s must be done by %v, got %v", want, got)
	}
}

func TestBoardRefusesABodyItHasNoRoomForAndLetsTheSenderReadWhy(t *testing.T) {
	t.Parallel()
	// Production mutation: bodies read with no budget are a memory bound of
	// the connection cap times the largest upload; a refusal sent before the
	// body is read reaches a client still sending as a broken pipe.
	bounds := defaultTransferLimits()
	bounds.bodyBytes = 2 * transferChunk
	routes := newTransferRoutes()
	stack, address := startBoardStack(t, routes, WorkLimits{MaxRequests: 4}, bounds)

	// Three chunks of a longer body: the first is let through, the other two
	// fill the budget.
	holder := rawRequest(t, address, http.MethodPost, "/write/0", 8*transferChunk, bytes.Repeat([]byte(" "), 3*transferChunk))
	awaitTransfers(t, stack.transfers, "the budget full", func(state transferState) bool {
		return state.BodyBytes == 2*transferChunk
	})

	// A body larger than a chunk does not fit. It is read to its end and
	// thrown away, and its sender reads the refusal on a connection that is
	// still good for another request.
	refused := rawRequest(t, address, http.MethodPost, "/write/0", 3*transferChunk, bytes.Repeat([]byte(" "), 3*transferChunk))
	answers := bufio.NewReader(refused)
	response, err := http.ReadResponse(answers, nil)
	if err != nil {
		t.Fatalf("read the refusal: %v", err)
	}
	requireBusyAnswer(t, response, "request bodies")
	if _, err := refused.Write([]byte("GET /healthz HTTP/1.1\r\nHost: " + address + "\r\n\r\n")); err != nil {
		t.Fatalf("the refused sender's connection is closed: %v", err)
	}
	again, err := http.ReadResponse(answers, nil)
	if err != nil || again.StatusCode != http.StatusOK {
		t.Fatalf("the next request on the refused sender's connection = %v, %v; want 200", again, err)
	}
	again.Body.Close()

	// A body of one chunk is let through whatever the budget holds, which is
	// what keeps a create working through a flood of large uploads.
	small := post(t, nil, address, "/write/0", bytes.Repeat([]byte(" "), transferChunk))
	written, _ := io.ReadAll(small.Body)
	small.Body.Close()
	if small.StatusCode != http.StatusOK {
		t.Fatalf("a one-chunk body beside a full budget = %d %q, want 200", small.StatusCode, written)
	}
	if state := stack.transfers.state(); state.BodyBytes != 2*transferChunk {
		t.Fatalf("budget after the refusal = %+v, want the holder's %d bytes alone", state, 2*transferChunk)
	}
	holder.Close()
	awaitTransfers(t, stack.transfers, "the budget given back", func(state transferState) bool {
		return state.Receiving == 0 && state.BodyBytes == 0
	})
}

func TestBoardReadsARefusedUploadToItsEndSoTheSenderCanReadWhy(t *testing.T) {
	t.Parallel()
	// Production mutation: a work turn refused before the body is read leaves
	// an upload's bytes unread. The server then closes the connection under a
	// client still sending, which a client other than a browser reports as a
	// broken pipe rather than the refusal — 48 of 150 in the review's flood.
	routes := newTransferRoutes()
	stack, address := startBoardStack(t, routes, WorkLimits{MaxRequests: 1}, defaultTransferLimits())
	timers := installManualTimers(stack.work)
	held := make(chan *http.Response, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodPost, "http://"+address+"/held", strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			response.Body.Close()
		}
		held <- response
	}()
	<-routes.entered
	for index := 0; index < workQueuePerSlot; index++ {
		go func() {
			request, _ := http.NewRequest(http.MethodPost, "http://"+address+"/held", strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/json")
			if response, err := http.DefaultClient.Do(request); err == nil {
				response.Body.Close()
			}
		}()
	}
	awaitWorkState(t, stack.work, "every turn and place in line taken", func(state workState) bool {
		return state.Busy == 1 && state.Waiting == workQueuePerSlot
	})

	upload := bytes.Repeat([]byte("A"), MaxAttachmentUploadBodyBytes-1024)
	connection := rawRequest(t, address, http.MethodPost, "/api/tasks/WB-1/attachments", len(upload), nil)
	sent := make(chan error, 1)
	go func() {
		_, err := connection.Write(upload)
		sent <- err
	}()
	answers := bufio.NewReader(connection)
	response, err := http.ReadResponse(answers, nil)
	if err != nil {
		t.Fatalf("read the refusal of a large upload: %v", err)
	}
	requireBusyAnswer(t, response, "requests it takes at once")
	if err := <-sent; err != nil {
		t.Fatalf("sending the refused upload: %v", err)
	}
	if _, err := connection.Write([]byte("GET /healthz HTTP/1.1\r\nHost: " + address + "\r\n\r\n")); err != nil {
		t.Fatalf("the refused upload's connection is closed: %v", err)
	}
	again, err := http.ReadResponse(answers, nil)
	if err != nil || again.StatusCode != http.StatusOK {
		t.Fatalf("the next request after a refused upload = %v, %v; want 200", again, err)
	}
	again.Body.Close()
	close(routes.release)
	timers.fireAll()
	<-held
}

func TestBoardRefusesAReadItHasNoRoomToHoldButNeverAWritesAnswer(t *testing.T) {
	t.Parallel()
	// Production mutation: answers held with no budget are a memory bound of
	// the connection cap times the largest answer, which for a board's task
	// list has no ceiling at all.
	bounds := defaultTransferLimits()
	const stuck = 16 << 20
	bounds.answerBytes = stuck
	stack, address := startBoardStack(t, newTransferRoutes(), WorkLimits{MaxRequests: 4}, bounds)

	// An answer the reader never takes, filling the budget.
	reader := rawRequest(t, address, http.MethodGet, "/answer/"+strconv.Itoa(stuck+transferChunk), -1, nil)
	awaitTransfers(t, stack.transfers, "the budget full", func(state transferState) bool {
		return state.Delivering == 1 && state.AnswerBytes == stuck
	})

	response, err := http.Get("http://" + address + "/answer/" + strconv.Itoa(3*transferChunk))
	if err != nil {
		t.Fatal(err)
	}
	requireBusyAnswer(t, response, "answers")
	if status, body := get(t, address, "/answer/"+strconv.Itoa(transferChunk)); status != http.StatusOK || len(body) != transferChunk {
		t.Fatalf("a one-chunk answer beside a full budget = %d with %d bytes, want 200 with %d", status, len(body), transferChunk)
	}
	write := post(t, nil, address, "/write/"+strconv.Itoa(3*transferChunk), []byte(`{}`))
	written, _ := io.ReadAll(write.Body)
	write.Body.Close()
	if write.StatusCode != http.StatusOK || len(written) != len("read 2\n")+3*transferChunk {
		t.Fatalf("a write's answer beside a full budget = %d with %d bytes, want it whole", write.StatusCode, len(written))
	}
	if state := stack.transfers.state(); state.Delivering != 1 || state.AnswerBytes != stuck {
		t.Fatalf("transfers after the write = %+v, want the stalled answer alone", state)
	}
	reader.Close()
	awaitTransfers(t, stack.transfers, "the budget given back", func(state transferState) bool {
		return state.Delivering == 0 && state.AnswerBytes == 0
	})
}

func TestBoardClosesIdleConnectionsToMakeRoomAndOtherwiseWaits(t *testing.T) {
	t.Parallel()
	// Production mutation: a listener with no cap accepts every connection
	// anyone opens, each with a goroutine of its own.
	bounds := defaultTransferLimits()
	bounds.maxConnections = 2
	stack, address := startBoardStack(t, newTransferRoutes(), WorkLimits{MaxRequests: 4}, bounds)

	// Two connections that have each had their answer and wait idle for the
	// next request.
	var idle []net.Conn
	for index := 0; index < 2; index++ {
		connection := rawRequest(t, address, http.MethodGet, "/healthz", -1, nil)
		if response, err := http.ReadResponse(bufio.NewReader(connection), nil); err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("answer %d = %v, %v", index, response, err)
		}
		idle = append(idle, connection)
	}
	awaitConnections(t, stack.listener, "two idle connections", func(state connectionState) bool {
		return state.Open == 2 && state.Idle == 2
	})
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	response, err := client.Get("http://" + address + "/healthz")
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("a request with every place held by an idle connection = %v, %v; want 200", response, err)
	}
	response.Body.Close()
	for _, connection := range idle {
		if _, err := connection.Read(make([]byte, 1)); err == nil {
			t.Fatal("an idle connection was left open while a new one waited")
		}
	}

	// Two connections that have sent nothing yet are not idle, so a third
	// waits for one of them to close.
	var fresh []net.Conn
	for index := 0; index < 2; index++ {
		connection, err := net.Dial("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		fresh = append(fresh, connection)
	}
	awaitConnections(t, stack.listener, "two fresh connections", func(state connectionState) bool {
		return state.Open == 2
	})
	answered := make(chan error, 1)
	go func() {
		response, err := client.Get("http://" + address + "/healthz")
		if err == nil {
			response.Body.Close()
		}
		answered <- err
	}()
	awaitConnections(t, stack.listener, "a connection waiting for a place", func(state connectionState) bool {
		return state.Waiting
	})
	select {
	case err := <-answered:
		t.Fatalf("a request was answered past the cap: %v", err)
	default:
	}
	fresh[0].Close()
	if err := <-answered; err != nil {
		t.Fatalf("the waiting request once a place freed: %v", err)
	}
}

// Not parallel: it counts the process's goroutines, which other tests would
// move while it reads them.
func TestBoardGoroutinesStayWithinTheConnectionCap(t *testing.T) {
	// Production mutation: without a cap on connections, every stalled upload
	// anyone opens costs the board a goroutine of its own.
	bounds := defaultTransferLimits()
	bounds.maxConnections = 8
	stack, address := startBoardStack(t, newTransferRoutes(), WorkLimits{MaxRequests: 4}, bounds)
	if status, _ := get(t, address, "/healthz"); status != http.StatusOK {
		t.Fatal("the board did not answer")
	}
	http.DefaultClient.CloseIdleConnections()
	awaitConnections(t, stack.listener, "no connection open", func(state connectionState) bool { return state.Open == 0 })
	before := runtime.NumGoroutine()

	const stalled = 40
	var wait sync.WaitGroup
	for index := 0; index < stalled; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			connection, err := net.Dial("tcp", address)
			if err != nil {
				return
			}
			t.Cleanup(func() { connection.Close() })
			_, _ = connection.Write([]byte("POST /write/0 HTTP/1.1\r\nHost: " + address +
				"\r\nContent-Type: application/json\r\nContent-Length: 100000\r\n\r\n{"))
		}()
	}
	wait.Wait()
	awaitConnections(t, stack.listener, "every place taken and one waiting", func(state connectionState) bool {
		return state.Open == bounds.maxConnections && state.Waiting
	})
	awaitTransfers(t, stack.transfers, "every accepted upload stalled", func(state transferState) bool {
		return state.Receiving == bounds.maxConnections
	})
	// One goroutine for each connection the board accepted; none for the ones
	// it has not.
	if grown := runtime.NumGoroutine() - before; grown > bounds.maxConnections+2 {
		t.Fatalf("%d stalled uploads grew the board by %d goroutines, want at most %d", stalled, grown, bounds.maxConnections+2)
	}
}

func TestBoardHoldsWaitingUploadsWithinTheBodyBudget(t *testing.T) {
	t.Parallel()
	// Production mutation: bodies read before a turn with no budget hold every
	// waiting upload in memory — the review measured 150 simultaneous 1 MiB
	// uploads taking a board from 56 MB to 608 MB — so the cap would bound
	// processes but not memory.
	const size = 8 * transferChunk
	const counted = size - transferChunk // What one upload holds against the budget.
	bounds := defaultTransferLimits()
	bounds.bodyBytes = 3 * counted
	routes := newTransferRoutes()
	stack, address := startBoardStack(t, routes, WorkLimits{MaxRequests: 1}, bounds)
	timers := installManualTimers(stack.work)
	upload := func() <-chan *http.Response {
		answered := make(chan *http.Response, 1)
		go func() {
			request, _ := http.NewRequest(http.MethodPost, "http://"+address+"/held", bytes.NewReader(bytes.Repeat([]byte(" "), size)))
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				answered <- nil
				return
			}
			answered <- response
		}()
		return answered
	}

	// One upload at work, holding its body.
	working := upload()
	<-routes.entered
	// Five more: two fit beside it and wait for the turn, three do not.
	answers := make(chan *http.Response, 5)
	for index := 0; index < 5; index++ {
		answered := upload()
		go func() { answers <- <-answered }()
	}
	awaitWorkState(t, stack.work, "two uploads waiting", func(state workState) bool {
		return state.Busy == 1 && state.Waiting == 2
	})
	for refused := 0; refused < 3; refused++ {
		select {
		case response := <-answers:
			if response == nil {
				t.Fatal("an upload failed rather than being refused")
			}
			requireBusyAnswer(t, response, "request bodies")
		case <-time.After(30 * time.Second):
			t.Fatalf("only %d uploads were refused, want the 3 that did not fit", refused)
		}
	}
	if held := stack.transfers.state().BodyBytes; held != 3*counted {
		t.Fatalf("bodies held = %d bytes, want the budget's %d", held, 3*counted)
	}
	close(routes.release)
	timers.fireAll()
	if response := <-working; response == nil || response.StatusCode != http.StatusOK {
		t.Fatalf("the working upload = %v", response)
	}
	awaitTransfers(t, stack.transfers, "every body given back", func(state transferState) bool {
		return state.BodyBytes == 0
	})
}
