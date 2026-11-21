package webui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// How the board receives requests and delivers answers.
//
// A work turn (see work_limit.go) covers the board's own work: the Git
// processes it starts and the memory that work needs. It must never cover the
// time a client takes to send a request or to take its answer, because that
// time is the client's to spend: one that stops reading an attachment, or
// sends its headers and then stops, would otherwise hold a turn for as long as
// it kept the connection open, and sixteen such connections would lock the
// board for everyone.
//
// So every request passes through three stages, and only the middle one holds
// a turn:
//
//  1. Receiving. A request's body is read whole into memory before it asks for
//     a turn, at the pace below, and counted against bodyBytes while it is
//     held. A stalled or too-slow sender is cut off; one that arrives while
//     bodyBytes is spent is read to its end and discarded, then answered 503
//     with Retry-After, so a client still sending can read the answer.
//  2. Working. The handler runs with the body already in memory and writes its
//     answer into memory, counted against answerBytes. A read whose answer
//     does not fit is answered 503 with Retry-After instead; a write's answer
//     always fits, because the change it reports has been made.
//  3. Delivering. The turn has been given back. The answer is written to the
//     connection at the pace below, and a stalled or too-slow reader is cut
//     off.
//
// The pace is the same in both directions: no pause longer than
// transferStall, and after an allowance of transferAllowance, no slower than
// transferRate on average. That cuts off a connection that stops within ten
// seconds and one that trickles within about two minutes for a megabyte,
// while a phone on a 64 kbit/s link — 8 KiB/s — still downloads a 1 MiB
// attachment in about 138 seconds and uploads one in under three minutes.
//
// The memory that receiving and delivering hold is bounded twice: by the two
// byte budgets, and by the connection cap, because every body and answer up
// to one transferChunk is let through without counting (so a flood of large
// transfers never stops the small ones, like a poll's 304 or a create) and
// there is at most one of each per connection. Every connection costs one
// goroutine, two while a handler runs after its body has been read, and the
// cap bounds those too. See docs/reference.md for the numbers.
const (
	// defaultMaxConnections is how many connections the board keeps open at
	// once. Past it, the board closes connections that are idle between
	// requests to make room, and otherwise stops accepting until one closes;
	// a connection that arrives meanwhile waits in the operating system's
	// listen queue. A browser keeps at most six open to one board, and a
	// proxy a pool of its own, so ordinary use never comes near it.
	defaultMaxConnections = 512
	// defaultBodyBytes is how many bytes of request bodies the board holds at
	// once: about twenty-three of the largest uploads it accepts.
	defaultBodyBytes = 32 << 20
	// defaultAnswerBytes is how many bytes of answers the board holds at once
	// while it delivers them: about thirty-two 1 MiB attachments.
	defaultAnswerBytes = 32 << 20
	// transferChunk is the unit a transfer is paced in, and how much of each
	// body and each answer is let through without counting against a budget.
	transferChunk = 8 << 10
	// transferStall is the longest a transfer may pause.
	transferStall = 10 * time.Second
	// transferAllowance is how far a transfer may fall behind transferRate
	// before it is cut off, which is what gives a connection that starts slowly
	// time to get going.
	transferAllowance = 10 * time.Second
	// transferRate is the slowest average pace a transfer may keep, in bytes a
	// second.
	transferRate = 8 << 10
)

// transferLimits are the bounds above, held in one place so tests can make
// them small or give them a clock of their own.
type transferLimits struct {
	maxConnections int
	bodyBytes      int64
	answerBytes    int64
	chunk          int
	stall          time.Duration
	allowance      time.Duration
	rate           int64
	// now is the clock every deadline is computed from. The deadlines
	// themselves are the connection's, so a test that wants one to have
	// passed gives the board a clock that is behind.
	now func() time.Time
}

func defaultTransferLimits() transferLimits {
	return transferLimits{
		maxConnections: defaultMaxConnections,
		bodyBytes:      defaultBodyBytes,
		answerBytes:    defaultAnswerBytes,
		chunk:          transferChunk,
		stall:          transferStall,
		allowance:      transferAllowance,
		rate:           transferRate,
		now:            time.Now,
	}
}

