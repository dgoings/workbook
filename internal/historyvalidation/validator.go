package historyvalidation

import (
	"context"
	"sort"
	"strings"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// Result reports the current semantic-validation state for every canonical task.
type Result struct {
	ValidatorVersion int  `json:"validatorVersion"`
	Full             bool `json:"full"`
	TaskCount        int  `json:"taskCount"`
	TasksChecked     int  `json:"tasksChecked"`
	CommitsChecked   int  `json:"commitsChecked"`
	CacheHits        int  `json:"cacheHits"`
	Valid            int  `json:"valid"`
	Invalid          int  `json:"invalid"`
	// NewerWriter counts tasks whose history carries a writer-format generation
	// this build cannot fold. They are deliberately not counted as invalid: the
	// audit could not check them, which is a different answer from checking
	// them and finding them wrong, and a caller that treats them as corruption
	// would go looking for damage that is not there.
	//
	// It is omitted when zero, so the envelope a healthy project emits is
	// byte-identical to the one it emitted before this member existed. Their
	// per-task entries still appear in Failures, carrying the newer-writer
	// category, because the commit and the message are what somebody needs.
	NewerWriter int       `json:"newerWriter,omitempty"`
	Pending     int       `json:"pending"`
	CachePath   string    `json:"cachePath"`
	Failures    []Failure `json:"failures"`
	// Config reports the configuration ledger's audit, and is omitted for a
	// project that has no ledger — which is every project until somebody
	// changes a status. A run against such a project therefore emits exactly
	// the JSON it emitted before this section existed.
	Config *ConfigValidation `json:"config,omitempty"`
	// Advisories carry what is true about the validated state without being
	// wrong with it. They never affect the exit status; see Advisory.
	Advisories []Advisory `json:"advisories,omitempty"`
}

type source interface {
	ListTaskHeads(context.Context, core.ProjectConfig) ([]gitstore.TaskHead, error)
	ReadTaskHistoriesStream(context.Context, core.ProjectConfig, []gitstore.TaskHistoryRequest, gitstore.TaskHistoryStream) error
}

// Validator coordinates bounded Git history reads with the disposable cache.
type Validator struct {
	source      source
	cache       *Cache
	config      core.ProjectConfig
	afterRecord func() // test hook for deterministic interruption between task completions
}

// Open creates a validator for repository's shared Git directory.
func Open(ctx context.Context, repository *gitstore.Repository, config core.ProjectConfig) (*Validator, error) {
	if repository == nil {
		return nil, core.Errorf(core.CategoryOperational, "validation repository is required")
	}
	cache, err := OpenCache(ctx, repository.CommonGitDir, config)
	if err != nil {
		return nil, err
	}
	return &Validator{source: repository, cache: cache, config: config}, nil
}

// Close releases the disposable SQLite cache handle.
func (v *Validator) Close() error {
	if v == nil || v.cache == nil {
		return nil
	}
	return v.cache.Close()
}

// Validate audits every pending canonical task history and resumes from a
// reachable, semantically validated checkpoint when possible.
func (v *Validator) Validate(ctx context.Context, full bool) (Result, error) {
	result := v.emptyResult(full)
	if v == nil || v.source == nil || v.cache == nil {
		return result, core.Errorf(core.CategoryOperational, "validation is not initialized")
	}

	initialHeads, err := v.source.ListTaskHeads(ctx, v.config)
	if err != nil {
		return result, err
	}
	sortHeads(initialHeads)
	result.TaskCount = len(initialHeads)
	prepared, err := v.cache.Prepare(ctx, initialHeads, full)
	if err != nil {
		partial := v.partialResult(ctx, initialHeads, result)
		if full {
			partial = pendingResult(initialHeads, result)
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return partial, contextErr
		}
		return partial, err
	}

	requests := make([]gitstore.TaskHistoryRequest, 0, len(initialHeads))
	boundaries := make(map[string]*core.StateDocument, len(initialHeads))
	for _, head := range initialHeads {
		cached := prepared[head.TaskID]
		switch cached.Status {
		case StatusValid, StatusInvalid:
			result.CacheHits++
			continue
		case StatusPending:
		default:
			return v.partialResult(ctx, initialHeads, result), core.Errorf(core.CategoryOperational, "validation task %q has unsupported status %q", head.TaskID, cached.Status)
		}

		request := gitstore.TaskHistoryRequest{Head: head}
		if !full {
			if boundary, ok := cachedBoundary(cached); ok {
				request.StopAt = cached.LastValidCommit
				boundaries[head.TaskID] = boundary
			}
		}
		requests = append(requests, request)
	}

	if len(requests) > 0 {
		// A full run re-reads every task from its root, so nothing the cache
		// recorded is authoritative and the seeded set would be empty anyway.
		// Skipping the read keeps `validate --full` from scanning the whole
		// validated_operations table to build two maps it never consults.
		var seen map[string]ValidatedOperation
		var seeded map[string][]ValidatedOperation
		if !full {
			var err error
			seen, seeded, err = v.loadRecordedOperations(ctx, requests)
			if err != nil {
				return v.partialResult(ctx, initialHeads, result), err
			}
		} else {
			seen = make(map[string]ValidatedOperation)
		}
		fold := &historyFold{
			validator:      v,
			ctx:            ctx,
			requests:       requests,
			prepared:       prepared,
			boundaries:     boundaries,
			full:           full,
			result:         &result,
			seenOperations: seen,
			seededPrefixes: seeded,
		}
		streamErr := v.source.ReadTaskHistoriesStream(ctx, v.config, requests, gitstore.TaskHistoryStream{
			Begin:  fold.begin,
			Commit: fold.commit,
			End:    fold.end,
		})
		if streamErr != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return v.partialResult(ctx, initialHeads, result), contextErr
			}
			return v.partialResult(ctx, initialHeads, result), streamErr
		}
		if fold.completed != len(requests) {
			return v.partialResult(ctx, initialHeads, result), core.Errorf(
				core.CategoryCorruptData,
				"task history read returned %d results for %d requests",
				fold.completed,
				len(requests),
			)
		}
	}

	finalHeads, err := v.source.ListTaskHeads(ctx, v.config)
	if err != nil {
		return v.partialResult(ctx, initialHeads, result), err
	}
	sortHeads(finalHeads)
	if _, err := v.cache.Prepare(ctx, finalHeads, false); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return v.partialResult(ctx, finalHeads, result), contextErr
		}
		return v.partialResult(ctx, finalHeads, result), err
	}
	result, err = v.completeResult(ctx, finalHeads, result)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// The configuration ledger is audited after the tasks, and its outcome is
	// reported whatever theirs was. It is a separate history with a separate
	// fold, so a project whose tasks are sound and whose ledger is not has to
	// be able to say exactly that.
	configuration, advisories, configErr := v.validateConfig(ctx)
	if configErr != nil {
		return result, configErr
	}
	result.Config = configuration
	result.Advisories = advisories
	if result.Invalid > 0 {
		return result, core.Errorf(core.CategoryCorruptData, "semantic history validation found %d invalid task(s)", result.Invalid)
	}
	if !sameHeads(initialHeads, finalHeads) {
		return result, core.Errorf(core.CategoryStaleWrite, "canonical task heads changed during validation")
	}
	// A newer-writer verdict fails the run, and the reasoning is worth stating
	// because the softer option was available. `validate` exists to answer
	// whether this clone can vouch for its history, and for these tasks it
	// cannot: it could not fold them, and every mutation against them is
	// refused until somebody upgrades. Exiting zero would tell a script the
	// project is fully audited when part of it was skipped, and the advisory
	// channel beside this one is deliberately reserved for states that need no
	// action at all. So it gets its own nonzero exit — distinct from
	// corruption, which is reported first, because a repository that is both
	// damaged and behind needs the damage looked at first.
	if result.NewerWriter > 0 {
		return result, core.Errorf(core.CategoryNewerWriter,
			"%d task history(s) were written by a newer workbook; upgrade workbook to validate them",
			result.NewerWriter)
	}
	// A ledger failure carries its own category forward rather than being
	// restated as corrupt data. A pack this clone declined to fold is an
	// operational refusal that names a bound somebody can raise, and calling it
	// corruption would tell a team their configuration history is broken when
	// it is merely large.
	if configuration != nil && !configuration.Valid {
		return result, core.Errorf(
			core.Category(configuration.Failure.Category),
			"configuration ledger validation failed at %s: %s",
			configuration.Failure.Commit, configuration.Failure.Message,
		)
	}
	return result, nil
}

