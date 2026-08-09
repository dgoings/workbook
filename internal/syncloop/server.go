package syncloop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// handlerDeadline bounds one request exchange. A client that connects and says
// nothing must not be able to hold a handler goroutine open.
const handlerDeadline = 5 * time.Second

// server answers status, nudge, and acknowledgement requests.
//
// It shares nothing with the synchronizing goroutine that could block: timing
// comes from an atomic snapshot, conflicts from a mutex held only for map
// operations, and a nudge is a non-blocking send. A watcher wedged on a hung
// fetch therefore still answers, reporting a stale lastSyncAt, and a command
// falls back on the staleness rule rather than on a timeout.
type server struct {
	listener  net.Listener
	snapshot  *atomic.Pointer[Status]
	conflicts *conflictSet
	nudges    chan struct{}
}

// bind claims this repository's socket, refusing to start when another watcher
// already answers. A socket left behind by SIGKILL answers nothing, so it is
// removed and rebound.
func bind(commonGitDir string) (net.Listener, string, error) {
	path, err := socketPath(commonGitDir)
	if err != nil {
		return nil, "", err
	}
	if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = conn.Close()
		return nil, "", ErrWatcherLive
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, "", core.Wrap(core.CategoryOperational, "clear the stale watcher socket", err)
	}
	listener, err := listenPrivate(path)
	if err != nil {
		return nil, "", core.Wrap(core.CategoryOperational, "bind the watcher socket", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, "", core.Wrap(core.CategoryOperational, "restrict the watcher socket", err)
	}
	return listener, path, nil
}

func (s *server) serve(ctx context.Context) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// Accept fails permanently once the listener closes, which is how
			// shutdown reaches this goroutine.
			return
		}
		go s.handle(ctx, conn)
	}
}

func (s *server) handle(_ context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(handlerDeadline))

	line, err := readLine(bufio.NewReader(conn), maxRequestBytes)
	if err != nil {
		if errors.Is(err, errLineTooLong) {
			s.reply(conn, response{Error: "request is too long"})
		}
		return
	}
	var message request
	if err := json.Unmarshal(line, &message); err != nil {
		s.reply(conn, response{Error: "request is not valid JSON"})
		return
	}

	switch {
	case message.Status != nil:
		s.reply(conn, response{OK: true, Status: s.status()})
	case message.Nudge != nil:
		s.wake()
		s.reply(conn, response{OK: true})
	case message.Ack != nil:
		s.conflicts.acknowledge(message.Ack.TaskID)
		s.reply(conn, response{OK: true})
	default:
		s.reply(conn, response{Error: "request names no command"})
	}
}

// status composes the timing snapshot with the live conflict set, so an
// acknowledgement is visible immediately rather than at the next publish.
func (s *server) status() *Status {
	current := *s.snapshot.Load()
	current.Conflicts = s.conflicts.list()
	return &current
}

// wake records that work is pending without ever blocking the caller. A nudge
// arriving while one is already pending is absorbed, which is exactly the
// coalescing a burst of mutations needs.
func (s *server) wake() {
	select {
	case s.nudges <- struct{}{}:
	default:
	}
}

func (s *server) reply(conn net.Conn, answer response) {
	encoded, err := json.Marshal(answer)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(encoded, '\n'))
}
