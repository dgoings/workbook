package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// The board's cap on how much work it does at once.
//
// A work turn covers the board's own work on a request and nothing else: the
// request's body is already in memory when it asks for one (transfer.go reads
// it first, at a pace a stalled sender cannot hold up), and its answer is
// delivered after the turn is given back. So a client that sends or reads
// slowly costs a connection and the bytes it is moving, both bounded in
// transfer.go and connections.go, and never a turn.
//
// Every Git process a request starts is started inside its turn, so the turns
// bound those processes to a small multiple of the cap. The one Git work that
// is not a request's is the board's own synchronization loop, which runs one
// synchronization at a time on its interval whatever the requests do, so it
// adds at most one synchronization's processes to what the cap allows.
//
// A board on one person's machine never reaches the cap. It exists for a board
// several people share, where one person's flood of creates or uploads would
// otherwise slow everybody's refresh to a crawl and could run the server out
// of processes or memory. Fairness between those people is not this cap's job:
// it bounds the server, and the proxy in front of a shared board is where each
// person's own share is set.
const (
	// DefaultMaxRequests is how many requests the board works on at once
	// unless told otherwise. A person's own board, polling once a second and
	// writing when they act, has one or two requests open at a time, and a page
	// load with images in its descriptions a handful more, so ordinary use
	// never waits. The measurements behind the number are in docs/reference.md.
	DefaultMaxRequests = 16

	// workQueuePerSlot is how many requests may wait for a turn for every one
	// the board can work on. A waiting request costs a goroutine, its
	// connection and its body, which the body budget bounds in total, but that
	// is still not free, so the line is
	// bounded by length as well as by time: past it a request is refused at
	// once rather than added to a queue nothing would ever drain in time.
	workQueuePerSlot = 4

	// workWait is the longest a request waits for its turn before it is
	// refused. Long enough to absorb a burst — a write on a shared board takes
	// tens of milliseconds, or a second or two when it publishes to a slow
	// origin — and short enough that a refused person hears so while they are
	// still looking at the change they made.
	workWait = 5 * time.Second

	// workRetryAfterSeconds is what a refusal tells the client to wait before
	// asking again. The board's own page waits at least this long and longer
	// still if it is refused again.
	workRetryAfterSeconds = 2
)

// WorkLimits says how much work the board does at once.
type WorkLimits struct {
	// MaxRequests is how many requests the board works on at once. Writes may
	// hold at most half of them, rounded down but at least one, so reads — the
	// refresh every open page depends on most of all — always have room while
	// writes are saturated. Below one it means DefaultMaxRequests.
	MaxRequests int
}

// DefaultWorkLimits is the cap Serve applies.
func DefaultWorkLimits() WorkLimits {
	return WorkLimits{MaxRequests: DefaultMaxRequests}
}

// workLimiter admits requests to the board's handler, at most `slots` at once
// and at most `writeSlots` of them writes, and makes the rest wait their turn
// in arrival order for up to `wait`.
//
// The order is first come, first served, with one exception: a read may pass a
// write that is waiting only because the writes' share is full, which is what
// keeps the reads' share free for reads.
type workLimiter struct {
	slots      int
	writeSlots int
	queueLimit int
	wait       time.Duration
	// timer starts the wait, returning the channel that says it is over and a
	// function that stops it. Tests replace it with one they fire themselves,
	// so no test of a refusal depends on how fast the machine is.
	timer func(time.Duration) (<-chan time.Time, func())

	mutex      sync.Mutex
	busy       int
	busyWrites int
	waiting    []*workWaiter
	// changed is signalled, without blocking, whenever the counts above move,
	// so a test can wait for a state rather than for a while.
	changed chan struct{}
}

type workWaiter struct {
	write    bool
	admitted chan struct{}
	// granted is set under the limiter's mutex at the moment a slot is handed
	// over, so a wait that ends in the same instant can tell whether it holds
	// one.
	granted bool
}

// workState is the limiter's counts at one moment, for tests.
type workState struct {
	Busy, BusyWrites, Waiting int
}

func newWorkLimiter(limits WorkLimits) *workLimiter {
	slots := limits.MaxRequests
	if slots < 1 {
		slots = DefaultMaxRequests
	}
	writeSlots := slots / 2
	if writeSlots < 1 {
		writeSlots = 1
	}
	return &workLimiter{
		slots:      slots,
		writeSlots: writeSlots,
		queueLimit: slots * workQueuePerSlot,
		wait:       workWait,
		timer: func(wait time.Duration) (<-chan time.Time, func()) {
			timer := time.NewTimer(wait)
			return timer.C, func() { timer.Stop() }
		},
		changed: make(chan struct{}, 1),
	}
}

// LimitWork caps how much work handler does at once. Serve applies it to every
// board, so no caller can serve one without a cap.
func LimitWork(handler http.Handler, limits WorkLimits) http.Handler {
	return newWorkLimiter(limits).wrap(handler)
}

func (limiter *workLimiter) wrap(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// The health check does no work at all, and it is what a proxy or a
		// supervisor asks to decide whether the board is alive. A board that is
		// merely busy is alive, and a check that waited behind the flood would
		// say otherwise and could get it restarted in the middle of its work.
		if request.URL.Path == "/healthz" {
			inner.ServeHTTP(writer, request)
			return
		}
		release, admitted := limiter.acquire(request.Context(), mutatingMethod(request.Method))
		if !admitted {
			writeBusy(writer, fmt.Sprintf(busyWorkMessage, limiter.slots))
			return
		}
		defer release()
		inner.ServeHTTP(writer, request)
	})
}