// pace is one transfer's schedule: when it started and how much of it has
// moved.
type pace struct {
	limits *transferLimits
	start  time.Time
	done   int64
}

func (limits *transferLimits) pace() *pace {
	return &pace{limits: limits, start: limits.now()}
}

// deadline is when the next count bytes must have moved: no later than a
// stall from now, and no later than the schedule allows for everything up to
// and including them.
func (pace *pace) deadline(count int) time.Time {
	stalled := pace.limits.now().Add(pace.limits.stall)
	behind := time.Duration((pace.done + int64(count)) * int64(time.Second) / pace.limits.rate)
	scheduled := pace.start.Add(pace.limits.allowance + behind)
	if scheduled.Before(stalled) {
		return scheduled
	}
	return stalled
}

// byteBudget counts bytes held against a limit.
type byteBudget struct {
	mutex sync.Mutex
	limit int64
	held  int64
	// changed is told whenever held moves, so a test can wait for a state.
	changed func()
}

func (budget *byteBudget) moved() {
	if budget.changed != nil {
		budget.changed()
	}
}

// take holds count more bytes if they fit.
func (budget *byteBudget) take(count int64) bool {
	budget.mutex.Lock()
	if budget.held+count > budget.limit {
		budget.mutex.Unlock()
		return false
	}
	budget.held += count
	budget.mutex.Unlock()
	budget.moved()
	return true
}

// force holds count more bytes whether or not they fit, for an answer that
// must be delivered.
func (budget *byteBudget) force(count int64) {
	budget.mutex.Lock()
	budget.held += count
	budget.mutex.Unlock()
	budget.moved()
}

func (budget *byteBudget) give(count int64) {
	if count == 0 {
		return
	}
	budget.mutex.Lock()
	budget.held -= count
	budget.mutex.Unlock()
	budget.moved()
}

func (budget *byteBudget) current() int64 {
	budget.mutex.Lock()
	defer budget.mutex.Unlock()
	return budget.held
}

// transfers receives the board's request bodies and delivers its answers
// within the bounds above.
type transfers struct {
	limits  transferLimits
	bodies  byteBudget
	answers byteBudget

	mutex      sync.Mutex
	receiving  int
	delivering int
	// changed is signalled, without blocking, whenever the counts move, so a
	// test can wait for a state rather than for a while.
	changed chan struct{}
}

// transferState is the counts at one moment, for tests.
type transferState struct {
	Receiving, Delivering  int
	BodyBytes, AnswerBytes int64
}

func newTransfers(limits transferLimits) *transfers {
	board := &transfers{limits: limits, changed: make(chan struct{}, 1)}
	board.bodies = byteBudget{limit: limits.bodyBytes, changed: board.signal}
	board.answers = byteBudget{limit: limits.answerBytes, changed: board.signal}
	return board
}

func (board *transfers) signal() {
	select {
	case board.changed <- struct{}{}:
	default:
	}
}

func (board *transfers) count(counter *int, delta int) {
	board.mutex.Lock()
	*counter += delta
	board.mutex.Unlock()
	board.signal()
}

func (board *transfers) state() transferState {
	board.mutex.Lock()
	defer board.mutex.Unlock()
	return transferState{
		Receiving:   board.receiving,
		Delivering:  board.delivering,
		BodyBytes:   board.bodies.current(),
		AnswerBytes: board.answers.current(),
	}
}

// exchange is one request's link between the stages: the connection's
// deadlines, which only the outermost stage can reach, and whether the body
// was read to its end.
type exchange struct {
	control  *http.ResponseController
	bodyRead bool
}

type exchangeKey struct{}

// hasBody reports whether a request carries a body to read. The server hands a
// request with neither a length nor chunked framing http.NoBody.
func hasBody(request *http.Request) bool {
	return request.Body != nil && request.Body != http.NoBody && request.ContentLength != 0
}

