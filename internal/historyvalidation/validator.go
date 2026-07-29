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
	ReadTaskHistories(context.Context, core.ProjectConfig, []gitstore.TaskHistoryRequest) ([]gitstore.TaskHistoryResult, error)
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
		histories, readErr := v.source.ReadTaskHistories(ctx, v.config, requests)
		if readErr != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return v.partialResult(ctx, initialHeads, result), contextErr
			}
			return v.partialResult(ctx, initialHeads, result), readErr
		}
		if err := checkHistoryResults(requests, histories); err != nil {
			return v.partialResult(ctx, initialHeads, result), err
		}
		for _, history := range histories {
			if err := ctx.Err(); err != nil {
				return v.partialResult(ctx, initialHeads, result), err
			}
			result.TasksChecked++
			result.CommitsChecked += history.CheckedCommits
			cached := prepared[history.TaskID]
			completion, completionErr := v.evaluate(history, cached, boundaries[history.TaskID], full)
			if completionErr != nil {
				return v.partialResult(ctx, initialHeads, result), completionErr
			}
			if err := v.cache.Record(ctx, completion); err != nil {
				if contextErr := ctx.Err(); contextErr != nil {
					return v.partialResult(ctx, initialHeads, result), contextErr
				}
				return v.partialResult(ctx, initialHeads, result), err
			}
			if v.afterRecord != nil {
				v.afterRecord()
			}
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

func (v *Validator) evaluate(history gitstore.TaskHistoryResult, cached CachedTask, boundary *core.StateDocument, full bool) (Completion, error) {
	completion := Completion{
		TaskID:         history.TaskID,
		ObservedHead:   history.Head,
		Status:         StatusValid,
		LastValidState: []byte{},
		// A retained boundary that Git cannot reach is no longer a safe
		// prefix. Rebuilding this task's immutable-commit set avoids duplicate
		// rows while preserving Record's duplicate-delivery guard.
		Full: full || (!history.BoundaryReached && cached.LastValidCommit != ""),
	}
	var parent *core.StateDocument
	if history.BoundaryReached && boundary != nil {
		state := *boundary
		parent = &state
		completion.LastValidCommit = cached.LastValidCommit
		completion.LastValidGeneration = cached.LastValidGeneration
		completion.LastValidState = append([]byte(nil), cached.LastValidState...)
		completion.ValidatedCommitCount = cached.ValidatedCommitCount
	}

	for _, record := range history.Commits {
		if err := core.ValidateCheckpoint(parent, record.Operation, record.State, v.config.Key); err != nil {
			completion.Status = StatusInvalid
			completion.Failure = validationFailure(history.TaskID, record.ObjectID, err)
			return completion, nil
		}
		encoded, err := core.EncodeDocument(record.State)
		if err != nil {
			return Completion{}, err
		}
		state := record.State
		parent = &state
		completion.LastValidCommit = record.ObjectID
		completion.LastValidGeneration = record.State.History.Generation
		completion.LastValidState = encoded
		completion.ValidatedCommitCount++
		completion.ValidatedCommitIDs = append(completion.ValidatedCommitIDs, record.ObjectID)
	}
	if history.Failure != nil {
		completion.Status = StatusInvalid
		completion.Failure = validationFailure(history.TaskID, history.Failure.Commit, history.Failure.Err)
	}
	return completion, nil
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

func checkHistoryResults(requests []gitstore.TaskHistoryRequest, histories []gitstore.TaskHistoryResult) error {
	if len(histories) != len(requests) {
		return core.Errorf(core.CategoryCorruptData, "task history read returned %d results for %d requests", len(histories), len(requests))
	}
	for index, request := range requests {
		result := histories[index]
		if result.TaskID != request.Head.TaskID || result.Head != request.Head.ObjectID {
			return core.Errorf(core.CategoryCorruptData, "task history read result %d does not match requested task head", index)
		}
	}
	return nil
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
