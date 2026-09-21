package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/dgoings/workbook/internal/autosync"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/projection"
	"github.com/dgoings/workbook/internal/syncloop"
	"github.com/dgoings/workbook/internal/userconfig"
)

const (
	syncStatusCompleted = "completed"
	syncStatusSkipped   = "skipped"
	syncStatusFailed    = "failed"
	// syncStatusDeferred reports that a running watcher was handed this change
	// instead of the command synchronizing inline. It is best-effort rather
	// than a guarantee: the local write is durable, and publication follows
	// within milliseconds, but a watcher killed in that window leaves the work
	// local until `workbook push` or the next watcher runs.
	syncStatusDeferred = "deferred"
)

// syncReport is the machine-readable account of what a command did about
// synchronization. It is reported even when nothing was attempted so a caller
// can tell a deliberate skip from a silent one.
type syncReport struct {
	Enabled bool                     `json:"enabled"`
	Source  autosync.Source          `json:"source"`
	Status  string                   `json:"status"`
	Detail  string                   `json:"detail,omitempty"`
	Fetch   *gitstore.SyncResult     `json:"fetch,omitempty"`
	Push    *gitstore.SyncTaskResult `json:"push,omitempty"`
	// Identity reports what publishing this change had to establish about
	// origin's project, and is omitted when there was nothing to establish. A
	// mutation is the most common way a project publishes anything, so it is
	// also where a remote that refuses the identity ref has to be reported.
	Identity *gitstore.SyncIdentityResult `json:"identity,omitempty"`
	// Config reports what this command did about the project's configuration
	// ledger, on the same terms. A mutation publishes the vocabulary its own
	// status was written against, so a remote that refuses the ledger while
	// accepting the task ref is reported here.
	Config *gitstore.SyncConfigResult `json:"config,omitempty"`
	// configConflicts travels with the report but is not part of it: the
	// envelope carries the list, exactly as it carries the task conflict list,
	// so one command reports one list of each kind.
	configConflicts []core.ConfigConflict
}

// taskSession carries the repository, service, and resolved synchronization
// policy through one command so the fetch, the mutation, and the targeted push
// share a single opened repository.
type taskSession struct {
	repository *gitstore.Repository
	config     core.ProjectConfig
	service    core.Service
	report     syncReport
	fetched    *gitstore.SyncResult
	conflicts  []core.Conflict
	// defects carries what a fetch that still ran to completion reported about
	// refs it could not validate. Publication follows such a fetch, so this is
	// a warning the caller hears alongside a synchronized change rather than
	// the failure that used to stand in for it.
	defects string
	// deferred reports that a trustworthy watcher answered, so this command
	// writes locally and hands publication to it.
	deferred bool
	watcher  syncloop.Status
}

func openTaskSession(ctx context.Context, cwd string, noSync, withWriter bool, stderr io.Writer) (*taskSession, error) {
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return nil, err
	}
	user, err := userconfig.Load()
	if err != nil {
		return nil, err
	}
	policy, err := autosync.Resolve(noSync, config, user)
	if err != nil {
		return nil, err
	}

	store, err := projection.Open(ctx, repository, config)
	if err != nil {
		return nil, err
	}
	// The session opens on the configuration this clone currently holds, and
	// refreshes it after the fetch; see refreshConfiguration.
	//
	// Both sections, from one read. The statuses alone were enough only while
	// nothing could configure a priority: a Service that left Priorities at the
	// zero value substitutes the built-in three, which is what an unconfigured
	// project has, so the omission was invisible. It stops being invisible the
	// moment a project puts its own priority on file — `create --priority` would
	// refuse it, `next` would rank against the wrong vocabulary, and a status
	// change would regenerate guidelines that document priorities this project
	// does not use.
	state, err := repository.LoadVocabularyState(ctx, config)
	if err != nil {
		return nil, err
	}
	service := core.Service{
		Config:     config,
		Vocabulary: state.Vocabulary,
		Priorities: state.Priorities,
		// And the keys, from the same read. Without them every Service in this
		// package falls back to the founding key, so `create` on a project that
		// has moved its current key would mint under the key the identity was
		// written with rather than under the one the ledger names.
		Keys:   state.Keys,
		Reader: store,
		IDs:    core.CryptoULIDSource{},
		Now:    time.Now,
	}
	if withWriter {
		actor, err := repository.Actor(ctx)
		if err != nil {
			return nil, err
		}
		service.Writer = repository
		service.Blobs = repository
		service.Projection = store
		service.Actor = actor
	}

	session := &taskSession{
		repository: repository,
		config:     config,
		service:    service,
		report:     syncReport{Enabled: policy.Enabled, Source: policy.Source, Status: syncStatusSkipped},
	}
	if !policy.Enabled {
		session.report.Detail = "automatic synchronization is disabled"
		return session, nil
	}
	session.probeWatcher()
	return session, nil
}

