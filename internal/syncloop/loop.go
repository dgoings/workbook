// Package syncloop keeps one clone's task refs and projection current in the
// background, so a mutation can record its work locally and hand publication
// to a process that is already connected.
//
// The loop is an optimization and never a requirement. Every command works
// unchanged with no watcher running, and a command that cannot reach one falls
// back to synchronizing inline.
package syncloop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

const (
	// DefaultInterval spaces scheduled synchronizations.
	DefaultInterval = 5 * time.Second
	// DefaultQuiet is how long a nudge waits before synchronizing, so a burst
	// of mutations collapses into one round trip instead of one each.
	DefaultQuiet = 250 * time.Millisecond
	// DefaultShutdown bounds the final synchronization.
	DefaultShutdown = 5 * time.Second

	// ReadyPrefix opens the line a watcher prints once it is listening. Tests
	// and the performance fixture wait for it rather than for a duration.
	ReadyPrefix = "Workbook sync watcher:"
)

// ErrWatcherLive reports that another process already answers for this
// repository. It is not a failure for a caller that merely wanted one running.
var ErrWatcherLive = errors.New("a Workbook watcher already owns this repository")

// Syncer is the Git work a watcher performs, kept narrow so the loop is
// testable without a repository.
type Syncer interface {
	Sync(context.Context, core.ProjectConfig) (gitstore.SyncRunResult, error)
	PruneParkedRefs(context.Context, core.ProjectConfig) (int, error)
	InspectTaskHead(context.Context, core.ProjectConfig, string) (gitstore.TaskHead, bool, error)
	HasOrigin(context.Context) bool
}

// Refresher warms the read projection after refs move.
type Refresher interface {
	Refresh(context.Context) error
}

// Options configures one watcher.
type Options struct {
	CommonGitDir string
	Repository   Syncer
	Config       core.ProjectConfig
	// Projection is warmed after a synchronization that changed refs. A nil
	// Projection disables that step.
	Projection Refresher
	Interval   time.Duration
	Quiet      time.Duration
	Shutdown   time.Duration
	// Stderr carries the readiness line and conflict reports. A watcher is a
	// foreground process, and its terminal is the only channel by which a
	// person learns about a conflict nobody was present for.
	Stderr io.Writer
	Now    func() time.Time
}

// Run binds this repository's watcher socket, publishes the pointer file that
// tells commands where to find it, and synchronizes until ctx is done. It then
// runs one final synchronization on a fresh context so an ordinary Ctrl-C or
// kill never strands work the watcher was still holding.
//
// It returns ErrWatcherLive when another process already answers.
func Run(ctx context.Context, options Options) error {
	loop, err := newLoop(options)
	if err != nil {
		return err
	}
	return loop.run(ctx)
}

type loop struct {
	options   Options
	conflicts *conflictSet
	snapshot  atomic.Pointer[Status]
	nudges    chan struct{}
	started   time.Time
}

func newLoop(options Options) (*loop, error) {
	if options.CommonGitDir == "" {
		return nil, core.Errorf(core.CategoryOperational, "watcher repository directory is required")
	}
	if options.Repository == nil {
		return nil, core.Errorf(core.CategoryOperational, "watcher repository is required")
	}
	if options.Interval <= 0 {
		options.Interval = DefaultInterval
	}
	if options.Quiet <= 0 {
		options.Quiet = DefaultQuiet
	}
	if options.Shutdown <= 0 {
		options.Shutdown = DefaultShutdown
	}
	if options.Stderr == nil {
		options.Stderr = io.Discard
	}
	if options.Now == nil {
		options.Now = time.Now
	}

	created := &loop{
		options:   options,
		conflicts: newConflictSet(),
		nudges:    make(chan struct{}, 1),
		started:   options.Now(),
	}
	// A status must exist before the first request arrives. Until the opening
	// synchronization lands it reports not-yet-synchronized, which reads as
	// untrustworthy and sends commands down the inline path.
	created.snapshot.Store(&Status{
		Format:     StatusFormat,
		Version:    StatusVersion,
		PID:        os.Getpid(),
		IntervalMS: options.Interval.Milliseconds(),
		StartedAt:  created.started,
	})
	return created, nil
}