// safeMethod reports whether a request changes nothing, so an answer to it may
// be thrown away and the request asked again.
func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// deliver is the outermost stage: it holds the answer the stages inside it
// write, and writes it to the connection once they have returned and the work
// turn is given back.
func (board *transfers) deliver(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		control := http.NewResponseController(writer)
		limits := &board.limits
		// The one thing the server may write before the answer is ready is a
		// 100 Continue, sent when the body is first read. It is bounded too.
		_ = control.SetWriteDeadline(limits.now().Add(limits.stall))
		trade := &exchange{control: control}
		answer := &heldAnswer{
			header:    make(http.Header),
			budget:    &board.answers,
			free:      int64(limits.chunk),
			refusable: safeMethod(request.Method),
		}
		defer answer.release()
		inner.ServeHTTP(answer, request.WithContext(context.WithValue(request.Context(), exchangeKey{}, trade)))
		if hasBody(request) && !trade.bodyRead {
			// The server reads and discards what is left of a body nothing read
			// before it reuses the connection. That read is a client's to pace
			// too, and the server sets no deadline of its own.
			_ = control.SetReadDeadline(limits.now().Add(limits.stall))
		}
		board.send(writer, control, answer)
	})
}

// send writes a held answer at the pace, and cuts the connection off when the
// reader falls behind it.
func (board *transfers) send(writer http.ResponseWriter, control *http.ResponseController, answer *heldAnswer) {
	board.count(&board.delivering, 1)
	defer board.count(&board.delivering, -1)
	limits := &board.limits
	status, body := answer.status, answer.body
	if answer.refused {
		status, body = busyAnswer(writer.Header(), busyAnswerMessage)
	} else {
		for key, values := range answer.header {
			writer.Header()[key] = values
		}
	}
	if status == 0 {
		status = http.StatusOK
	}
	bodyAllowed := status >= http.StatusOK && status != http.StatusNoContent && status != http.StatusNotModified
	if bodyAllowed && writer.Header().Get("Content-Length") == "" {
		writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	schedule := limits.pace()
	_ = control.SetWriteDeadline(schedule.deadline(min(limits.chunk, len(body))))
	writer.WriteHeader(status)
	if bodyAllowed {
		for offset := 0; offset < len(body); offset += limits.chunk {
			end := min(offset+limits.chunk, len(body))
			_ = control.SetWriteDeadline(schedule.deadline(end - offset))
			if _, err := writer.Write(body[offset:end]); err != nil {
				panic(http.ErrAbortHandler)
			}
			// Pushed to the connection under this chunk's deadline rather than
			// left in the server's buffer for a write nothing bounds.
			if err := control.Flush(); err != nil {
				panic(http.ErrAbortHandler)
			}
			schedule.done = int64(end)
		}
	}
	if err := control.Flush(); err != nil {
		panic(http.ErrAbortHandler)
	}
	// Whatever the server still writes for this answer is bounded like it.
	_ = control.SetWriteDeadline(limits.now().Add(limits.stall))
}

// heldAnswer is the answer a handler writes, held in memory and counted
// against the answer budget until it has been delivered.
type heldAnswer struct {
	header   http.Header
	status   int
	body     []byte
	reserved int64
	budget   *byteBudget
	// free is how many bytes are held without counting against the budget.
	free int64
	// refusable says the answer may be thrown away when it does not fit,
	// which is true of a read: the client can ask again, and nothing was
	// changed. A write's answer is never thrown away.
	refusable bool
	refused   bool
}

func (answer *heldAnswer) Header() http.Header { return answer.header }

func (answer *heldAnswer) WriteHeader(status int) {
	if answer.status == 0 {
		answer.status = status
	}
}

func (answer *heldAnswer) Write(content []byte) (int, error) {
	if answer.status == 0 {
		answer.WriteHeader(http.StatusOK)
	}
	if answer.refused {
		return len(content), nil
	}
	if need := int64(len(answer.body)+len(content)) - answer.free - answer.reserved; need > 0 {
		switch {
		case answer.budget.take(need):
			answer.reserved += need
		case answer.refusable:
			answer.refuse()
			return len(content), nil
		default:
			answer.budget.force(need)
			answer.reserved += need
		}
	}
	answer.body = append(answer.body, content...)
	return len(content), nil
}

// refuse throws the answer away for the busy refusal, which is small enough
// to be let through without counting.
func (answer *heldAnswer) refuse() {
	answer.refused = true
	answer.body = nil
	answer.release()
}

func (answer *heldAnswer) release() {
	answer.budget.give(answer.reserved)
	answer.reserved = 0
}

// receive is the first stage: it reads a request's body whole, at the pace,
// before the request asks for a work turn.
func (board *transfers) receive(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		trade, _ := request.Context().Value(exchangeKey{}).(*exchange)
		if trade == nil || !hasBody(request) {
			inner.ServeHTTP(writer, request)
			return
		}
		limit := requestBodyLimit(request.URL.Path)
		body, reserved, outcome := board.read(trade.control, request.Body, limit)
		defer board.bodies.give(reserved)
		switch outcome {
		case bodyStalled:
			// The client stopped sending, or sent too slowly, or went away. There
			// is nobody reading an answer, so the connection is closed.
			panic(http.ErrAbortHandler)
		case bodyRefused:
			writeBusy(writer, busyBodyMessage)
			return
		case bodyRefusedOverLimit:
			writer.Header().Set("Connection", "close")
			writeBusy(writer, busyBodyMessage)
			return
		case bodyOverLimit:
			// The rest is unread, so the connection cannot carry another
			// request. The handler answers the oversized body as it always has.
			writer.Header().Set("Connection", "close")
		case bodyComplete:
			trade.bodyRead = true
			// The server watches a connection whose body has been read for the
			// client going away. That watch is not a transfer and must not time
			// out: an expired one cancels the request it belongs to.
			_ = trade.control.SetReadDeadline(time.Time{})
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		inner.ServeHTTP(writer, request)
	})
}