// loadRecordedOperations builds the project-wide seen set every task in this
// run is folded against, before any of them is read.
//
// Two kinds of task contribute their recorded ULIDs to it. A task this run is
// not reading at all contributes because its chain is being taken on the
// cache's word. A task this run resumes at a cached boundary contributes its
// prefix for the same reason: the run will never re-read those commits, so the
// cache is the only witness that the task owns them.
//
// Seeding a resuming task's prefix up front rather than at its own Begin is
// what makes the verdict independent of task order. Tasks are streamed in task
// ID order, so a prefix seeded at its owner's Begin is invisible to every
// lower-sorting task in the same run — and a new task repeating a ULID from a
// resuming task's prefix would be reported valid purely because it sorts first.
//
// A task this run re-reads from its root contributes nothing: its stored rows
// are about to be replaced wholesale, and seeding them would report each of its
// own commits as a repeat of itself. Those rows are handed back separately so
// that a task whose cached boundary turns out to be unreachable — the one case
// where a request asks to resume and the read starts at the root anyway — can
// have its seed withdrawn at Begin.
func (v *Validator) loadRecordedOperations(
	ctx context.Context,
	requests []gitstore.TaskHistoryRequest,
) (map[string]ValidatedOperation, map[string][]ValidatedOperation, error) {
	recorded, err := v.cache.ValidatedOperations(ctx)
	if err != nil {
		return nil, nil, err
	}
	resuming := make(map[string]bool, len(requests))
	rereading := make(map[string]bool, len(requests))
	for _, request := range requests {
		if request.StopAt != "" {
			resuming[request.Head.TaskID] = true
			continue
		}
		rereading[request.Head.TaskID] = true
	}
	seen := make(map[string]ValidatedOperation, len(recorded))
	var seeded map[string][]ValidatedOperation
	for _, operation := range recorded {
		if rereading[operation.TaskID] {
			continue
		}
		if resuming[operation.TaskID] {
			if seeded == nil {
				seeded = make(map[string][]ValidatedOperation, len(resuming))
			}
			seeded[operation.TaskID] = append(seeded[operation.TaskID], operation)
		}
		// Rows arrive ordered, so the first owner of a ULID is the same one on
		// every run. A collision between two chains the cache already holds
		// cannot be attributed to a commit this run reads, so the run reports
		// the chains it can read and leaves that pair to `validate --full`,
		// which takes nothing from the cache.
		if _, exists := seen[operation.OperationID]; !exists {
			seen[operation.OperationID] = operation
		}
	}
	return seen, seeded, nil
}

