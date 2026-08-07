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
	ValidatorVersion int       `json:"validatorVersion"`
	Full             bool      `json:"full"`
	TaskCount        int       `json:"taskCount"`
	TasksChecked     int       `json:"tasksChecked"`
	CommitsChecked   int       `json:"commitsChecked"`
	CacheHits        int       `json:"cacheHits"`
	Valid            int       `json:"valid"`
	Invalid          int       `json:"invalid"`
	Pending          int       `json:"pending"`
	CachePath        string    `json:"cachePath"`
	Failures         []Failure `json:"failures"`
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
		fold := &historyFold{
			validator:  v,
			ctx:        ctx,
			requests:   requests,
			prepared:   prepared,
			boundaries: boundaries,
			full:       full,
			result:     &result,
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
	if result.Invalid > 0 {
		return result, core.Errorf(core.CategoryCorruptData, "semantic history validation found %d invalid task(s)", result.Invalid)
	}
	if !sameHeads(initialHeads, finalHeads) {
		return result, core.Errorf(core.CategoryStaleWrite, "canonical task heads changed during validation")
	}
	return result, nil
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
		Full: f.full || (!start.BoundaryReached && cached.LastValidCommit != ""),
	}
	if boundary := f.boundaries[start.TaskID]; start.BoundaryReached && boundary != nil {
		state := *boundary
		f.parent = &state
		f.completion.LastValidCommit = cached.LastValidCommit
		f.completion.LastValidGeneration = cached.LastValidGeneration
		f.completion.LastValidState = append([]byte(nil), cached.LastValidState...)
		f.completion.ValidatedCommitCount = cached.ValidatedCommitCount
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
	if err := core.ValidateCheckpoint(f.parent, record.Operation, record.State, f.validator.config.Key); err != nil {
		f.completion.Status = StatusInvalid
		f.completion.Failure = validationFailure(taskID, record.ObjectID, err)
		f.failed = true
		return nil
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
	prior.Valid, prior.Invalid, prior.Pending = 0, 0, 0
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
			prior.Invalid++
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
