package webui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// serveInBackground starts Serve on a fresh loopback listener and hands back the
// address to talk to, the cancel that stands in for Ctrl+C, and the channel
// Serve's result arrives on.
func serveInBackground(t *testing.T, handler http.Handler) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- Serve(ctx, listener, handler) }()
	return address, cancel, result
}

// awaitShutdown waits for Serve to return, failing if it takes longer than a
// user pressing Ctrl+C would sit through.
func awaitShutdown(t *testing.T, result <-chan error, within time.Duration) {
	t.Helper()
	started := time.Now()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve() error = %v after %v, want nil", err, time.Since(started))
		}
	case <-time.After(within):
		t.Fatalf("Serve() had not returned %v after the context was cancelled", within)
	}
}

// TestServeDoesNotWaitForAConnectionThatSentNothing is the regression test for
// the reported bug: a browser routinely opens a spare socket it never sends a
// request on, and shutdown used to wait out the read-header timeout for a
// request that was never coming.
func TestServeDoesNotWaitForAConnectionThatSentNothing(t *testing.T) {
	address, cancel, result := serveInBackground(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, "answered")
	}))

	silent, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("dial board: %v", err)
	}
	defer silent.Close()

	// A listener hands out connections in the order they arrived, so an answer
	// on a connection opened after the silent one proves the silent one has
	// already been accepted and is sitting in the state shutdown used to wait
	// on. Waiting for that beats sleeping and guessing.
	if body := askOnItsOwnConnection(t, address); body != "answered" {
		t.Fatalf("probe response body = %q, want %q", body, "answered")
	}

	cancel()
	awaitShutdown(t, result, time.Second)
}

// askOnItsOwnConnection makes one request on a connection of its own, so no
// pooled connection from elsewhere in the package can answer it, and returns the
// body.
func askOnItsOwnConnection(t *testing.T, address string) string {
	t.Helper()
	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("dial board: %v", err)
	}
	defer connection.Close()
	request, err := http.NewRequest(http.MethodGet, "http://"+address+"/anything", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := request.Write(connection); err != nil {
		t.Fatalf("write request: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), request)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

// TestServeLetsAnInFlightRequestFinish covers the other half: a request the
// board has already accepted gets its brief grace to answer, so a save that was
// received is not cut off mid-write.
func TestServeLetsAnInFlightRequestFinish(t *testing.T) {
	entered := make(chan struct{})
	address, cancel, result := serveInBackground(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(entered)
		time.Sleep(250 * time.Millisecond)
		fmt.Fprint(writer, "answered")
	}))

	answer := make(chan string, 1)
	failure := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + address + "/anything")
		if err != nil {
			failure <- err
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			failure <- err
			return
		}
		answer <- string(body)
	}()

	select {
	case <-entered:
	case err := <-failure:
		t.Fatalf("GET board: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the handler")
	}
	cancel()

	select {
	case body := <-answer:
		if body != "answered" {
			t.Fatalf("response body = %q, want %q", body, "answered")
		}
	case err := <-failure:
		t.Fatalf("GET board: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight request never finished")
	}
	awaitShutdown(t, result, 5*time.Second)
}

// TestServeDoesNotWaitForAHandlerBeyondTheGrace holds the line the whole task is
// about: a handler that keeps working past the grace stops being shutdown's
// problem, and giving up on it is a normal exit rather than an error.
func TestServeDoesNotWaitForAHandlerBeyondTheGrace(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	address, cancel, result := serveInBackground(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		fmt.Fprint(writer, "too late")
	}))

	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("dial board: %v", err)
	}
	defer connection.Close()
	request, err := http.NewRequest(http.MethodGet, "http://"+address+"/anything", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := request.Write(connection); err != nil {
		t.Fatalf("write request: %v", err)
	}

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the handler")
	}
	cancel()

	awaitShutdown(t, result, boardShutdownGrace+2*time.Second)

	// The board dropped the connection rather than waiting for the answer.
	if err := connection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := http.ReadResponse(bufio.NewReader(connection), request); err == nil {
		t.Fatal("read response: got an answer, want the dropped connection")
	}
}
