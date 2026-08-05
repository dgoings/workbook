package syncloop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

const probeDeadline = 500 * time.Millisecond

// The request handler shares no lock with the synchronizing goroutine, so a
// watcher wedged on a hung fetch still answers. Without this a command would
// pay the full network timeout before falling back.
func TestStatusAnswersWhileASyncIsBlocked(t *testing.T) {
	release := make(chan struct{})
	syncer := &fakeSyncer{origin: true, block: release}
	directory, output := startWatcher(t, syncer, func(options *Options) {
		options.Interval = time.Hour
	})
	defer close(release)
	waitForOutput(t, output, ReadyPrefix)

	answered := make(chan Status, 1)
	go func() {
		client, err := Dial(directory, probeDeadline)
		if err != nil {
			return
		}
		defer client.Close()
		status, err := client.Status()
		if err != nil {
			return
		}
		answered <- status
	}()

	select {
	case status := <-answered:
		if status.Format != StatusFormat {
			t.Fatalf("status format = %q, want %q", status.Format, StatusFormat)
		}
		if status.Trustworthy(time.Now()) {
			t.Fatal("a watcher that has never completed a sync reported itself trustworthy")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("status did not answer while a synchronization was blocked")
	}
}

func TestBurstOfNudgesCoalescesIntoOneFollowUpSync(t *testing.T) {
	syncer := &fakeSyncer{origin: true}
	directory, output := startWatcher(t, syncer, func(options *Options) {
		options.Interval = time.Hour
		options.Quiet = 150 * time.Millisecond
	})
	waitForOutput(t, output, ReadyPrefix)
	waitForSyncs(t, syncer, 1)

	for range 10 {
		nudge(t, directory)
	}
	waitForSyncs(t, syncer, 2)
	time.Sleep(300 * time.Millisecond)

	if got := syncer.syncCount(); got != 2 {
		t.Fatalf("syncs after a burst of 10 nudges = %d, want 2 (the opening sync and one coalesced follow-up)", got)
	}
}

func TestScheduledTickIsSuppressedWhileANudgeIsPending(t *testing.T) {
	syncer := &fakeSyncer{origin: true}
	directory, output := startWatcher(t, syncer, func(options *Options) {
		options.Interval = 20 * time.Millisecond
		options.Quiet = 400 * time.Millisecond
	})
	waitForOutput(t, output, ReadyPrefix)
	waitForSyncs(t, syncer, 1)

	nudge(t, directory)
	// Sample inside the quiet window. Roughly fifteen ticks fall in it, and
	// every one must be skipped because a nudged synchronization is already
	// pending; only the opening synchronization should have happened.
	time.Sleep(300 * time.Millisecond)
	if got := syncer.syncCount(); got != 1 {
		t.Fatalf("syncs inside the quiet window = %d, want 1 (the ticks suppressed)", got)
	}

	// The pending synchronization still has to arrive.
	waitForSyncs(t, syncer, 2)
}

func TestLoopIsAQuietNoOpWithoutAnOrigin(t *testing.T) {
	syncer := &fakeSyncer{origin: false}
	_, output := startWatcher(t, syncer, func(options *Options) {
		options.Interval = 20 * time.Millisecond
	})
	waitForOutput(t, output, ReadyPrefix)
	time.Sleep(150 * time.Millisecond)

	if got := syncer.syncCount(); got != 0 {
		t.Fatalf("syncs with no origin = %d, want 0", got)
	}
	remainder := strings.TrimPrefix(output.String(), output.String()[:strings.IndexByte(output.String(), '\n')+1])
	if strings.TrimSpace(remainder) != "" {
		t.Fatalf("watcher with no origin wrote %q, want only the readiness line", remainder)
	}
}

func TestFinalSyncRunsOnCancellation(t *testing.T) {
	syncer := &fakeSyncer{origin: true}
	ctx, cancel := context.WithCancel(context.Background())
	output := &syncBuffer{}
	directory := t.TempDir()
	finished := make(chan error, 1)
	go func() {
		finished <- Run(ctx, Options{
			CommonGitDir: directory,
			Repository:   syncer,
			Config:       watcherConfig(),
			Interval:     time.Hour,
			Stderr:       output,
		})
	}()
	waitForOutput(t, output, ReadyPrefix)
	waitForSyncs(t, syncer, 1)

	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	if got := syncer.syncCount(); got != 2 {
		t.Fatalf("syncs = %d, want 2 (the opening sync and the final one)", got)
	}
	if syncer.lastContextErr != nil {
		t.Fatalf("the final sync ran on a cancelled context: %v", syncer.lastContextErr)
	}
}

func TestConflictEntryExpiresWhenTheTaskHeadMoves(t *testing.T) {
	const taskID = "WB-01K0M6B8A4FTT8C39MXXYTW7D1"
	syncer := &fakeSyncer{
		origin:    true,
		heads:     map[string]string{taskID: "head-1"},
		conflicts: []core.Conflict{{TaskID: taskID, Type: core.ConflictDescription, Description: &core.DescriptionConflict{}}},
	}
	directory, output := startWatcher(t, syncer, func(options *Options) {
		options.Interval = time.Hour
		options.Quiet = 10 * time.Millisecond
	})
	waitForOutput(t, output, ReadyPrefix)
	waitForSyncs(t, syncer, 1)

	if entries := readStatus(t, directory).Conflicts; len(entries) != 1 || entries[0].TaskID != taskID {
		t.Fatalf("conflicts after the reporting sync = %#v, want one for %s", entries, taskID)
	}

	syncer.setConflicts(nil)
	syncer.setHead(taskID, "head-2")
	nudge(t, directory)
	waitForSyncs(t, syncer, 2)

	if entries := readStatus(t, directory).Conflicts; len(entries) != 0 {
		t.Fatalf("conflicts after the task head moved = %#v, want none", entries)
	}
}

func TestAcknowledgementRemovesOneConflict(t *testing.T) {
	const reported = "WB-01K0M6B8A4FTT8C39MXXYTW7D1"
	const other = "WB-01K0M6B8A4FTT8C39MXXYTW7D2"
	syncer := &fakeSyncer{
		origin: true,
		heads:  map[string]string{reported: "head-1", other: "head-1"},
		conflicts: []core.Conflict{
			{TaskID: reported, Type: core.ConflictDescription, Description: &core.DescriptionConflict{}},
			{TaskID: other, Type: core.ConflictDescription, Description: &core.DescriptionConflict{}},
		},
	}
	directory, output := startWatcher(t, syncer, func(options *Options) {
		options.Interval = time.Hour
	})
	waitForOutput(t, output, ReadyPrefix)
	waitForSyncs(t, syncer, 1)
	if entries := readStatus(t, directory).Conflicts; len(entries) != 2 {
		t.Fatalf("conflicts = %#v, want two", entries)
	}

	client, err := Dial(directory, probeDeadline)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	if err := client.Acknowledge(reported); err != nil {
		t.Fatalf("Acknowledge() error = %v", err)
	}
	client.Close()

	entries := readStatus(t, directory).Conflicts
	if len(entries) != 1 || entries[0].TaskID != other {
		t.Fatalf("conflicts after acknowledging %s = %#v, want only %s", reported, entries, other)
	}
}

// A conflict occurs with nobody at the keyboard and vanishes from Git on the
// next fetch, so the watcher's terminal is the only place it is ever observable.
func TestWatcherReportsEachConflictToItsTerminalOnce(t *testing.T) {
	const taskID = "WB-01K0M6B8A4FTT8C39MXXYTW7D1"
	syncer := &fakeSyncer{
		origin:    true,
		heads:     map[string]string{taskID: "head-1"},
		conflicts: []core.Conflict{{TaskID: taskID, Type: core.ConflictDescription, Description: &core.DescriptionConflict{}}},
	}
	directory, output := startWatcher(t, syncer, func(options *Options) {
		options.Interval = time.Hour
		options.Quiet = 10 * time.Millisecond
	})
	waitForOutput(t, output, ReadyPrefix)
	waitForOutput(t, output, "conflict on "+taskID)

	nudge(t, directory)
	waitForSyncs(t, syncer, 2)

	if got := strings.Count(output.String(), "conflict on "+taskID); got != 1 {
		t.Fatalf("conflict reported %d times, want once", got)
	}
}

func TestSecondWatcherReportsTheRepositoryIsAlreadyOwned(t *testing.T) {
	syncer := &fakeSyncer{origin: true}
	directory, output := startWatcher(t, syncer, func(options *Options) {
		options.Interval = time.Hour
	})
	waitForOutput(t, output, ReadyPrefix)

	err := Run(context.Background(), Options{
		CommonGitDir: directory,
		Repository:   &fakeSyncer{origin: true},
		Config:       watcherConfig(),
		Interval:     time.Hour,
		Stderr:       &syncBuffer{},
	})
	if !errors.Is(err, ErrWatcherLive) {
		t.Fatalf("second Run() error = %v, want ErrWatcherLive", err)
	}
}

func TestDialReportsNoWatcherWithoutAPointer(t *testing.T) {
	if _, err := Dial(t.TempDir(), probeDeadline); !errors.Is(err, ErrNoWatcher) {
		t.Fatalf("Dial(no pointer) error = %v, want ErrNoWatcher", err)
	}
}

func TestDialReportsNoWatcherForADeadSocket(t *testing.T) {
	directory := t.TempDir()
	published := pointer{
		Format:  PointerFormat,
		Version: PointerVersion,
		Socket:  filepath.Join(directory, "absent.sock"),
		PID:     os.Getpid(),
	}
	if err := writePointer(directory, published); err != nil {
		t.Fatalf("writePointer() error = %v", err)
	}
	if _, err := Dial(directory, probeDeadline); !errors.Is(err, ErrNoWatcher) {
		t.Fatalf("Dial(dead socket) error = %v, want ErrNoWatcher", err)
	}
}

func TestPointerRoundTripsThroughTheRepositoryDirectory(t *testing.T) {
	directory := t.TempDir()
	published := pointer{Format: PointerFormat, Version: PointerVersion, Socket: "/tmp/wb-test.sock", PID: 4211}
	if err := writePointer(directory, published); err != nil {
		t.Fatalf("writePointer() error = %v", err)
	}
	read, err := readPointer(directory)
	if err != nil {
		t.Fatalf("readPointer() error = %v", err)
	}
	if read != published {
		t.Fatalf("readPointer() = %#v, want %#v", read, published)
	}

	if err := os.WriteFile(pointerPath(directory), []byte(`{"format":"other","version":1,"socket":"/x"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := readPointer(directory); err == nil {
		t.Fatal("readPointer(foreign format) error = nil, want a rejection")
	}
}

func TestSocketPathFallsBackWhenTheCandidateIsTooLong(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 90))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	t.Setenv("TMPDIR", long)

	path, err := socketPath(t.TempDir())
	if err != nil {
		t.Fatalf("socketPath() error = %v", err)
	}
	if len(path) > maxSocketPath {
		t.Fatalf("socketPath() = %q (%d bytes), want at most %d", path, len(path), maxSocketPath)
	}
	if strings.HasPrefix(path, long) {
		t.Fatalf("socketPath() = %q, want a fallback outside the oversized temporary directory", path)
	}
}

// --- helpers ---

func watcherConfig() core.ProjectConfig {
	return core.ProjectConfig{Format: "workbook.project", Version: 1, ProjectID: "01K0M6B8A4FTT8C39MXXYTW7D0", Key: "WB"}
}

func startWatcher(t *testing.T, syncer *fakeSyncer, adjust func(*Options)) (string, *syncBuffer) {
	t.Helper()
	directory := t.TempDir()
	output := &syncBuffer{}
	options := Options{
		CommonGitDir: directory,
		Repository:   syncer,
		Config:       watcherConfig(),
		Stderr:       output,
	}
	if adjust != nil {
		adjust(&options)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- Run(ctx, options) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("watcher did not stop")
		}
	})
	return directory, output
}

func nudge(t *testing.T, directory string) {
	t.Helper()
	client, err := Dial(directory, probeDeadline)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer client.Close()
	if err := client.Nudge(""); err != nil {
		t.Fatalf("Nudge() error = %v", err)
	}
}

func readStatus(t *testing.T, directory string) Status {
	t.Helper()
	client, err := Dial(directory, probeDeadline)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer client.Close()
	status, err := client.Status()
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	return status
}

func waitForOutput(t *testing.T, output *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), want) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("watcher never wrote %q; wrote %q", want, output.String())
}

func waitForSyncs(t *testing.T, syncer *fakeSyncer, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syncer.syncCount() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("watcher performed %d syncs, want at least %d", syncer.syncCount(), want)
}

type syncBuffer struct {
	mu       sync.Mutex
	contents bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.contents.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.contents.String()
}

type fakeSyncer struct {
	mu             sync.Mutex
	syncs          int
	prunes         int
	origin         bool
	block          chan struct{}
	heads          map[string]string
	conflicts      []core.Conflict
	err            error
	lastContextErr error
}

func (f *fakeSyncer) Sync(ctx context.Context, _ core.ProjectConfig) (gitstore.SyncRunResult, error) {
	f.mu.Lock()
	f.syncs++
	f.lastContextErr = ctx.Err()
	blocker := f.block
	conflicts := append([]core.Conflict{}, f.conflicts...)
	err := f.err
	f.mu.Unlock()

	if blocker != nil {
		<-blocker
	}

	result := gitstore.SyncRunResult{
		Remote: "origin",
		Fetch:  gitstore.SyncResult{Remote: "origin", Status: gitstore.SyncPhaseCompleted, Conflicts: conflicts},
		Push:   gitstore.SyncResult{Remote: "origin", Status: gitstore.SyncPhaseCompleted},
	}
	if len(conflicts) > 0 && err == nil {
		err = core.ConflictError(conflicts)
	}
	return result, err
}

func (f *fakeSyncer) PruneParkedRefs(context.Context, core.ProjectConfig) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prunes++
	return 0, nil
}

func (f *fakeSyncer) InspectTaskHead(_ context.Context, _ core.ProjectConfig, taskID string) (gitstore.TaskHead, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	head, found := f.heads[taskID]
	if !found {
		return gitstore.TaskHead{}, false, nil
	}
	return gitstore.TaskHead{TaskID: taskID, ObjectID: head}, true, nil
}

func (f *fakeSyncer) HasOrigin(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.origin
}

func (f *fakeSyncer) syncCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.syncs
}

func (f *fakeSyncer) setConflicts(conflicts []core.Conflict) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conflicts = conflicts
}

func (f *fakeSyncer) setHead(taskID, head string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads[taskID] = head
}