// probeWatcher asks a running watcher whether it can be trusted with this
// command's synchronization.
//
// Every failure is silent and lands on the inline path, because no watcher is
// the ordinary case. With none running the whole probe is one os.ReadFile
// returning ENOENT, which is what keeps an unwatched repository's behavior
// indistinguishable from before watchers existed.
func (session *taskSession) probeWatcher() {
	client, err := syncloop.Dial(session.repository.CommonGitDir, watcherProbeDeadline)
	if err != nil {
		return
	}
	defer client.Close()
	status, err := client.Status()
	if err != nil || !status.Trustworthy(time.Now()) {
		return
	}
	session.deferred = true
	session.watcher = status
	// A watcher's conflicts are this command's conflicts. Carrying the whole
	// set rather than only the target's matches the inline path, where a
	// fetch's entire conflict list is reported.
	for _, entry := range status.Conflicts {
		session.conflicts = append(session.conflicts, entry.Conflict)
	}
}

// nudge asks the watcher to publish, waiting for receipt rather than for
// completion.
func (session *taskSession) nudge(taskID string) error {
	client, err := syncloop.Dial(session.repository.CommonGitDir, watcherProbeDeadline)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Nudge(taskID)
}

// acknowledge tells the watcher a conflict reached a caller, so the identical
// retry proceeds instead of meeting the same gate forever.
func (session *taskSession) acknowledge(conflict core.Conflict) {
	if !session.deferred {
		return
	}
	client, err := syncloop.Dial(session.repository.CommonGitDir, watcherProbeDeadline)
	if err != nil {
		return
	}
	defer client.Close()
	_ = client.Acknowledge(conflict.TaskID)
}

// fetchBefore refreshes shared task refs so the mutation that follows applies
// to origin's current tip. A teammate's advance is absorbed as a fast-forward,
// and local work origin does not have is replayed onto the fetched tip.
//
// An unreachable origin is a warning rather than a failure: the local write is
// the durable result, and refusing to record work because the network is down
// would defeat the local-first design. A conflict is different — the fetch
// itself completed and advanced refs, so it is recorded and left for the caller
// to act on.
//
// The same holds for any fetch that ran to completion while reporting per-task
// failures, which is why the gate is the phase's status rather than the error's
// category. Fetch isolates an invalid tip to its own task and advances every
// other ref, so one task's malformed object is no reason to deny publication to
// a mutation that did not touch it. What the fetch could not validate is
// carried as a warning instead.
func (session *taskSession) fetchBefore(ctx context.Context) {
	if !session.report.Enabled {
		return
	}
	if session.deferred {
		// The watcher fetched within its staleness window, so the tip this
		// mutation applies to is current enough. Reconciliation makes a
		// slightly stale tip a case the fetch path already handles rather than
		// one worth a network round trip to prevent.
		session.report.Status = syncStatusDeferred
		session.report.Detail = fmt.Sprintf("handed to the sync watcher (pid %d)", session.watcher.PID)
		return
	}
	if !session.repository.HasOrigin(ctx) {
		session.report.Status = syncStatusSkipped
		session.report.Detail = "no origin remote configured"
		return
	}
	result, err := session.repository.Fetch(ctx, session.config)
	session.report.Fetch = &result
	session.report.Config = result.Config
	// A configuration conflict never blocks the mutation. The two clones
	// disagree about a status, not about this task's history, and the mutation
	// is validated against the vocabulary the fetch did settle on — so it is
	// carried to the caller rather than raised in their way.
	session.report.configConflicts = result.ConfigConflicts
	session.conflicts = result.Conflicts
	if err != nil && result.Status != gitstore.SyncPhaseCompleted {
		session.report.Status = syncStatusFailed
		session.report.Detail = "fetch failed: " + err.Error()
		return
	}
	session.fetched = &result
	session.report.Status = syncStatusCompleted
	if err != nil && core.CategoryOf(err) != core.CategoryConflict {
		session.defects = err.Error()
	}
}

