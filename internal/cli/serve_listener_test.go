package cli

import (
	"errors"
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
	// dead records that the cleanup closed an untaken reservation. A bind asked
	// for after that point must be refused rather than answered, which is what
	// the field is for; see listen.
	dead bool
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
		reserved.dead = true
	})
	return reserved
}

// listen is the bind to hand runServeWith or openBoardListenerWith. It answers
// the one tcp address it reserved with the open probe, once, and delegates
// everything else — serve's port-zero fallback among them — to the real
// net.Listen. A second request for the reserved address therefore fails the way
// any occupied port does instead of handing two servers the same listener.
func (r *reservedListener) listen(network, address string) (net.Listener, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Once the cleanup has closed an untaken reservation the test is over, and
	// a bind arriving now is a straggler goroutine, not a server anybody will
	// use. Handing back the closed probe would give it a listener whose every
	// Accept fails, and binding the address afresh would reopen the very window
	// this helper closes, so the only safe answer is to refuse.
	if r.dead {
		return nil, net.ErrClosed
	}
	if !r.handedOn && network == "tcp" && address == r.addr {
		r.handedOn = true
		return r.probe, nil
	}
	return net.Listen(network, address)
}

// The whole point of the reservation is identity: serve has to end up holding
// the listener the test already bound. Every converted test would stay green if
// listen quietly closed the probe and rebound the same port — the address would
// still match and the board would still answer — while the close-then-rebind
// window it exists to close was back. So the identity is asserted here, where
// nothing else can stand in for it.
func TestReservedListenerHandsOverItsOwnListenerOnce(t *testing.T) {
	t.Parallel()
	reserved := reserveListener(t)

	// A different network is not this reservation, whatever the address says,
	// and asking must not consume it either. "udp" is the probe because
	// net.Listen refuses it outright, so the delegation below leaves nothing
	// behind — asking for "unix" here would create a socket file named after
	// the address in the package directory.
	if other, err := reserved.listen("udp", reserved.addr); err == nil {
		other.Close()
		t.Fatalf("listen(udp, %q) handed over a tcp reservation", reserved.addr)
	}

	got, err := reserved.listen("tcp", reserved.addr)
	if err != nil {
		t.Fatalf("listen(tcp, %q) error = %v, want the reservation", reserved.addr, err)
	}
	// Handed on, so the helper's cleanup steps aside and this test owns it.
	defer got.Close()
	if got != reserved.probe {
		t.Fatalf("listen(tcp, %q) returned a different listener (%v), want the reserved one (%v)", reserved.addr, got.Addr(), reserved.probe.Addr())
	}

	// And only once: the reservation is still open, so the second ask falls
	// through to net.Listen and fails the way any occupied port does, rather
	// than handing a second server the same listener.
	second, err := reserved.listen("tcp", reserved.addr)
	if err == nil {
		second.Close()
		t.Fatalf("listen(tcp, %q) handed the reservation out twice", reserved.addr)
	}
}

// A reservation nobody took is closed by its cleanup, and a straggler bind
// after that must be refused rather than handed a listener that cannot accept
// or quietly given the released port back.
func TestReservedListenerRefusesAReservationItsCleanupClosed(t *testing.T) {
	t.Parallel()
	var reserved *reservedListener
	// The cleanup fires when the subtest ends, which is the only way to reach
	// the closed state from inside a test.
	t.Run("reserve", func(t *testing.T) {
		reserved = reserveListener(t)
	})

	got, err := reserved.listen("tcp", reserved.addr)
	if got != nil {
		got.Close()
	}
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("listen(tcp, %q) after the cleanup closed it = (%v, %v), want net.ErrClosed", reserved.addr, got, err)
	}
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