func (l *loop) run(ctx context.Context) error {
	listener, path, err := bind(l.options.CommonGitDir)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(path)
		removePointer(l.options.CommonGitDir, path)
	}()

	published := pointer{Format: PointerFormat, Version: PointerVersion, Socket: path, PID: os.Getpid()}
	if err := writePointer(l.options.CommonGitDir, published); err != nil {
		return err
	}

	attendant := &server{listener: listener, snapshot: &l.snapshot, conflicts: l.conflicts, nudges: l.nudges}
	go attendant.serve(ctx)

	fmt.Fprintf(l.options.Stderr, "%s %s interval, socket %s\n", ReadyPrefix, l.options.Interval, path)

	// Synchronize once immediately. Without it the status reports no successful
	// synchronization for up to a full interval, and every mutation in that
	// window would correctly refuse to defer.
	l.syncOnce(ctx)
	l.wait(ctx)
	l.finalSync()
	return nil
}

// wait runs scheduled and nudged synchronizations until the context is done.
//
// A nudge arms a fixed quiet window rather than restarting it, so a burst
// collapses into exactly one synchronization at a bounded delay; a stream of
// nudges cannot postpone publication indefinitely. A scheduled tick is skipped
// while a nudged synchronization is already pending, so the timer does not
// follow a burst with a second redundant round trip.
func (l *loop) wait(ctx context.Context) {
	ticker := time.NewTicker(l.options.Interval)
	defer ticker.Stop()

	var quiet *time.Timer
	var pending <-chan time.Time
	defer func() {
		if quiet != nil {
			quiet.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-l.nudges:
			if quiet == nil {
				quiet = time.NewTimer(l.options.Quiet)
				pending = quiet.C
			}
		case <-pending:
			quiet, pending = nil, nil
			l.syncOnce(ctx)
		case <-ticker.C:
			if pending != nil {
				continue
			}
			l.syncOnce(ctx)
		}
	}
}

func (l *loop) finalSync() {
	ctx, cancel := context.WithTimeout(context.Background(), l.options.Shutdown)
	defer cancel()
	l.syncOnce(ctx)
}

func (l *loop) syncOnce(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if !l.options.Repository.HasOrigin(ctx) {
		// Nothing to synchronize with. This is the ordinary state of a clone
		// with no remote, so it reports success and stays silent rather than
		// printing an error every tick.
		l.publish(true, "", nil)
		return
	}

	result, err := l.options.Repository.Sync(ctx, l.options.Config)
	conflicts := append(append([]core.Conflict{}, result.Fetch.Conflicts...), result.Push.Conflicts...)
	l.recordConflicts(ctx, conflicts)
	// The fetch phase is the whole report: it lists origin's namespace, and the
	// push phase that follows it repeats none of that. It is bounded before it
	// is announced or published so the terminal and the status always agree, and
	// so a poisoned origin cannot grow a status document past what a client will
	// read.
	ignored := boundIgnoredRefs(result.Fetch.Ignored)
	l.announceIgnored(result.Remote, ignored)

	// A conflict is not a failed synchronization. The fetch completed, refs
	// advanced, and everything that replayed cleanly was published; one task
	// needs a decision.
	failure := err
	if core.CategoryOf(err) == core.CategoryConflict {
		failure = nil
	}

	if _, pruneErr := l.options.Repository.PruneParkedRefs(ctx, l.options.Config); pruneErr != nil && failure == nil {
		failure = pruneErr
	}
	l.expireConflicts(ctx)

	if l.options.Projection != nil && changedRefs(result) {
		if refreshErr := l.options.Projection.Refresh(ctx); refreshErr != nil && failure == nil {
			failure = refreshErr
		}
	}

	if failure != nil {
		l.publish(false, failure.Error(), ignored)
		return
	}
	l.publish(true, "", ignored)
}

