package syncloop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

func TestReadLineRefusesALineOverTheLimit(t *testing.T) {
	within := append(bytes.Repeat([]byte("a"), 64), '\n')
	line, err := readLine(bufio.NewReader(bytes.NewReader(within)), 128)
	if err != nil {
		t.Fatalf("readLine(within) error = %v", err)
	}
	if !bytes.Equal(line, within) {
		t.Fatalf("readLine(within) = %q, want %q", line, within)
	}

	flood := bytes.Repeat([]byte("a"), 4096)
	if _, err := readLine(bufio.NewReader(bytes.NewReader(flood)), 128); !errors.Is(err, errLineTooLong) {
		t.Fatalf("readLine(flood) error = %v, want errLineTooLong", err)
	}
}

// A peer that never sends a newline must not be able to make the watcher grow a
// buffer until the process dies. The 5 s handler deadline bounds the time, not
// the memory.
func TestWatcherRefusesARequestLineWithoutEnd(t *testing.T) {
	syncer := &fakeSyncer{origin: true}
	directory, output := startWatcher(t, syncer, func(options *Options) {
		options.Interval = time.Hour
	})
	waitForOutput(t, output, ReadyPrefix)

	published, err := readPointer(directory)
	if err != nil {
		t.Fatalf("readPointer() error = %v", err)
	}
	conn, err := net.DialTimeout("unix", published.Socket, probeDeadline)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(handlerDeadline - time.Second))

	started := time.Now()
	block := bytes.Repeat([]byte("a"), 8192)
	written := 0
	for written <= maxRequestBytes {
		n, err := conn.Write(block)
		written += n
		if err != nil {
			// The watcher gave up on the line and closed, which is the point.
			break
		}
	}

	answer, err := readLine(bufio.NewReader(conn), maxResponseBytes)
	// An unbounded reader would hold the line until the handler deadline; a
	// bounded one gives up the moment the limit is passed.
	if elapsed := time.Since(started); elapsed > handlerDeadline/2 {
		t.Fatalf("the watcher held the flooded connection for %v, want a refusal well inside the %v handler deadline", elapsed, handlerDeadline)
	}
	if err == nil && !bytes.Contains(answer, []byte("too long")) {
		t.Fatalf("watcher answered %q, want a refusal", answer)
	}

	// The watcher is still healthy: an ordinary request on a new connection is
	// answered as usual.
	client, err := Dial(directory, probeDeadline)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer client.Close()
	if _, err := client.Status(); err != nil {
		t.Fatalf("Status() after a flooded connection error = %v", err)
	}
}

// The response bound must clear the largest answer a healthy watcher can
// honestly give. A description conflict reports three descriptions, each of
// them up to core.MaxDescriptionBytes, so a bound chosen for a typical status
// line would refuse a watcher that is doing exactly what it should and send
// every mutation down the inline path.
//
// Twenty is the documented figure, so twenty is what this asserts. A bound of
// exactly twenty conflicts' descriptions left nothing for the JSON framing and
// refused the twentieth by 3,477 bytes.
func TestStatusCarryingMaximumDescriptionConflictsStillFits(t *testing.T) {
	const conflicting = 20
	description := strings.Repeat("d", core.MaxDescriptionBytes)
	conflicts := make([]core.Conflict, 0, conflicting)
	heads := make(map[string]string, conflicting)
	for index := range conflicting {
		taskID := fmt.Sprintf("WB-01K0M6B8A4FTT8C39MXXYTW7D%d", index)
		heads[taskID] = "head-1"
		conflicts = append(conflicts, core.Conflict{
			TaskID: taskID,
			Type:   core.ConflictDescription,
			Description: &core.DescriptionConflict{
				Base:   description,
				Ours:   description,
				Theirs: description,
			},
		})
	}

	syncer := &fakeSyncer{origin: true, conflicts: conflicts, heads: heads}
	directory, output := startWatcher(t, syncer, func(options *Options) {
		options.Interval = time.Hour
	})
	waitForOutput(t, output, ReadyPrefix)
	waitForSyncs(t, syncer, 1)

	if entries := readStatus(t, directory).Conflicts; len(entries) != conflicting {
		t.Fatalf("conflicts = %d, want %d", len(entries), conflicting)
	}
}

// A watcher must not be able to outgrow the bound its own clients enforce. If
// it could, the client would refuse the status, never trust the watcher, never
// acknowledge anything, and the set that made the answer too large would never
// drain: the watcher would be wedged out of the deferral path for good while
// still answering every dial.
func TestStatusDropsConflictsPastTheServerBound(t *testing.T) {
	description := strings.Repeat("d", core.MaxDescriptionBytes)
	entries := make([]ConflictEntry, 0, 64)
	for index := range 64 {
		entries = append(entries, ConflictEntry{
			Conflict: core.Conflict{
				TaskID: fmt.Sprintf("WB-01K0M6B8A4FTT8C39MXXYTW%03d", index),
				Type:   core.ConflictDescription,
				Description: &core.DescriptionConflict{
					Base:   description,
					Ours:   description,
					Theirs: description,
				},
			},
			Head: strings.Repeat("a", 40),
		})
	}

	bounded := boundConflicts(entries)
	if len(bounded) == len(entries) {
		t.Fatalf("boundConflicts() kept all %d entries, want the tail dropped", len(entries))
	}
	if len(bounded) < 20 {
		t.Fatalf("boundConflicts() kept %d entries, want at least the documented twenty", len(bounded))
	}
	for index := range bounded {
		if bounded[index].TaskID != entries[index].TaskID {
			t.Fatalf("boundConflicts() entry %d = %q, want the sorted prefix %q", index, bounded[index].TaskID, entries[index].TaskID)
		}
	}

	answer := response{OK: true, Status: &Status{
		Format:    StatusFormat,
		Version:   StatusVersion,
		Conflicts: bounded,
	}}
	encoded, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if len(encoded)+1 > maxResponseBytes {
		t.Fatalf("bounded status marshalled to %d bytes, want at most the client's %d", len(encoded)+1, maxResponseBytes)
	}
}

// The client is bounded the same way, because a watcher is not automatically
// trusted either: the pointer file names a socket, and whatever answers it can
// reply with as much as it likes.
func TestClientRefusesAnOversizedResponse(t *testing.T) {
	directory := t.TempDir()
	socket := filepath.Join(directory, "s.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := readLine(bufio.NewReader(conn), maxRequestBytes); err != nil {
			return
		}
		// Never newline-terminated and never closed, so only a bound on the
		// client's side can end this.
		block := bytes.Repeat([]byte("a"), 1<<16)
		for {
			if _, err := conn.Write(block); err != nil {
				return
			}
		}
	}()

	conn, err := net.DialTimeout("unix", socket, probeDeadline)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	client := &Client{conn: conn, reader: bufio.NewReader(conn), deadline: 30 * time.Second}
	defer client.Close()
	_, err = client.Status()
	if err == nil {
		t.Fatal("Status() error = nil, want a refusal of the oversized response")
	}
	if !strings.Contains(err.Error(), errLineTooLong.Error()) {
		t.Fatalf("Status() error = %v, want it to name the line limit", err)
	}
}
