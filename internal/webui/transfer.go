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
//     connection at the pace below, each chunk giving back its share of
//     answerBytes as the connection takes it, and a too-slow reader is cut
//     off. So the budget counts what the board itself still holds: the
//     operating system takes much of an answer into the connection's buffers
//     at once, and a reader that never reads would otherwise keep the whole of
//     it counted until it was cut off.
//
// A body is paced by two rules: no pause longer than transferStall, and after
// an allowance of transferAllowance, no slower than transferRate on average.
// That cuts off a sender that stops within ten seconds and one that trickles
// within about three minutes for the largest upload, while a phone on a
// 64 kbit/s link — 8 KiB/s — still uploads a 1 MiB attachment in under three
// minutes.
//
// An answer is paced by the average alone, so the whole of it is due within
// transferAllowance plus one second for every transferRate bytes of it: 138
// seconds for a 1 MiB attachment. It has no stall rule because a pause in
// writing is not a pause in reading. The board writes an answer into the
// connection's buffers far faster than a slow link drains them, and the
// operating system then holds the next write back for five or ten seconds at a
// time while the reader keeps taking bytes; a stall rule cut every 1 MiB
// download below 64 KiB a second short of its end. A reader that takes
// nothing at all is still cut off when the average runs out: ten seconds plus
// one for every 8 KiB the operating system took for it, and never later than
// the whole answer's deadline.
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
	// transferStall is the longest a body may pause.
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

// pace is one transfer's schedule: when it started, how much of it has
// moved, and whether a pause cuts it off.
type pace struct {
	limits *transferLimits
	start  time.Time
	done   int64
	stalls bool
}

// pace is a body's schedule, which a pause of transferStall cuts off.
func (limits *transferLimits) pace() *pace {
	return &pace{limits: limits, start: limits.now(), stalls: true}
}

// answerPace is an answer's schedule, which only the average cuts off.
func (limits *transferLimits) answerPace() *pace {
	return &pace{limits: limits, start: limits.now()}
}

// deadline is when the next count bytes must have moved: no later than the
// schedule allows for everything up to and including them, and for a body no
// later than a stall from now.
func (pace *pace) deadline(count int) time.Time {
	behind := time.Duration((pace.done + int64(count)) * int64(time.Second) / pace.limits.rate)
	scheduled := pace.start.Add(pace.limits.allowance + behind)
	if !pace.stalls {
		return scheduled
	}
	if stalled := pace.limits.now().Add(pace.limits.stall); stalled.Before(scheduled) {
		return stalled
	}
	return scheduled
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
			body:      heldBytes{size: limits.chunk, budget: &board.answers},
			refusable: safeMethod(request.Method),
		}
		defer answer.body.release()
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

// send writes a held answer at the answer pace, and cuts the connection off
// when the reader falls behind it.
func (board *transfers) send(writer http.ResponseWriter, control *http.ResponseController, answer *heldAnswer) {
	board.count(&board.delivering, 1)
	defer board.count(&board.delivering, -1)
	limits := &board.limits
	status, chunks := answer.status, answer.body.chunks
	if answer.refused {
		var refusal []byte
		status, refusal = busyAnswer(writer.Header(), busyAnswerMessage)
		chunks = [][]byte{refusal}
	} else {
		for key, values := range answer.header {
			writer.Header()[key] = values
		}
	}
	if status == 0 {
		status = http.StatusOK
	}
	length := 0
	for _, chunk := range chunks {
		length += len(chunk)
	}
	bodyAllowed := status >= http.StatusOK && status != http.StatusNoContent && status != http.StatusNotModified
	if bodyAllowed && writer.Header().Get("Content-Length") == "" {
		writer.Header().Set("Content-Length", strconv.Itoa(length))
	}
	schedule := limits.answerPace()
	_ = control.SetWriteDeadline(schedule.deadline(min(limits.chunk, length)))
	writer.WriteHeader(status)
	if bodyAllowed {
		for index, chunk := range chunks {
			_ = control.SetWriteDeadline(schedule.deadline(len(chunk)))
			if _, err := writer.Write(chunk); err != nil {
				panic(http.ErrAbortHandler)
			}
			// Pushed to the connection under this chunk's deadline rather than
			// left in the server's buffer for a write nothing bounds.
			if err := control.Flush(); err != nil {
				panic(http.ErrAbortHandler)
			}
			schedule.done += int64(len(chunk))
			if !answer.refused {
				// The connection has it now, so the board no longer does.
				answer.body.giveBack(index)
			}
		}
	}
	if err := control.Flush(); err != nil {
		panic(http.ErrAbortHandler)
	}
	// Whatever the server still writes for this answer is bounded like it.
	_ = control.SetWriteDeadline(schedule.deadline(0))
}

// heldBytes is a body or an answer held in memory, in chunks of one size. Every
// chunk but the first is counted against a budget before it is allocated, so
// what the budget counts is exactly what is held, and the first is what lets a
// small transfer through whatever the budget holds.
type heldBytes struct {
	size     int
	budget   *byteBudget
	chunks   [][]byte
	reserved int64
}