// recordConflicts remembers each conflict against the task's current tip and
// announces the new ones. Announcing is not decoration: a stopped replay leaves
// the ref truncated, so the next fetch finds nothing divergent and this is the
// only moment the conflict is observable to a person.
func (l *loop) recordConflicts(ctx context.Context, conflicts []core.Conflict) {
	for _, conflict := range conflicts {
		entry := ConflictEntry{Conflict: conflict}
		if head, found, err := l.options.Repository.InspectTaskHead(ctx, l.options.Config, conflict.TaskID); err == nil && found {
			entry.Head = head.ObjectID
		}
		if l.conflicts.add(entry) {
			fmt.Fprintf(l.options.Stderr, "workbook: conflict on %s: %s\n", conflict.TaskID, core.ConflictDetail(conflict))
		}
	}
}

// announceIgnored names each newly skipped ref on the watcher's terminal, for
// the reason a conflict is announced: the watcher synchronizes with nobody
// present, and its terminal is the only channel by which a person learns that
// origin holds a name this build does not read.
//
// The previously published snapshot is the memory, so a ref is announced once
// while origin keeps it and again if it returns after being removed. The line
// names the ref and why it was skipped and stops there. Such a name may be a
// newer version's or a second project's real history, so this is a report and
// never an instruction to remove anything; the full verdict and the one command
// Workbook is willing to suggest belong to `sync --status` and the foreground
// fetch, where a person is present to weigh them.
func (l *loop) announceIgnored(remote string, ignored []gitstore.IgnoredRef) {
	if len(ignored) == 0 {
		return
	}
	announced := make(map[string]struct{})
	for _, entry := range l.snapshot.Load().IgnoredRefs {
		announced[entry.Ref] = struct{}{}
	}
	for _, entry := range ignored {
		if _, found := announced[entry.Ref]; found {
			continue
		}
		fmt.Fprintf(l.options.Stderr, "workbook: ignored ref %s on %s: %s\n", entry.Ref, remote, entry.Reason)
	}
}

func (l *loop) expireConflicts(ctx context.Context) {
	recorded := l.conflicts.heads()
	if len(recorded) == 0 {
		return
	}
	moved := make([]string, 0, len(recorded))
	for taskID, head := range recorded {
		current, found, err := l.options.Repository.InspectTaskHead(ctx, l.options.Config, taskID)
		if err != nil {
			continue
		}
		if !found || current.ObjectID != head {
			moved = append(moved, taskID)
		}
	}
	l.conflicts.expire(moved)
}

func (l *loop) publish(ok bool, message string, ignored []gitstore.IgnoredRef) {
	l.snapshot.Store(&Status{
		Format:      StatusFormat,
		Version:     StatusVersion,
		PID:         os.Getpid(),
		IntervalMS:  l.options.Interval.Milliseconds(),
		StartedAt:   l.started,
		LastSyncAt:  l.options.Now(),
		LastSyncOK:  ok,
		LastError:   message,
		IgnoredRefs: ignored,
	})
}

// changedRefs reports whether the synchronization moved anything, which gates
// the projection refresh. Refresh is about to scale with operation count rather
// than task count, so running it on a quiet tick would buy nothing and cost
// increasingly more.
func changedRefs(result gitstore.SyncRunResult) bool {
	for _, task := range result.Fetch.Tasks {
		if task.Status != gitstore.SyncUnchanged {
			return true
		}
	}
	for _, task := range result.Push.Tasks {
		if task.Status != gitstore.SyncUpToDate {
			return true
		}
	}
	return configRefChanged(result.Config)
}

// configRefChanged reports whether the configuration stage moved the local
// ledger ref. Only the four outcomes that write it count: local-ahead and
// published say origin moved or did not, unchanged says nothing did, and
// invalid says the stage declined to touch anything.
func configRefChanged(result *gitstore.SyncConfigResult) bool {
	if result == nil {
		return false
	}
	switch result.Status {
	case gitstore.SyncConfigCreated,
		gitstore.SyncConfigFastForwarded,
		gitstore.SyncConfigReconciled,
		gitstore.SyncConfigConflicted:
		return true
	default:
		return false
	}
}
