package webui

import (
	"context"
	"errors"
	"net"
	"net/http"
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
)

// newBoardServer builds the board's HTTP server with its connection deadlines.
//
// WriteTimeout is deliberately absent. A mutation may publish inline to origin
// before it answers, and how long a push to an unreachable remote takes is not
// something this package can bound honestly; a write deadline would abort the
// response of a mutation that is still going to succeed. Read and idle
// deadlines cover the stalled-connection case a write deadline was never the
// right tool for.
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
func Serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := newBoardServer(GuardSameOrigin(handler, listener.Addr().String()))
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()

	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownContext)
		serveErr := <-result
		if shutdownErr != nil {
			return shutdownErr
		}
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return serveErr
	}
}
