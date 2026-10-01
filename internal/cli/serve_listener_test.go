package cli

import (
	"net"
	"regexp"
	"sync"
	"testing"
	"time"
)

// reservedListener is an already-bound listener together with the bind hook
// that hands it to the code under test.
//
// The pattern it replaces reserved a port by binding 127.0.0.1:0, closing the
// probe, and passing the address on for serve to bind again. Between the close
// and the rebind the port is free, so another test in this parallel package
// can be handed the very port that was just released and the rebind then fails
// with EADDRINUSE. Handing the still-open probe over removes the window rather
// than narrowing it: the port is never free between the reservation and the
// serve, because it is the same listener throughout.
type reservedListener struct {
	// addr is the address the probe holds. A test passes it as --addr, which is
	// what lets listen recognize its own reservation.
	addr string

	mu       sync.Mutex
	probe    net.Listener
	handedOn bool
}

// reserveListener reserves an OS-assigned loopback port.
func reserveListener(t *testing.T) *reservedListener {
	t.Helper()
	return reserveListenerOn(t, "127.0.0.1")
}

// reserveListenerOn reserves an OS-assigned port on host. A test that wants the
// wildcard bind serve warns about asks for "0.0.0.0" here rather than binding
// loopback and renaming the address, because the warning and the banner are
// both read off the address the listener itself reports.
func reserveListenerOn(t *testing.T, host string) *reservedListener {
	t.Helper()
	probe, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatalf("reserve a port on %s: %v", host, err)
	}
	reserved := &reservedListener{addr: probe.Addr().String(), probe: probe}
	// Only a reservation nobody took is this helper's to close: once serve owns
	// the listener it closes it on shutdown. A test's own cleanup is what stops
	// serve, and it is registered after this one, so it runs before it.
	t.Cleanup(func() {
		reserved.mu.Lock()
		defer reserved.mu.Unlock()
		if reserved.handedOn {
			return
		}
		_ = reserved.probe.Close()
	})
	return reserved
}

// listen is the bind to hand runServeWith or openBoardListenerWith. It answers
// the one address it reserved with the open probe, once, and delegates
// everything else — serve's port-zero fallback among them — to the real
// net.Listen. A second request for the reserved address therefore fails the way
// any occupied port does instead of handing two servers the same listener.
func (r *reservedListener) listen(network, address string) (net.Listener, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.handedOn && address == r.addr {
		r.handedOn = true
		return r.probe, nil
	}
	return net.Listen(network, address)
}

// boardBanner matches the line serve prints to say where the board is.
var boardBanner = regexp.MustCompile(`Workbook board: http://(\S+)`)

// waitForBoardAddress returns the address serve announced, waiting for the
// banner the way waitForWatcherReady waits for the watcher's readiness line.
//
// It is how a test that reaches serve through the exported Run learns the port:
// Run takes no bind, so there is no reservation to hand over, and asking for
// port zero leaves the choice inside serve where only the banner reports it.
func waitForBoardAddress(t *testing.T, output *watcherOutput) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second) // hang guard, not a measurement; see waitForHTTP
	for time.Now().Before(deadline) {
		if match := boardBanner.FindStringSubmatch(output.String()); match != nil {
			return match[1]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("serve never announced the board address; wrote %q", output.String())
	return ""
}
