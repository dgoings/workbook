package webui

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

func TestServeStopsCleanlyWhenContextIsCancelled(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- Serve(ctx, listener, NewHandler(func(context.Context) ([]core.Task, error) { return nil, nil }))
	}()

	response, err := http.Get("http://" + listener.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("GET healthz: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET healthz status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not stop after context cancellation")
	}
}

func TestServeReturnsUnexpectedHTTPFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	err = Serve(context.Background(), listener, NewHandler(func(context.Context) ([]core.Task, error) { return nil, nil }))
	if err == nil {
		t.Fatal("Serve() error = nil, want closed-listener failure")
	}
	if errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve() error = %v, want unexpected listener failure", err)
	}
}
