// Package watchertest provides a scriptable stand-in for a sync watcher.
//
// It speaks the wire protocol from the outside rather than reusing the loop's
// internals, so a test can put a watcher into states a real one reaches only
// rarely — wedged, stale, or holding a conflict — and the protocol itself stays
// pinned by something that does not share its implementation.
package watchertest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/syncloop"
)

var sequence atomic.Int64

// Recorder is a fake watcher. It answers with a fixed status and remembers the
// nudges and acknowledgements it received.
type Recorder struct {
	mu       sync.Mutex
	status   syncloop.Status
	nudges   []string
	acks     []string
	refuse   bool
	listener net.Listener
	socket   string
	dir      string
	stopped  bool
}

// Start publishes a watcher for commonGitDir and serves the given status until
// the test ends.
func Start(t *testing.T, commonGitDir string, status syncloop.Status) *Recorder {
	t.Helper()
	if status.Format == "" {
		status.Format = syncloop.StatusFormat
	}
	if status.Version == 0 {
		status.Version = syncloop.StatusVersion
	}
	if status.Conflicts == nil {
		status.Conflicts = []syncloop.ConflictEntry{}
	}

	socket := filepath.Join(os.TempDir(), fmt.Sprintf("wbt-%d-%d.sock", os.Getpid(), sequence.Add(1)))
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("watchertest listen: %v", err)
	}
	recorder := &Recorder{status: status, listener: listener, socket: socket, dir: commonGitDir}

	if err := os.MkdirAll(filepath.Dir(syncloop.PointerPath(commonGitDir)), 0o755); err != nil {
		t.Fatalf("watchertest pointer directory: %v", err)
	}
	published, err := json.Marshal(map[string]any{
		"format":  syncloop.PointerFormat,
		"version": syncloop.PointerVersion,
		"socket":  socket,
		"pid":     os.Getpid(),
	})
	if err != nil {
		t.Fatalf("watchertest pointer: %v", err)
	}
	if err := os.WriteFile(syncloop.PointerPath(commonGitDir), append(published, '\n'), 0o600); err != nil {
		t.Fatalf("watchertest pointer write: %v", err)
	}

	go recorder.serve()
	t.Cleanup(recorder.Stop)
	return recorder
}

// StartDead publishes a pointer to a socket nothing answers, which is what a
// watcher killed with SIGKILL leaves behind.
func StartDead(t *testing.T, commonGitDir string) {
	t.Helper()
	recorder := Start(t, commonGitDir, syncloop.Status{LastSyncAt: time.Now(), LastSyncOK: true, IntervalMS: 5000})
	recorder.Stop()
}

// RefuseNudges makes the watcher reject nudges, standing in for one that is
// listening but can no longer publish.
func (r *Recorder) RefuseNudges() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refuse = true
}

func (r *Recorder) Nudges() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.nudges...)
}

func (r *Recorder) Acknowledgements() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.acks...)
}

func (r *Recorder) Stop() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	r.mu.Unlock()
	_ = r.listener.Close()
	_ = os.Remove(r.socket)
}

func (r *Recorder) serve() {
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			return
		}
		go r.handle(conn)
	}
}

func (r *Recorder) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}

	var message struct {
		Status *struct{} `json:"status"`
		Nudge  *struct {
			TaskID string `json:"taskId"`
		} `json:"nudge"`
		Ack *struct {
			TaskID string `json:"taskId"`
			Head   string `json:"head"`
		} `json:"ack"`
	}
	if err := json.Unmarshal(line, &message); err != nil {
		return
	}

	r.mu.Lock()
	answer := map[string]any{"ok": true}
	switch {
	case message.Status != nil:
		answer["status"] = r.status
	case message.Nudge != nil:
		if r.refuse {
			answer = map[string]any{"ok": false, "error": "watcher cannot publish"}
		} else {
			r.nudges = append(r.nudges, message.Nudge.TaskID)
		}
	case message.Ack != nil:
		r.acks = append(r.acks, message.Ack.TaskID)
	}
	r.mu.Unlock()

	encoded, err := json.Marshal(answer)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(encoded, '\n'))
}