// historyFold turns the streamed history of every pending task into one
// completion at a time. The audit is an incremental fold over each chain that
// needs only the parent state and the current record, so nothing above one
// task's accumulated completion is ever resident, and each completion is
// recorded and dropped at its own task boundary.
type historyFold struct {
	validator  *Validator
	ctx        context.Context
	requests   []gitstore.TaskHistoryRequest
	prepared   map[string]CachedTask
	boundaries map[string]*core.StateDocument
	full       bool
	result     *Result

	completed  int
	open       bool
	completion Completion
	parent     *core.StateDocument
	// lastValidState is encoded once at the task boundary. Encoding after every
	// commit would repeat work whose result only the final commit keeps.
	lastValidState *core.StateDocument
	failed         bool
	// seenOperations maps every operation ULID this run knows about to the task
	// and commit that recorded it. It spans the whole project and the whole run:
	// it is seeded from the cache for the tasks this run is not reading, and it
	// is never reset at a task boundary.
	//
	// Uniqueness is a property of the project, not of any one commit or chain,
	// so no amount of checkpoint comparison can see a violation: repeating a
	// ULID changes no projected state, and every checkpoint still folds. The
	// projection keys its operation rows on the ULID alone, with no task in the
	// key, so any chain that repeats one — whether within itself or against a
	// sibling task — is a chain no clone can hold, and a run that reported it
	// valid would be vouching for a repository that `workbook rebuild` cannot
	// repair.
	seenOperations map[string]ValidatedOperation
	// seededPrefixes holds, per resuming task, the recorded operations already
	// folded into seenOperations on its behalf. It exists so that a task whose
	// cached boundary the read could not reach — and which is therefore
	// re-reading its own prefix — can have that seed withdrawn before its
	// commits arrive.
	seededPrefixes map[string][]ValidatedOperation
}