// publish sends exactly the ref the mutation changed.
//
// It is skipped when the fetch did not complete, because a second connection to
// an unreachable origin only buys a second timeout. A rejection is reported as
// a warning: the change is durable locally, and the next fetch replays it onto
// whatever origin holds by then.
func (session *taskSession) publish(ctx context.Context, taskID string) {
	if !session.report.Enabled {
		return
	}
	if session.deferred {
		if err := session.nudge(taskID); err == nil {
			return
		}
		// The watcher answered the probe and was gone by the time the write
		// landed. Publishing here is what keeps "deferred" from being a promise
		// this command has no basis to make.
		session.deferred = false
		session.report.Status = syncStatusCompleted
		session.report.Detail = ""
		session.pushInline(ctx, taskID)
		return
	}
	if session.fetched == nil {
		return
	}
	session.pushInline(ctx, taskID)
}

// publishConfig sends the configuration ledger a status change just moved.
//
// It is publish for the change that has no task ref, and the difference is only
// in what goes out: the same watcher deferral, the same skip when the fetch did
// not complete, and the same treatment of a rejection as a warning rather than
// a failure — the ledger is durable locally, and the next fetch replays it onto
// whatever origin holds by then.
//
// The nudge carries no task ID because there is no task. A watcher's nudge only
// wakes its loop, which synchronizes everything this clone holds, so the ledger
// rides that just as a task would.
func (session *taskSession) publishConfig(ctx context.Context) {
	if !session.report.Enabled {
		return
	}
	if session.deferred {
		if err := session.nudge(""); err == nil {
			return
		}
		// The watcher answered the probe and was gone by the time the write
		// landed, exactly as on the task path; publishing here is what keeps
		// "deferred" from being a promise this command has no basis to make.
		session.deferred = false
		session.report.Status = syncStatusCompleted
		session.report.Detail = ""
		session.pushConfigInline(ctx)
		return
	}
	if session.fetched == nil {
		return
	}
	session.pushConfigInline(ctx)
}

func (session *taskSession) pushConfigInline(ctx context.Context) {
	published, err := session.repository.PushConfig(ctx, session.config)
	session.report.Identity, _ = session.repository.IdentityReport()
	if published != nil {
		session.report.Config = published
	}
	if err != nil {
		session.report.Status = syncStatusFailed
		session.report.Detail = "push failed: " + err.Error()
		return
	}
	session.report.Status = syncStatusCompleted
}

func (session *taskSession) pushInline(ctx context.Context, taskID string) {
	pushed, err := session.repository.PushTask(ctx, session.config, taskID)
	session.report.Push = &pushed
	session.report.Identity, _ = session.repository.IdentityReport()
	if published, found := session.repository.ConfigReport(); found {
		session.report.Config = published
	}
	if err != nil {
		session.report.Status = syncStatusFailed
		session.report.Detail = "push failed: " + err.Error()
		return
	}
	session.report.Status = syncStatusCompleted
}

