package webui

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// Serve answers the board on an open listener until the context is cancelled.
//
// The handler is wrapped in GuardSameOrigin before it sees a request, using the
// address the listener actually bound rather than the one that was asked for,
// so an ephemeral or resolved port is the one guarded. Applying the guard here
// rather than at each call site means no future board can be served without it.
func Serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{Handler: GuardSameOrigin(handler, listener.Addr().String())}
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