func (f *historyFold) begin(start gitstore.TaskHistoryStart) error {
	if err := f.ctx.Err(); err != nil {
		return err
	}
	if f.open || f.completed >= len(f.requests) {
		return core.Errorf(core.CategoryCorruptData, "task history read returned an unexpected result")
	}
	request := f.requests[f.completed]
	if start.TaskID != request.Head.TaskID || start.Head != request.Head.ObjectID {
		return core.Errorf(
			core.CategoryCorruptData,
			"task history read result %d does not match requested task head",
			f.completed,
		)
	}
	cached := f.prepared[start.TaskID]
	f.open = true
	f.failed = false
	f.parent = nil
	f.lastValidState = nil
	f.completion = Completion{
		TaskID:         start.TaskID,
		ObservedHead:   start.Head,
		Status:         StatusValid,
		LastValidState: []byte{},
		// A retained boundary that Git cannot reach is no longer a safe
		// prefix. Rebuilding this task's immutable-commit set avoids duplicate
		// rows while preserving Record's duplicate-delivery guard.
		//
		// Record's full path also drops this task's recorded operation ULIDs,
		// which is more than rebuilding its own set: a task whose tip read
		// failed reaches here too, and it forgets ULIDs its ref still owns, so
		// a sibling repeating one of them is not caught until the broken task
		// is repaired and re-read. The run still exits nonzero, because the
		// task that failed is counted invalid, so `validate` never vouches for
		// the repository on the strength of the forgotten rows.
		Full: f.full || (!start.BoundaryReached && cached.LastValidCommit != ""),
	}
	if boundary := f.boundaries[start.TaskID]; start.BoundaryReached && boundary != nil {
		state := *boundary
		f.parent = &state
		f.completion.LastValidCommit = cached.LastValidCommit
		f.completion.LastValidGeneration = cached.LastValidGeneration
		f.completion.LastValidState = append([]byte(nil), cached.LastValidState...)
		f.completion.ValidatedCommitCount = cached.ValidatedCommitCount
		// The prefix's ULIDs are already in seenOperations: they were seeded
		// before any task was streamed, so that every task in this run — not
		// only the ones sorted after this one — is folded against them.
	} else {
		// The request asked to resume, but the read started at the root, so
		// this task is about to re-record the very operations that were seeded
		// on its behalf. Withdraw the seed first, or each of its own commits
		// is reported as a repeat of itself. Entries a different task owns are
		// left alone; only this task's claim is withdrawn.
		for _, operation := range f.seededPrefixes[start.TaskID] {
			if first, exists := f.seenOperations[operation.OperationID]; exists && first.TaskID == start.TaskID {
				delete(f.seenOperations, operation.OperationID)
			}
		}
	}
	f.result.TasksChecked++
	return nil
}

