package webui

import (
	"net"
	"net/http"
	"sync"
)

// connectionLimit keeps at most a fixed number of the board's connections open
// at once, which is what bounds its goroutines: the server runs one for every
// connection, and a second while a handler runs after its body has been read.
//
// When every place is taken, the board first closes the connections that are
// idle between requests, which a client opens again for its next request as a
// matter of course, and otherwise stops accepting until a connection closes.
// One connection past the cap waits here for a place, with no goroutine of its
// own, and every other one waits in the operating system's listen queue. Every other state a connection can be in
// is bounded in time: a request's headers by boardReadHeaderTimeout, its body
// and its answer by the transfer pace, and its work by the work turn.
type connectionLimit struct {
	net.Listener
	places chan struct{}
	closed chan struct{}
	once   sync.Once

	mutex sync.Mutex
	// idle holds the connections between requests, in the order they became
	// idle, so the one idle longest is closed first.
	idle    []net.Conn
	waiting bool
	// changed is signalled, without blocking, whenever the counts move, so a
	// test can wait for a state rather than for a while.
	changed chan struct{}
}

// connectionState is the limit's counts at one moment, for tests.
type connectionState struct {
	Open, Idle int
	Waiting    bool
}

func limitConnections(listener net.Listener, places int) *connectionLimit {
	if places < 1 {
		places = defaultMaxConnections
	}
	return &connectionLimit{
		Listener: listener,
		places:   make(chan struct{}, places),
		closed:   make(chan struct{}),
		changed:  make(chan struct{}, 1),
	}
}

func (limit *connectionLimit) signal() {
	select {
	case limit.changed <- struct{}{}:
	default:
	}
}

// Accept takes a connection from the operating system and then waits for a
// place for it, so at most one connection past the cap is ever held here, and
// it has no goroutine of its own until it has a place.
func (limit *connectionLimit) Accept() (net.Conn, error) {
	connection, err := limit.Listener.Accept()
	if err != nil {
		return nil, err
	}
	select {
	case limit.places <- struct{}{}:
	default:
		limit.setWaiting(true)
		select {
		case limit.places <- struct{}{}:
			limit.setWaiting(false)
		case <-limit.closed:
			limit.setWaiting(false)
			connection.Close()
			return nil, net.ErrClosed
		}
	}
	limit.signal()
	return &limitedConn{Conn: connection, limit: limit}, nil
}

func (limit *connectionLimit) Close() error {
	limit.once.Do(func() { close(limit.closed) })
	return limit.Listener.Close()
}

// setWaiting records whether Accept is waiting for a place. Starting to wait
// closes every idle connection, and while it waits each connection that goes
// idle is closed as it does.
func (limit *connectionLimit) setWaiting(waiting bool) {
	limit.mutex.Lock()
	limit.waiting = waiting
	var idle []net.Conn
	if waiting {
		idle, limit.idle = limit.idle, nil
	}
	limit.mutex.Unlock()
	for _, connection := range idle {
		connection.Close()
	}
	limit.signal()
}

// track is the http.Server.ConnState hook.
func (limit *connectionLimit) track(connection net.Conn, state http.ConnState) {
	limit.mutex.Lock()
	for index, candidate := range limit.idle {
		if candidate == connection {
			limit.idle = append(limit.idle[:index], limit.idle[index+1:]...)
			break
		}
	}
	closing := false
	if state == http.StateIdle {
		if limit.waiting {
			closing = true
		} else {
			limit.idle = append(limit.idle, connection)
		}
	}
	limit.mutex.Unlock()
	if closing {
		connection.Close()
	}
	limit.signal()
}

func (limit *connectionLimit) state() connectionState {
	limit.mutex.Lock()
	defer limit.mutex.Unlock()
	return connectionState{Open: len(limit.places), Idle: len(limit.idle), Waiting: limit.waiting}
}

// limitedConn gives its place back exactly once, when it closes.
type limitedConn struct {
	net.Conn
	limit *connectionLimit
	once  sync.Once
}

func (connection *limitedConn) Close() error {
	err := connection.Conn.Close()
	connection.once.Do(func() {
		<-connection.limit.places
		connection.limit.signal()
	})
	return err
}

// CloseWrite passes the half-close through. The server half-closes a
// connection it is about to close after an answer, then waits a moment before
// closing it, which is what lets a client still sending a body it was refused
// read the refusal instead of a reset.
func (connection *limitedConn) CloseWrite() error {
	if closer, ok := connection.Conn.(interface{ CloseWrite() error }); ok {
		return closer.CloseWrite()
	}
	return nil
}