// refreshConfiguration re-reads the project's statuses, priorities and keys
// after the fetch that may have changed them, from one read of one tip.
//
// A mutation must be validated against the configuration this command ends up
// writing into, not the one it started with. A teammate who renamed `ready` to
// `todo` an hour ago means that `--status todo` typed here should be accepted
// and that a task still stored under `ready` should be settled on this write;
// the same is true of a teammate who added `urgent`. Both are properties of the
// fetched configuration, and the fetch happens after the session was opened.
//
// It refreshes the keys from the same read, so a `create --key` typed against a
// teammate's newly added key is accepted rather than refused by the set this
// command opened with — and so a `key retire` lands on the set the fetch settled
// on rather than on one that has since grown a key.
//
// It is one refresher for every section rather than one per section, and that
// is the point. Reading the statuses and the priorities separately would let a
// fetch land between them and render a project out of two configurations, which
// is what gitstore.VocabularyState exists to prevent — and a task mutation that
// refreshed only the statuses would author against priorities the fetch had
// already superseded, which is the same bug in the half nobody was looking at.
//
// It costs one ref enumeration: LoadVocabularyState skips the repository's
// vocabulary memo on purpose, because that memo records no head, and memoizes
// the decode instead, keyed on the tip it decoded. A command that reaches here
// has just run a fetch, so one local `for-each-ref` beside a network round trip
// is not the expensive part of this path.
func (session *taskSession) refreshConfiguration(ctx context.Context) error {
	state, err := session.repository.LoadVocabularyState(ctx, session.config)
	if err != nil {
		return err
	}
	session.service.Vocabulary = state.Vocabulary
	session.service.Priorities = state.Priorities
	session.service.Keys = state.Keys
	return nil
}

// conflictFor reports whether this command's own task is one the fetch could
// not finish replaying. An unrelated task's conflict never blocks a mutation;
// this task's does, because the mutation would otherwise build on a history
// that silently dropped the caller's earlier local operations.
func (session *taskSession) conflictFor(ctx context.Context, target string) *core.Conflict {
	if len(session.conflicts) == 0 || target == "" {
		return nil
	}
	taskID := target
	if !session.service.KeySet().Owns(taskID) {
		resolved, err := session.service.Reader.Resolve(ctx, session.config, target)
		if err != nil {
			return nil
		}
		taskID = resolved
	}
	for index := range session.conflicts {
		if session.conflicts[index].TaskID == taskID {
			return &session.conflicts[index]
		}
	}
	return nil
}

// mutate runs the fetch, the mutation, and the targeted push in order. The
// mutation's result names the canonical task ID, which is what makes one
// sequence work for create as well as for the commands that take an ID.
//
// target is the id-or-prefix the command was given, or empty when it creates a
// task. It is checked before the mutation runs so a conflicted task is left
// exactly where the fetch put it and the caller can retry this same command.
func (session *taskSession) mutate(
	ctx context.Context,
	target string,
	apply func(context.Context) (core.MutationResult, error),
) (core.MutationResult, error) {
	session.fetchBefore(ctx)
	if err := session.refreshConfiguration(ctx); err != nil {
		return core.MutationResult{}, err
	}
	if conflict := session.conflictFor(ctx, target); conflict != nil {
		session.acknowledge(*conflict)
		return core.MutationResult{}, core.ConflictError([]core.Conflict{*conflict})
	}
	result, err := apply(ctx)
	if err != nil {
		return core.MutationResult{}, err
	}
	session.publish(ctx, result.Task.ID)
	switch {
	case session.report.Status == syncStatusFailed:
		result.Warnings = append(result.Warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the change was recorded locally, but " + session.report.Detail,
		})
	case session.defects != "":
		result.Warnings = append(result.Warnings, core.Warning{
			Code: core.WarningAutoSync,
			Message: "the change synchronized, but origin holds task refs this fetch could not " +
				"validate: " + session.defects,
		})
	}
	return result, nil
}