func (f *historyFold) commit(taskID string, record gitstore.HistoryCommit) error {
	if !f.open {
		return core.Errorf(core.CategoryCorruptData, "task history read delivered a commit outside a task")
	}
	// The first semantic failure is the reported one. Later commits of the same
	// task are still delivered, because the batch stream is shared, but they
	// must not move the recorded boundary past the corruption.
	if f.failed {
		return nil
	}
	// The checkpoint is compared first so that the pack this reads has already
	// been validated as a document, and so a commit this build cannot fold keeps
	// its own newer-writer verdict instead of being restated as damage.
	if err := core.ValidateCheckpoint(f.parent, record.Operation, record.State, f.validator.config.Key); err != nil {
		f.completion.Status = StatusInvalid
		f.completion.Failure = validationFailure(taskID, record.ObjectID, err)
		f.failed = true
		return nil
	}
	if err := f.checkOperationIDs(taskID, record); err != nil {
		f.completion.Status = StatusInvalid
		f.completion.Failure = validationFailure(taskID, record.ObjectID, err)
		f.failed = true
		return nil
	}
	for _, operation := range record.Operation.Operations {
		recorded := ValidatedOperation{TaskID: taskID, OperationID: operation.ID, CommitID: record.ObjectID}
		f.seenOperations[operation.ID] = recorded
		f.completion.ValidatedOperations = append(f.completion.ValidatedOperations, recorded)
	}
	state := record.State
	f.parent = &state
	f.lastValidState = &state
	f.completion.LastValidCommit = record.ObjectID
	f.completion.LastValidGeneration = state.History.Generation
	f.completion.ValidatedCommitCount++
	f.completion.ValidatedCommitIDs = append(f.completion.ValidatedCommitIDs, record.ObjectID)
	return nil
}

// checkOperationIDs refuses a commit that repeats an operation ULID any commit
// of any task already recorded. Duplicates inside one pack are already refused
// by the pack's own document validation, so this sees only the across-commit
// case that validation had no other way to reach — and, because the projection
// keys operations globally, the across-task case as well.
func (f *historyFold) checkOperationIDs(taskID string, record gitstore.HistoryCommit) error {
	for _, operation := range record.Operation.Operations {
		first, repeated := f.seenOperations[operation.ID]
		if !repeated {
			continue
		}
		if first.TaskID == taskID {
			return core.Errorf(
				core.CategoryCorruptData,
				"operation ID %q is already recorded by commit %s earlier in this task history",
				operation.ID,
				first.CommitID,
			)
		}
		return core.Errorf(
			core.CategoryCorruptData,
			"operation ID %q is already recorded by commit %s in task %q, and operation IDs are unique across the project",
			operation.ID,
			first.CommitID,
			first.TaskID,
		)
	}
	return nil
}

func (f *historyFold) end(history gitstore.TaskHistoryResult) error {
	if !f.open {
		return core.Errorf(core.CategoryCorruptData, "task history read ended a task it never began")
	}
	if f.lastValidState != nil {
		encoded, err := core.EncodeDocument(*f.lastValidState)
		if err != nil {
			return err
		}
		f.completion.LastValidState = encoded
	}
	if !f.failed && history.Failure != nil {
		f.completion.Status = StatusInvalid
		f.completion.Failure = validationFailure(history.TaskID, history.Failure.Commit, history.Failure.Err)
	}
	f.result.CommitsChecked += history.CheckedCommits
	if err := f.validator.cache.Record(f.ctx, f.completion); err != nil {
		return err
	}
	f.completed++
	f.open = false
	f.completion = Completion{}
	f.parent = nil
	f.lastValidState = nil
	if f.validator.afterRecord != nil {
		f.validator.afterRecord()
	}
	return nil
}

