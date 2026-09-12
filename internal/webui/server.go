package webui

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// Connection deadlines for the board.
//
// An http.Server with no deadlines keeps a goroutine and a file descriptor for
// every connection that opens and then stops talking, until the process exits.
// The board listens on the loopback interface, so the sender is a local process
// rather than the internet, but a browser tab left open across a laptop suspend
// or a script that dies mid-request is enough to accumulate them.
//
// The values are generous because nothing legitimate is slow here: a local
// browser sends a whole request at once, and MaxRequestBodyBytes already bounds
// how much of one there can be.
const (
	boardReadHeaderTimeout = 10 * time.Second
	boardReadTimeout       = 30 * time.Second
	boardIdleTimeout       = 2 * time.Minute
	// boardShutdownGrace is all the time a request that is already in flight
	// gets to finish its response once the board has been asked to stop. Someone
	// who has finished for the day and pressed Ctrl+C is waiting at a prompt, so
	// the wait has to read as "it quit", not as "it hung": two seconds is long
	// enough for a request the board has already received to record its change
	// and write an answer, all of which is local work, and short enough not to
	// be noticed. It must stay well under boardReadHeaderTimeout — a grace at or
	// beyond that bound is the mismatch that made Ctrl+C wait out a browser's
	// spare socket and then report a deadline as a failure.
	boardShutdownGrace = 2 * time.Second
)

// newBoardServer builds the board's HTTP server with its connection deadlines.
//
// WriteTimeout is deliberately absent. A mutation may publish inline to origin
// before it answers, and how long a push to an unreachable remote takes is not
// something this package can bound honestly; a write deadline would abort the
// response of a mutation that is still going to succeed. Read and idle
// deadlines cover the stalled-connection case a write deadline was never the
// right tool for.
//
// Shutdown is the one place that wait is bounded, and boardShutdownGrace is
// what bounds it. The two are not in tension: while the board is serving, an
// answer is worth waiting for, but once the user has asked the process to stop
// there is nothing left to wait for, because the change is already recorded
// locally before anything is published. Cutting a push short leaves the project
// exactly where a failed publish leaves it — recorded, unpublished, and picked
// up by the next sync — which is a state the design already treats as normal.
func newBoardServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: boardReadHeaderTimeout,
		ReadTimeout:       boardReadTimeout,
		IdleTimeout:       boardIdleTimeout,
	}
}

// Serve answers the board on an open listener until the context is cancelled.
//
// The handler is wrapped in GuardSameOrigin before it sees a request, using the
// address the listener actually bound rather than the one that was asked for,
// so an ephemeral or resolved port is the one guarded. Applying the guard here
// rather than at each call site means no future board can be served without it.
//
// Shutting down is quick whatever is connected. A connection with no request on
// it is dropped outright, a request already in flight has boardShutdownGrace to
// answer, and whatever is still going after that is dropped too and reported as
// a clean exit, because being told to stop is not a failure to serve.
func Serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := newBoardServer(GuardSameOrigin(handler, listener.Addr().String()))
	awaited := &awaitedConnections{}
	server.ConnState = awaited.track
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()

	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// Shutdown closes idle connections itself but waits on every connection
		// that has not delivered a complete request, which is what a browser's
		// pre-opened spare socket looks like. Dropping those first is what keeps
		// a Ctrl+C with a tab open as quick as one without.
		awaited.dropAll()
		shutdownContext, cancel := context.WithTimeout(context.Background(), boardShutdownGrace)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			// The grace ran out with a handler still working, or the listener
			// would not close cleanly. Either way the wait is over by design, so
			// drop what is left rather than report the deadline as a failure.
			server.Close()
		}
		serveErr := <-result
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return serveErr
	}
}

// awaitedConnections holds the connections shutdown would otherwise wait on: the
// ones accepted but not yet carrying a complete request. Go's Shutdown closes
// idle connections for itself and gives every other connection the full
// boardReadHeaderTimeout to produce a request, which a socket a browser opened
// speculatively never will.
type awaitedConnections struct {
	mutex    sync.Mutex
	stopping bool
	open     map[net.Conn]struct{}
}

// track is the http.Server.ConnState hook. A connection is worth dropping only
// while it sits in StateNew; once it is active it belongs to a request, and once
// it is idle or closed Shutdown deals with it.
func (awaited *awaitedConnections) track(connection net.Conn, state http.ConnState) {
	awaited.mutex.Lock()
	defer awaited.mutex.Unlock()
	if state != http.StateNew {
		delete(awaited.open, connection)
		return
	}
	// A connection accepted in the instant between dropAll and the listener
	// closing would otherwise be waited on by the shutdown already underway.
	if awaited.stopping {
		connection.Close()
		return
	}
	if awaited.open == nil {
		awaited.open = make(map[net.Conn]struct{})
	}
	awaited.open[connection] = struct{}{}
}

// dropAll closes every connection that has yet to send a request, and leaves
// this tracker closing any that arrive afterwards.
func (awaited *awaitedConnections) dropAll() {
	awaited.mutex.Lock()
	defer awaited.mutex.Unlock()
	awaited.stopping = true
	for connection := range awaited.open {
		connection.Close()
	}
	awaited.open = nil
}