// The three things a busy board says. Each is a sentence of its own, because a
// client may show it after words of its own.
const (
	busyWorkMessage   = "The board is busy: it is already working on the %d requests it takes at once, so this one was not started. Try again in a moment."
	busyBodyMessage   = "The board is busy: it is already holding as many request bodies as it takes at once, so this one was not started. Try again in a moment."
	busyAnswerMessage = "The board is busy: it is already holding as many answers as it delivers at once, so this read was not kept. Try again in a moment."
)

// writeBusy refuses a request the board had no room for.
//
// 503 with Retry-After is the answer HTTP has for "not now": nothing about the
// request was wrong and nothing was changed by it, so a client may send exactly
// the same request again. The board's own page does, after the wait this names.
// The body is the board's usual error document so a client reading only that
// still learns what happened.
func writeBusy(writer http.ResponseWriter, message string) {
	status, body := busyAnswer(writer.Header(), message)
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}

// busyAnswer sets a refusal's headers and hands back its status and body.
func busyAnswer(header http.Header, message string) (int, []byte) {
	header.Set("Content-Security-Policy", securityPolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Retry-After", strconv.Itoa(workRetryAfterSeconds))
	header.Set("Content-Type", "application/json; charset=utf-8")
	body, _ := json.Marshal(ErrorDocument{
		Format:  "workbook.error",
		Version: 1,
		Error: ErrorBody{
			Category: core.CategoryOperational,
			Message:  message,
		},
	})
	return http.StatusServiceUnavailable, append(body, '\n')
}

// fits reports whether a request of this kind could start now. The caller
// holds the mutex.
func (limiter *workLimiter) fits(write bool) bool {
	if limiter.busy >= limiter.slots {
		return false
	}
	return !write || limiter.busyWrites < limiter.writeSlots
}

// take gives a slot to a request of this kind. The caller holds the mutex and
// has checked that it fits.
func (limiter *workLimiter) take(write bool) {
	limiter.busy++
	if write {
		limiter.busyWrites++
	}
}

// acquire waits for a turn. It answers false when the line is already full,
// when the wait runs out, and when the client goes away first; in every one
// of those cases the request has used no slot and must not be worked on.
func (limiter *workLimiter) acquire(ctx context.Context, write bool) (func(), bool) {
	limiter.mutex.Lock()
	// Starting at once never jumps the line. Every release hands freed slots
	// to the waiting requests before anyone else can look, so a slot that is
	// free now is one no waiting request of this kind could use.
	if limiter.fits(write) {
		limiter.take(write)
		limiter.mutex.Unlock()
		limiter.signal()
		return limiter.releaser(write), true
	}
	if len(limiter.waiting) >= limiter.queueLimit {
		limiter.mutex.Unlock()
		return nil, false
	}
	waiter := &workWaiter{write: write, admitted: make(chan struct{})}
	limiter.waiting = append(limiter.waiting, waiter)
	limiter.mutex.Unlock()
	limiter.signal()

	expired, stop := limiter.timer(limiter.wait)
	defer stop()
	select {
	case <-waiter.admitted:
		return limiter.releaser(write), true
	case <-expired:
	case <-ctx.Done():
	}
	limiter.mutex.Lock()
	if waiter.granted {
		// Handed a slot in the same instant the wait ended. The slot is this
		// request's now, so it is worked on rather than refused: refusing it
		// would mean giving the slot back as well, for a request that got its
		// turn.
		limiter.mutex.Unlock()
		return limiter.releaser(write), true
	}
	for index, candidate := range limiter.waiting {
		if candidate == waiter {
			limiter.waiting = append(limiter.waiting[:index], limiter.waiting[index+1:]...)
			break
		}
	}
	limiter.mutex.Unlock()
	limiter.signal()
	return nil, false
}

// releaser is what a request that got a turn calls once it is done. It gives
// the slot back exactly once, and hands it on to whoever has waited longest
// and fits.
func (limiter *workLimiter) releaser(write bool) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			limiter.mutex.Lock()
			limiter.busy--
			if write {
				limiter.busyWrites--
			}
			remaining := limiter.waiting[:0]
			for _, waiter := range limiter.waiting {
				if limiter.fits(waiter.write) {
					limiter.take(waiter.write)
					waiter.granted = true
					close(waiter.admitted)
					continue
				}
				remaining = append(remaining, waiter)
			}
			// Cleared past the end so the slice does not keep admitted waiters
			// reachable.
			for index := len(remaining); index < len(limiter.waiting); index++ {
				limiter.waiting[index] = nil
			}
			limiter.waiting = remaining
			limiter.mutex.Unlock()
			limiter.signal()
		})
	}
}

func (limiter *workLimiter) signal() {
	select {
	case limiter.changed <- struct{}{}:
	default:
	}
}

func (limiter *workLimiter) state() workState {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	return workState{Busy: limiter.busy, BusyWrites: limiter.busyWrites, Waiting: len(limiter.waiting)}
}
