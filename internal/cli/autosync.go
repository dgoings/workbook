package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
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
	// PushRelic reports the publication of the one relic a relic command
	// changed, as Push reports a task's.
	PushRelic *gitstore.SyncRelicResult `json:"pushRelic,omitempty"`
	// Pushed reports a publication of several tasks at once, which only
	// compact makes: it publishes every checkpoint the run wrote in one push.
	// A single-task mutation reports through Push instead.
	Pushed *gitstore.SyncResult `json:"pushed,omitempty"`
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
	// relicConflicts and relicWarnings travel the same way: the envelope
	// carries them, so a task command reports the relic conflicts and warnings
	// its fetch met exactly as a relic command does.
	relicConflicts []core.RelicConflict
	relicWarnings  []core.Warning
}

// taskSession carries the repository, service, and resolved synchronization
// policy through one command so the fetch, the mutation, and the targeted push
// share a single opened repository.
type taskSession struct {
	repository *gitstore.Repository
	config     core.ProjectConfig
	service    core.Service
	// store is the projection the service reads and writes through, for a
	// command such as compact that has to move it directly.
	store     *projection.Store
	report    syncReport
	fetched   *gitstore.SyncResult
	conflicts []core.Conflict
	// relicConflicts and relicWarnings are what the fetch reported about
	// relics, the conflicts with every body version's content filled in. A
	// relic command stops on its own relic's conflict, as mutate stops on its
	// task's.
	relicConflicts []core.RelicConflict
	relicWarnings  []core.Warning
	// defects carries what a fetch that still ran to completion reported about
	// refs it could not validate. Publication follows such a fetch, so this is
	// a warning the caller hears alongside a synchronized change rather than
	// the failure that used to stand in for it.
	defects string
	// relicDefectsOnly reports that every defect the fetch found was a
	// relic's, so the warning about them says relics rather than task refs.
	relicDefectsOnly bool
	// compactedOnOrigin names the tasks the fetch kept on the old history
	// because origin compacted them and their local changes would not replay.
	// That is each task's own state rather than a defect of the fetch, so it
	// is said in its own words.
	compactedOnOrigin []string
	// unsettled is why the projection could not take a task the fetch moved
	// onto another history, which leaves a cache to rebuild before Git prunes
	// the history it last read. See settleMovedHistories.
	unsettled error
	// deferred reports that a trustworthy watcher answered, so this command
	// writes locally and hands publication to it.
	deferred bool
	// refusal is a publication refusal the command fails on; see noteRefusal.
	refusal error
	watcher syncloop.Status
	// releaseProjection closes the projection the session reads and writes
	// through; see openProjection for why it has to be given back.
	releaseProjection func()
}

// Close releases what the session opened. Every command that opens a session
// defers it, so the projection handle lives exactly as long as the command.
//
// It answers nothing rather than an error, unlike the Close it stands in
// front of: this runs as a command returns, after the command has written its
// result, and there is nothing a caller could do about a cache that would not
// close.
//
// A session with no release is closed silently rather than by panicking. Only
// openTaskSession builds one today and it always sets the field, but Close runs
// from a defer on every path out of a command, which is the worst possible
// place to discover that a future constructor left it unset.
func (session *taskSession) Close() {
	if session.releaseProjection == nil {
		return
	}
	session.releaseProjection()
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
	var actor string
	if withWriter {
		actor, err = repository.Actor(ctx)
		if err != nil {
			return nil, err
		}
	}
	// Opened last, after everything else that can fail, so no error path
	// between here and the returned session leaves a projection nobody holds
	// the release for. Nothing above it needs the cache.
	store, releaseProjection, err := openProjection(ctx, repository, config)
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
		IDs:    core.CryptoULIDSource{Now: recordedNow},
		Now:    sessionNow,
	}
	if withWriter {
		service.Writer = repository
		service.Blobs = repository
		service.Projection = store
		service.Actor = actor
	}

	session := &taskSession{
		repository:        repository,
		config:            config,
		service:           service,
		store:             store,
		report:            syncReport{Enabled: policy.Enabled, Source: policy.Source, Status: syncStatusSkipped},
		releaseProjection: releaseProjection,
	}
	if !policy.Enabled {
		session.report.Detail = "automatic synchronization is disabled"
		return session, nil
	}
	session.probeWatcher(ctx)
	return session, nil
}