type bodyOutcome int

const (
	bodyComplete bodyOutcome = iota
	bodyOverLimit
	bodyRefused
	bodyRefusedOverLimit
	bodyStalled
)

// read reads at most limit+1 bytes of a body at the pace. It hands back the
// body and how many of its bytes it holds against the budget, which the
// caller gives back once the request is done.
//
// A body that does not fit in the budget is read on to its end and thrown
// away, so a client still sending is not cut off mid-request and can read the
// refusal; what is read past the point it stopped fitting is never held.
func (board *transfers) read(control *http.ResponseController, source io.Reader, limit int64) ([]byte, int64, bodyOutcome) {
	board.count(&board.receiving, 1)
	defer board.count(&board.receiving, -1)
	limits := &board.limits
	schedule := limits.pace()
	chunk := make([]byte, limits.chunk)
	var body []byte
	var reserved int64
	refused := false
	for schedule.done <= limit {
		want := int(min(int64(limits.chunk), limit+1-schedule.done))
		_ = control.SetReadDeadline(schedule.deadline(want))
		count, err := source.Read(chunk[:want])
		if count > 0 && !refused {
			if need := int64(len(body)+count) - int64(limits.chunk) - reserved; need > 0 {
				if board.bodies.take(need) {
					reserved += need
				} else {
					refused = true
					board.bodies.give(reserved)
					reserved = 0
					body = nil
				}
			}
			if !refused {
				body = append(body, chunk[:count]...)
			}
		}
		schedule.done += int64(count)
		if errors.Is(err, io.EOF) {
			if refused {
				return nil, 0, bodyRefused
			}
			return body, reserved, bodyComplete
		}
		if err != nil {
			board.bodies.give(reserved)
			return nil, 0, bodyStalled
		}
	}
	if refused {
		return nil, 0, bodyRefusedOverLimit
	}
	return body, reserved, bodyOverLimit
}