func cachedBoundary(cached CachedTask) (*core.StateDocument, bool) {
	if cached.Status != StatusPending || cached.LastValidCommit == "" || cached.LastValidGeneration == "" || len(cached.LastValidState) == 0 {
		return nil, false
	}
	state, err := core.DecodeStateDocument(cached.LastValidState)
	if err != nil || state.TaskID != cached.TaskID || state.History.Generation != cached.LastValidGeneration {
		return nil, false
	}
	return &state, true
}

func validationFailure(taskID, commit string, err error) *Failure {
	category := core.CategoryOf(err)
	if category == "" {
		category = core.CategoryCorruptData
	}
	message := "invalid task history"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = err.Error()
	}
	return &Failure{TaskID: taskID, Commit: commit, Category: string(category), Message: message}
}

func (v *Validator) emptyResult(full bool) Result {
	result := Result{ValidatorVersion: ValidatorVersion, Full: full}
	if v != nil && v.cache != nil {
		result.CachePath = v.cache.Path()
	}
	return result
}

func (v *Validator) partialResult(ctx context.Context, heads []gitstore.TaskHead, prior Result) Result {
	result, _ := v.resultFromSnapshot(ctx, heads, prior)
	return result
}

func pendingResult(heads []gitstore.TaskHead, prior Result) Result {
	prior.TaskCount = len(heads)
	prior.Valid = 0
	prior.Invalid = 0
	prior.Pending = len(heads)
	prior.Failures = nil
	return prior
}

func (v *Validator) completeResult(ctx context.Context, heads []gitstore.TaskHead, prior Result) (Result, error) {
	return v.resultFromSnapshot(ctx, heads, prior)
}

func (v *Validator) resultFromSnapshot(ctx context.Context, heads []gitstore.TaskHead, prior Result) (Result, error) {
	prior.TaskCount = len(heads)
	prior.CachePath = v.cache.Path()
	taskIDs := make([]string, len(heads))
	for index, head := range heads {
		taskIDs[index] = head.TaskID
	}
	cached, err := v.cache.Snapshot(context.WithoutCancel(ctx), taskIDs)
	if err != nil {
		return prior, err
	}
	prior.Valid, prior.Invalid, prior.NewerWriter, prior.Pending = 0, 0, 0, 0
	prior.Failures = nil
	byTaskID := make(map[string]CachedTask, len(cached))
	for _, task := range cached {
		byTaskID[task.TaskID] = task
	}
	for _, head := range heads {
		task, found := byTaskID[head.TaskID]
		if !found || task.ObservedHead != head.ObjectID || task.ValidatorVersion != ValidatorVersion {
			prior.Pending++
			continue
		}
		switch task.Status {
		case StatusValid:
			prior.Valid++
		case StatusInvalid:
			// The cache stores one failed status, and the category on the
			// failure is what separates "this history is wrong" from "this
			// history is newer than this build". Splitting them here rather
			// than in the schema keeps every cached row written before the
			// signal existed readable, and it keeps the one place that decides
			// what a category means in the same file as the counts it feeds.
			if task.Failure != nil && core.Category(task.Failure.Category) == core.CategoryNewerWriter {
				prior.NewerWriter++
			} else {
				prior.Invalid++
			}
			if task.Failure != nil {
				prior.Failures = append(prior.Failures, *task.Failure)
			}
		default:
			prior.Pending++
		}
	}
	sort.Slice(prior.Failures, func(i, j int) bool { return prior.Failures[i].TaskID < prior.Failures[j].TaskID })
	return prior, nil
}

func sortHeads(heads []gitstore.TaskHead) {
	sort.Slice(heads, func(i, j int) bool { return heads[i].TaskID < heads[j].TaskID })
}

func sameHeads(left, right []gitstore.TaskHead) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

var _ source = (*gitstore.Repository)(nil)