// space is where the next bytes go: the free end of the last chunk, or a new
// chunk. It is nil when a new chunk does not fit the budget, unless force says
// to count it anyway.
func (held *heldBytes) space(force bool) []byte {
	if count := len(held.chunks); count > 0 {
		if last := held.chunks[count-1]; len(last) < cap(last) {
			return last[len(last):cap(last)]
		}
		if !held.budget.take(int64(held.size)) {
			if !force {
				return nil
			}
			held.budget.force(int64(held.size))
		}
		held.reserved += int64(held.size)
	}
	chunk := make([]byte, 0, held.size)
	held.chunks = append(held.chunks, chunk)
	return chunk[:cap(chunk)]
}

// full says the chunks held so far have no room left, so the next byte needs a
// new one.
func (held *heldBytes) full() bool {
	count := len(held.chunks)
	return count > 0 && len(held.chunks[count-1]) == cap(held.chunks[count-1])
}

// grew records that count bytes were written into the last space handed out.
func (held *heldBytes) grew(count int) {
	last := len(held.chunks) - 1
	held.chunks[last] = held.chunks[last][:len(held.chunks[last])+count]
}

func (held *heldBytes) reader() io.Reader {
	readers := make([]io.Reader, len(held.chunks))
	for index, chunk := range held.chunks {
		readers[index] = bytes.NewReader(chunk)
	}
	return io.MultiReader(readers...)
}

// giveBack gives up chunk index once it is no longer needed, and the budget
// it was counted against with it.
func (held *heldBytes) giveBack(index int) {
	if held.chunks[index] == nil {
		return
	}
	held.chunks[index] = nil
	if index > 0 {
		held.budget.give(int64(held.size))
		held.reserved -= int64(held.size)
	}
}

func (held *heldBytes) release() {
	held.budget.give(held.reserved)
	held.reserved = 0
	held.chunks = nil
}

// heldAnswer is the answer a handler writes, held in memory and counted
// against the answer budget until it has been delivered.
type heldAnswer struct {
	header http.Header
	status int
	body   heldBytes
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
	written := len(content)
	for len(content) > 0 && !answer.refused {
		space := answer.body.space(!answer.refusable)
		if space == nil {
			// Thrown away for the busy refusal, which is small enough to be
			// let through without counting.
			answer.refused = true
			answer.body.release()
			break
		}
		copied := copy(space, content)
		answer.body.grew(copied)
		content = content[copied:]
	}
	return written, nil
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
		body := &heldBytes{size: board.limits.chunk, budget: &board.bodies}
		defer body.release()
		switch board.read(trade.control, request.Body, requestBodyLimit(request.URL.Path), body) {
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
		request.Body = io.NopCloser(body.reader())
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

// read reads at most limit+1 bytes of a body into held, at the pace.
//
// A body that does not fit in the budget is read on to its end and thrown
// away, so a client still sending is not cut off mid-request and can read the
// refusal; nothing read past the point it stopped fitting is held.
func (board *transfers) read(control *http.ResponseController, source io.Reader, limit int64, held *heldBytes) bodyOutcome {
	board.count(&board.receiving, 1)
	defer board.count(&board.receiving, -1)
	limits := &board.limits
	schedule := limits.pace()
	var discard []byte
	var probe [1]byte
	refused := false
	for schedule.done <= limit {
		if !refused && held.full() {
			// One byte decides whether the body goes on, so a new chunk is
			// counted only for a body that has more to hold, and a body that
			// ends exactly at a chunk's end is not refused for room it never
			// needed.
			_ = control.SetReadDeadline(schedule.deadline(1))
			count, err := source.Read(probe[:])
			if count > 0 {
				schedule.done += int64(count)
				if space := held.space(false); space == nil {
					refused = true
					held.release()
				} else {
					space[0] = probe[0]
					held.grew(1)
				}
			}
			if errors.Is(err, io.EOF) {
				if refused {
					return bodyRefused
				}
				return bodyComplete
			}
			if err != nil {
				held.release()
				return bodyStalled
			}
			continue
		}
		var space []byte
		if !refused {
			if space = held.space(false); space == nil {
				refused = true
				held.release()
			}
		}
		if refused {
			if discard == nil {
				discard = make([]byte, limits.chunk)
			}
			space = discard
		}
		want := int(min(int64(len(space)), limit+1-schedule.done))
		_ = control.SetReadDeadline(schedule.deadline(want))
		count, err := source.Read(space[:want])
		if !refused {
			held.grew(count)
		}
		schedule.done += int64(count)
		if errors.Is(err, io.EOF) {
			if refused {
				return bodyRefused
			}
			return bodyComplete
		}
		if err != nil {
			held.release()
			return bodyStalled
		}
	}
	if refused {
		return bodyRefusedOverLimit
	}
	return bodyOverLimit
}