// probeWatcher asks a running watcher whether it can be trusted with this
// command's synchronization.
//
// Every failure is silent and lands on the inline path, because no watcher is
// the ordinary case. With none running the whole probe is one os.ReadFile
// returning ENOENT, which is what keeps an unwatched repository's behavior
// indistinguishable from before watchers existed.
func (session *taskSession) probeWatcher(ctx context.Context) {
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
	// Its relic conflicts too, with each body read from this clone as an
	// inline fetch's are: the watcher keeps no body text. Its relic warnings
	// are not carried; they were said on the watcher's terminal.
	if len(status.RelicConflicts) > 0 {
		var fetched gitstore.SyncResult
		for _, entry := range status.RelicConflicts {
			fetched.RelicConflicts = append(fetched.RelicConflicts, entry.RelicConflict)
		}
		session.relicConflicts, session.relicWarnings = relicReport(ctx, session.repository, fetched)
		session.report.relicConflicts = session.relicConflicts
		session.report.relicWarnings = session.relicWarnings
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

// acknowledgeRelic is acknowledge for a relic's conflict.
func (session *taskSession) acknowledgeRelic(conflict core.RelicConflict) {
	if !session.deferred {
		return
	}
	client, err := syncloop.Dial(session.repository.CommonGitDir, watcherProbeDeadline)
	if err != nil {
		return
	}
	defer client.Close()
	_ = client.AcknowledgeRelic(conflict.RelicID)
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
//
// It returns an error only for a tasks remote marked as moved: the edit would
// otherwise be recorded here and published to the place the tasks left, or to
// nowhere, so the command stops with the refusal saying where they went.
func (session *taskSession) fetchBefore(ctx context.Context) error {
	if !session.report.Enabled {
		return nil
	}
	if session.deferred {
		// The watcher fetched within its staleness window, so the tip this
		// mutation applies to is current enough. Reconciliation makes a
		// slightly stale tip a case the fetch path already handles rather than
		// one worth a network round trip to prevent.
		//
		// A tasks remote marked as the place the tasks moved away from is
		// refused here too, from this checkout's copy of its identity ref,
		// which the watcher's own fetches keep current: a change recorded now
		// would be handed to a watcher that is refused, and this command would
		// exit as if it had been published. Nothing is asked of the remote, so
		// a move reaches this check within one watcher interval.
		if err := session.repository.CheckRemoteNotMoved(ctx, false); err != nil {
			return err
		}
		session.report.Status = syncStatusDeferred
		session.report.Detail = fmt.Sprintf("handed to the sync watcher (pid %d)", session.watcher.PID)
		return nil
	}
	if !session.repository.HasTasksRemote(ctx) {
		session.report.Status = syncStatusSkipped
		session.report.Detail = "no " + session.repository.TasksRemoteName() + " remote configured"
		return nil
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
	if refusal, found := core.TasksRemoteRefusalOf(err); found && refusal.Reason == core.TasksRemoteMoved {
		return err
	}
	if err != nil && result.Status != gitstore.SyncPhaseCompleted {
		session.report.Status = syncStatusFailed
		session.report.Detail = "fetch failed: " + err.Error()
		return nil
	}
	session.fetched = &result
	session.report.Status = syncStatusCompleted
	if err != nil && core.CategoryOf(err) != core.CategoryConflict {
		session.defects = err.Error()
		session.relicDefectsOnly = onlyRelicDefects(result)
	}
	session.noteRelicReport(ctx, result)
	for _, item := range result.Tasks {
		if item.Status == gitstore.SyncCompactedOnOrigin {
			session.compactedOnOrigin = append(session.compactedOnOrigin, item.TaskID)
		}
	}
	session.unsettled = settleMovedHistories(ctx, session.store, session.config, result)
	return nil
}

// settleMovedHistories reads, through the projection, every task a fetch moved
// onto another history (gitstore.SyncStatus.LeavesPreviousHistory), so the
// command that moved the ref also moves the cache before it returns.
//
// It has to happen then. The projection proves such a move against the head it
// last read, and nothing keeps that head alive once the ref leaves it: after
// `git gc` prunes it, the proof cannot run and every read fails until a
// rebuild. A fetch that moved no task this way reads nothing here, so a
// steady-state synchronization costs exactly what it did.
//
// Every command that fetches comes through here: the mutations, next and
// compact through their session's fetch, and sync, fetch and setup on their
// own. The watcher refreshes its own projection after each synchronization.
func settleMovedHistories(ctx context.Context, store *projection.Store, config core.ProjectConfig, fetched gitstore.SyncResult) error {
	for _, item := range fetched.Tasks {
		if !item.Status.LeavesPreviousHistory() {
			continue
		}
		if _, err := store.Get(ctx, config, item.TaskID); err != nil {
			return err
		}
	}
	return nil
}

// movesHistories reports whether settleMovedHistories has anything to read, so
// a caller holding no projection opens one only then.
func movesHistories(fetched gitstore.SyncResult) bool {
	for _, item := range fetched.Tasks {
		if item.Status.LeavesPreviousHistory() {
			return true
		}
	}
	return false
}

// settleFetchedHistories is settleMovedHistories for a command that holds no
// projection of its own: sync, fetch and setup. A failure is a warning, since
// the refs are right and the cache can be rebuilt from them.
func settleFetchedHistories(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	fetched gitstore.SyncResult,
) []core.Warning {
	if !movesHistories(fetched) {
		return nil
	}
	store, releaseProjection, err := openProjection(ctx, repository, config)
	if err == nil {
		defer releaseProjection()
		err = settleMovedHistories(ctx, store, config, fetched)
	}
	if err == nil {
		return nil
	}
	return []core.Warning{unsettledWarning(err)}
}

// unsettledWarning says that the projection could not take a task the fetch
// moved onto another history.
func unsettledWarning(err error) core.Warning {
	return core.Warning{
		Code: core.WarningProjectionUpdate,
		Message: "origin's compaction was adopted, but the SQLite cache could not be updated; run " +
			"`workbook rebuild` before `git gc`: " + err.Error(),
	}
}

// unsettledWarnings is unsettledWarning for a session's own fetch, or nothing
// when its projection took every task the fetch moved.
//
// It also carries a warning for each publication the remote accepted that
// could not be checked afterwards (gitstore.Repository.PublicationWarnings),
// since every command that publishes task refs reports through here.
func (session *taskSession) unsettledWarnings() []core.Warning {
	warnings := session.repository.PublicationWarnings()
	if session.unsettled == nil {
		return warnings
	}
	return append([]core.Warning{unsettledWarning(session.unsettled)}, warnings...)
}

// compactedOnOriginWarning says which tasks the fetch kept on the old history,
// after prefix, or reports false when it kept none.
func (session *taskSession) compactedOnOriginWarning(prefix string) (core.Warning, bool) {
	if len(session.compactedOnOrigin) == 0 {
		return core.Warning{}, false
	}
	return core.Warning{
		Code: core.WarningAutoSync,
		Message: prefix + session.repository.TasksRemoteName() + " compacted tasks this clone keeps on the old history (" +
			strings.Join(session.compactedOnOrigin, ", ") + `); run "workbook sync" for details`,
	}, true
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
	push := session.repository.PushConfig
	if session.fetched != nil {
		push = session.repository.PushConfigAfterFetch
	}
	published, err := push(ctx, session.config)
	session.report.Identity, _ = session.repository.IdentityReport()
	if published != nil {
		session.report.Config = published
	}
	if err != nil {
		session.report.Status = syncStatusFailed
		session.report.Detail = "push failed: " + err.Error()
		session.noteRefusal(err)
		return
	}
	session.report.Status = syncStatusCompleted
}

// publishTasks sends the refs one command changed across several tasks, as
// compact does for the checkpoints it wrote. It is publish for a set of tasks:
// a live watcher is nudged, and its own synchronization publishes them, leasing
// a checkpoint exactly as the inline push would; otherwise the push is inline,
// and skipped when the fetch did not complete.
func (session *taskSession) publishTasks(ctx context.Context, taskIDs []string) {
	if !session.report.Enabled || len(taskIDs) == 0 {
		return
	}
	if session.deferred {
		// The nudge carries no task ID, as publishConfig's does not: it only
		// wakes the watcher's loop, which synchronizes every task.
		if err := session.nudge(""); err == nil {
			return
		}
		// The watcher answered the probe and was gone by the time the refs
		// moved; publishing here keeps "deferred" from being an empty promise.
		session.deferred = false
		session.report.Status = syncStatusCompleted
		session.report.Detail = ""
		session.pushTasksInline(ctx, taskIDs)
		return
	}
	if session.fetched == nil {
		return
	}
	session.pushTasksInline(ctx, taskIDs)
}

func (session *taskSession) pushTasksInline(ctx context.Context, taskIDs []string) {
	pushed, err := session.repository.PushTasks(ctx, session.config, taskIDs)
	session.report.Pushed = &pushed
	session.report.Identity, _ = session.repository.IdentityReport()
	if pushed.Config != nil {
		session.report.Config = pushed.Config
	}
	if err != nil {
		session.report.Status = syncStatusFailed
		session.report.Detail = "push failed: " + err.Error()
		session.noteRefusal(err)
		return
	}
	session.report.Status = syncStatusCompleted
}

func (session *taskSession) pushInline(ctx context.Context, taskID string) {
	// This command's own fetch compared the identity refs moments ago, so the
	// push need not ask again. A command that handed off to a watcher which
	// then vanished did not fetch, and asks.
	push := session.repository.PushTask
	if session.fetched != nil {
		push = session.repository.PushTaskAfterFetch
	}
	pushed, err := push(ctx, session.config, taskID)
	session.report.Push = &pushed
	session.report.Identity, _ = session.repository.IdentityReport()
	if published, found := session.repository.ConfigReport(); found {
		session.report.Config = published
	}
	if err != nil {
		session.report.Status = syncStatusFailed
		session.report.Detail = "push failed: " + err.Error()
		session.noteRefusal(err)
		return
	}
	session.report.Status = syncStatusCompleted
}

// noteRefusal keeps a publication's refusal that the command has to fail on
// rather than carry as a warning: the tasks remote was marked as the place the
// tasks moved away from, either before the push (nothing was published) or
// just after it (the change reached a place nobody reads any more). The change
// is recorded either way, and both refusals say so; what they must not be is
// a warning beside an exit status that says all was well.
func (session *taskSession) noteRefusal(err error) {
	refusal, found := core.TasksRemoteRefusalOf(err)
	if !found {
		return
	}
	switch refusal.Reason {
	case core.TasksRemoteMoved, core.TasksRemotePublishedAfterMove:
		session.refusal = err
	}
}

// publicationRefusal is the refusal noteRefusal kept, if any. Every command
// that records a change and then publishes it returns this after writing
// nothing else, so its exit status and its JSON error envelope say what
// happened to the change.
func (session *taskSession) publicationRefusal() error {
	return session.refusal
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
	taskID, err := session.resolveTaskID(ctx, target)
	if err != nil {
		return nil
	}
	for index := range session.conflicts {
		if session.conflicts[index].TaskID == taskID {
			return &session.conflicts[index]
		}
	}
	return nil
}

// resolveTaskID turns the id-or-prefix a command was given into a task ID.
func (session *taskSession) resolveTaskID(ctx context.Context, target string) (string, error) {
	if session.service.KeySet().Owns(target) {
		return target, nil
	}
	return session.service.Reader.Resolve(ctx, session.config, target)
}

// refuseStuck refuses a command that would change a task its compaction left
// stuck, in the words fetch reports for that task. Changing it would only
// add history origin will not take, and for a task kept on the old history
// would add to the changes `compact --adopt` has to replay.
//
// It reads local refs rather than this command's fetch, so it holds with
// --no-sync and when a watcher did the fetching. A target that does not
// resolve is left for the command itself to report.
func (session *taskSession) refuseStuck(ctx context.Context, target string) error {
	return refuseStuckTask(ctx, session.repository, session.service, target)
}

// refuseStuckTask is refuseStuck for a caller that holds a service and a
// repository rather than a session: the board's mutations, which go through
// the service and the board's publisher rather than through mutate.
func refuseStuckTask(ctx context.Context, repository *gitstore.Repository, service core.Service, target string) error {
	if target == "" {
		return nil
	}
	taskID := target
	if !service.KeySet().Owns(target) {
		resolved, err := service.Reader.Resolve(ctx, service.Config, target)
		if err != nil {
			return nil
		}
		taskID = resolved
	}
	stuck, err := repository.StuckCompactions(ctx, service.Config, []string{taskID})
	if err != nil {
		return err
	}
	if entry, found := stuck[taskID]; found {
		return core.Errorf(entry.Category, "%s", entry.Detail)
	}
	return nil
}

// stuckTasks names every task a compaction left stuck, which next must not
// offer.
func (session *taskSession) stuckTasks(ctx context.Context) (map[string]struct{}, error) {
	stuck, err := session.repository.StuckCompactions(ctx, session.config, nil)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]struct{}, len(stuck))
	for taskID := range stuck {
		ids[taskID] = struct{}{}
	}
	return ids, nil
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
	if err := session.fetchBefore(ctx); err != nil {
		return core.MutationResult{}, err
	}
	if err := session.refreshConfiguration(ctx); err != nil {
		return core.MutationResult{}, err
	}
	// Ahead of the conflict check: a task this clone deleted on its
	// checkpoint also carries the fetch's tombstone conflict, and the stuck
	// state's own words are the ones that say how to get out of it.
	if err := session.refuseStuck(ctx, target); err != nil {
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
	if err := session.publicationRefusal(); err != nil {
		return core.MutationResult{}, err
	}
	result.Warnings = append(result.Warnings, session.mutationWarnings()...)
	return result, nil
}

// mutationWarnings is what a command that recorded and published one change
// says beside it about the synchronization around it.
func (session *taskSession) mutationWarnings() []core.Warning {
	var warnings []core.Warning
	switch {
	case session.report.Status == syncStatusFailed:
		warnings = append(warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the change was recorded locally, but " + session.report.Detail,
		})
	case session.defects != "" && session.relicDefectsOnly:
		warnings = append(warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the change synchronized, but origin holds relics this fetch could not check: " + session.defects,
		})
	case session.defects != "":
		warnings = append(warnings, core.Warning{
			Code: core.WarningAutoSync,
			Message: "the change synchronized, but origin holds task refs this fetch could not " +
				"validate: " + session.defects,
		})
	}
	if warning, found := session.compactedOnOriginWarning("the change synchronized, but "); found {
		warnings = append(warnings, warning)
	}
	return append(warnings, session.unsettledWarnings()...)
}
