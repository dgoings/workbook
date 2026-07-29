package historyvalidation

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testrepo"
)

func TestValidateChecksEveryCheckpointAndCachesValidCommits(t *testing.T) {
	// Production mutation: skipping a checkpoint validation or its cache record reports a green audit for a corrupt historical state.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	first := validationHistory(t, taskID(1), generationID(1), 10, 3)
	second := validationHistory(t, taskID(2), generationID(2), 20, 3)
	source := &validatorSource{heads: []gitstore.TaskHead{{TaskID: taskID(2), ObjectID: second[2].ObjectID}, {TaskID: taskID(1), ObjectID: first[2].ObjectID}}, histories: map[string]gitstore.TaskHistoryResult{
		taskID(1): historyResult(taskID(1), first[2].ObjectID, false, first),
		taskID(2): historyResult(taskID(2), second[2].ObjectID, false, second),
	}}
	v := &Validator{source: source, cache: cache, config: testConfig()}

	got, err := v.Validate(ctx, false)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got.TasksChecked != 2 || got.CommitsChecked != 6 || got.CacheHits != 0 || got.Valid != 2 || got.Invalid != 0 || got.Pending != 0 {
		t.Fatalf("fresh result = %#v, want 2 tasks, 6 commits, 0 hits, 2 valid", got)
	}
	if got.CachePath != cache.Path() {
		t.Fatalf("cache path = %q, want %q", got.CachePath, cache.Path())
	}
	got, err = v.Validate(ctx, false)
	if err != nil {
		t.Fatalf("cached Validate() error = %v", err)
	}
	if got.TasksChecked != 0 || got.CommitsChecked != 0 || got.CacheHits != 2 || got.Valid != 2 {
		t.Fatalf("cached result = %#v, want 0 tasks, 0 commits, 2 hits, 2 valid", got)
	}
}

func TestValidateContinuesAllTasksAndReportsEveryFirstFailure(t *testing.T) {
	// Production mutation: returning after the first invalid task hides independent corruption from the user.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	valid := validationHistory(t, taskID(1), generationID(1), 30, 2)
	badOne := validationHistory(t, taskID(2), generationID(2), 40, 2)
	badTwo := validationHistory(t, taskID(3), generationID(3), 50, 2)
	badOne[1].State.Task.Title = "tampered"
	badTwo[0].State.Task.Title = "tampered"
	source := &validatorSource{heads: headsFor(valid, badOne, badTwo), histories: map[string]gitstore.TaskHistoryResult{
		taskID(1): historyResult(taskID(1), valid[1].ObjectID, false, valid),
		taskID(2): historyResult(taskID(2), badOne[1].ObjectID, false, badOne),
		taskID(3): historyResult(taskID(3), badTwo[1].ObjectID, false, badTwo),
	}}
	got, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, false)
	if category := core.CategoryOf(err); category != core.CategoryCorruptData {
		t.Fatalf("Validate() category = %q, want corrupt-data; error = %v", category, err)
	}
	if got.Valid != 1 || got.Invalid != 2 || len(got.Failures) != 2 || got.Failures[0].TaskID != taskID(2) || got.Failures[1].TaskID != taskID(3) {
		t.Fatalf("failure result = %#v, want sorted first failures for both corrupt tasks", got)
	}
}

func TestValidateReusesUnchangedValidAndInvalidHeads(t *testing.T) {
	// Production mutation: rereading unchanged invalid heads both wastes Git work and can make an existing failure disappear.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	valid := validationHistory(t, taskID(1), generationID(1), 60, 1)
	invalid := validationHistory(t, taskID(2), generationID(2), 70, 1)
	invalid[0].State.Task.Title = "tampered"
	source := &validatorSource{heads: headsFor(valid, invalid), histories: map[string]gitstore.TaskHistoryResult{
		taskID(1): historyResult(taskID(1), valid[0].ObjectID, false, valid), taskID(2): historyResult(taskID(2), invalid[0].ObjectID, false, invalid),
	}}
	v := &Validator{source: source, cache: cache, config: testConfig()}
	_, _ = v.Validate(ctx, false)
	source.reads = 0
	got, err := v.Validate(ctx, false)
	if category := core.CategoryOf(err); category != core.CategoryCorruptData {
		t.Fatalf("cached invalid category = %q, want corrupt-data", category)
	}
	if source.reads != 0 || got.CacheHits != 2 || got.TasksChecked != 0 || got.Invalid != 1 || len(got.Failures) != 1 {
		t.Fatalf("cached invalid result = %#v, reads = %d; want cached failure without history read", got, source.reads)
	}
}

func TestValidateChangedHeadUsesReachableBoundaryAndChecksOnlyDescendants(t *testing.T) {
	// Production mutation: omitting StopAt replays the full history after every one-commit change.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	history := validationHistory(t, taskID(1), generationID(1), 80, 3)
	source := &validatorSource{heads: []gitstore.TaskHead{{TaskID: taskID(1), ObjectID: history[1].ObjectID}}, histories: map[string]gitstore.TaskHistoryResult{taskID(1): historyResult(taskID(1), history[1].ObjectID, false, history[:2])}}
	v := &Validator{source: source, cache: cache, config: testConfig()}
	if _, err := v.Validate(ctx, false); err != nil {
		t.Fatal(err)
	}
	source.heads = []gitstore.TaskHead{{TaskID: taskID(1), ObjectID: history[2].ObjectID}}
	source.histories[taskID(1)] = historyResult(taskID(1), history[2].ObjectID, true, history[2:])
	got, err := v.Validate(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.TasksChecked != 1 || got.CommitsChecked != 1 || source.lastRequests[0].StopAt != history[1].ObjectID {
		t.Fatalf("incremental result = %#v, requests = %#v; want one descendant after cached boundary", got, source.lastRequests)
	}
}

func TestValidateUnreachableBoundaryRestartsAtRoot(t *testing.T) {
	// Production mutation: trusting an unreachable cached state validates a suffix against the wrong parent.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	history := validationHistory(t, taskID(1), generationID(1), 90, 3)
	source := &validatorSource{heads: []gitstore.TaskHead{{TaskID: taskID(1), ObjectID: history[1].ObjectID}}, histories: map[string]gitstore.TaskHistoryResult{taskID(1): historyResult(taskID(1), history[1].ObjectID, false, history[:2])}}
	v := &Validator{source: source, cache: cache, config: testConfig()}
	if _, err := v.Validate(ctx, false); err != nil {
		t.Fatal(err)
	}
	source.heads = []gitstore.TaskHead{{TaskID: taskID(1), ObjectID: history[2].ObjectID}}
	source.histories[taskID(1)] = historyResult(taskID(1), history[2].ObjectID, false, history)
	got, err := v.Validate(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitsChecked != 3 || source.lastRequests[0].StopAt != history[1].ObjectID {
		t.Fatalf("unreachable result = %#v, requests = %#v; want root replay after an unreachable boundary", got, source.lastRequests)
	}
}

func TestValidateFullBypassesCachedValidAndInvalidResults(t *testing.T) {
	// Production mutation: honoring completed rows during --full lets a prior invalid or valid result bypass requested revalidation.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	valid := validationHistory(t, taskID(1), generationID(1), 100, 1)
	invalid := validationHistory(t, taskID(2), generationID(2), 110, 1)
	invalid[0].State.Task.Title = "tampered"
	source := &validatorSource{heads: headsFor(valid, invalid), histories: map[string]gitstore.TaskHistoryResult{taskID(1): historyResult(taskID(1), valid[0].ObjectID, false, valid), taskID(2): historyResult(taskID(2), invalid[0].ObjectID, false, invalid)}}
	v := &Validator{source: source, cache: cache, config: testConfig()}
	_, _ = v.Validate(ctx, false)
	source.reads = 0
	got, err := v.Validate(ctx, true)
	if core.CategoryOf(err) != core.CategoryCorruptData || source.reads != 1 || got.TasksChecked != 2 || got.CacheHits != 0 {
		t.Fatalf("full result = %#v, reads = %d; want both heads reread", got, source.reads)
	}
	for _, request := range source.lastRequests {
		if request.StopAt != "" {
			t.Fatalf("full request = %#v, want no boundary", request)
		}
	}
}

func TestValidateCancellationPreservesCompletedTasksAndLeavesPending(t *testing.T) {
	// Production mutation: converting cancellation into invalidity poisons resumable state instead of leaving work pending.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := openTestCache(t, context.Background(), testConfig())
	history := validationHistory(t, taskID(1), generationID(1), 120, 1)
	source := &validatorSource{heads: []gitstore.TaskHead{{TaskID: taskID(1), ObjectID: history[0].ObjectID}}, histories: map[string]gitstore.TaskHistoryResult{taskID(1): historyResult(taskID(1), history[0].ObjectID, false, history)}, cancelOnRead: cancel}
	got, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, false)
	if err != context.Canceled || got.Pending != 1 || got.Invalid != 0 {
		t.Fatalf("canceled result = %#v, error = %v; want pending and context cancellation", got, err)
	}
}

func TestValidateRefRaceLeavesChangedTaskPendingAndReturnsStaleWrite(t *testing.T) {
	// Production mutation: accepting the initial inventory after a ref race marks a stale head valid.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	history := validationHistory(t, taskID(1), generationID(1), 130, 2)
	source := &validatorSource{headLists: [][]gitstore.TaskHead{{{TaskID: taskID(1), ObjectID: history[0].ObjectID}}, {{TaskID: taskID(1), ObjectID: history[1].ObjectID}}}, histories: map[string]gitstore.TaskHistoryResult{taskID(1): historyResult(taskID(1), history[0].ObjectID, false, history[:1])}}
	got, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, false)
	if core.CategoryOf(err) != core.CategoryStaleWrite || got.Pending != 1 || got.Valid != 0 {
		t.Fatalf("race result = %#v, error = %v; want stale pending task", got, err)
	}
}

func TestValidateNeverMutatesCanonicalRefs(t *testing.T) {
	// Production mutation: using a write-capable repository call during validation changes the canonical task namespace.
	ctx := context.Background()
	repo, err := gitstore.Open(ctx, testrepo.New(t))
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{
		"01K0M6B8A4FTT8C39MXXYTW7C1",
		"01K0M6B8A4FTT8C39MXXYTW7D1",
		"01K0M6B8A4FTT8C39MXXYTW7E1",
		"01K0M6B8A4FTT8C39MXXYTW7F1",
	}
	index := 0
	newID := core.IDSourceFunc(func() (string, error) { value := ids[index]; index++; return value, nil })
	config, _, err := repo.Init(ctx, "WB", newID)
	if err != nil {
		t.Fatal(err)
	}
	service := core.Service{Config: config, Reader: repo, Writer: repo, IDs: newID, Now: func() time.Time { return time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC) }, Actor: "validator@example.test"}
	if _, err := service.CreateMutation(ctx, core.CreateInput{Title: "immutable history"}); err != nil {
		t.Fatal(err)
	}
	before, err := repo.Git(ctx, nil, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/workbook/tasks/")
	if err != nil {
		t.Fatal(err)
	}
	v, err := Open(ctx, repo, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	if _, err := v.Validate(ctx, false); err != nil {
		t.Fatal(err)
	}
	after, err := repo.Git(ctx, nil, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/workbook/tasks/")
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("canonical refs after validation = %q, want unchanged %q", after, before)
	}
}

type validatorSource struct {
	heads        []gitstore.TaskHead
	headLists    [][]gitstore.TaskHead
	historyIndex int
	histories    map[string]gitstore.TaskHistoryResult
	lastRequests []gitstore.TaskHistoryRequest
	reads        int
	cancelOnRead context.CancelFunc
}

func (s *validatorSource) ListTaskHeads(_ context.Context, _ core.ProjectConfig) ([]gitstore.TaskHead, error) {
	if len(s.headLists) > 0 {
		index := s.historyIndex
		if index >= len(s.headLists) {
			index = len(s.headLists) - 1
		}
		s.historyIndex++
		return append([]gitstore.TaskHead(nil), s.headLists[index]...), nil
	}
	return append([]gitstore.TaskHead(nil), s.heads...), nil
}

func (s *validatorSource) ReadTaskHistories(_ context.Context, _ core.ProjectConfig, requests []gitstore.TaskHistoryRequest) ([]gitstore.TaskHistoryResult, error) {
	s.reads++
	s.lastRequests = append([]gitstore.TaskHistoryRequest(nil), requests...)
	results := make([]gitstore.TaskHistoryResult, 0, len(requests))
	for _, request := range requests {
		results = append(results, s.histories[request.Head.TaskID])
	}
	if s.cancelOnRead != nil {
		s.cancelOnRead()
		s.cancelOnRead = nil
	}
	return results, nil
}

func validationHistory(t *testing.T, id, generation string, base, count int) []gitstore.HistoryCommit {
	t.Helper()
	created := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	task := core.TaskData{Title: "validator task", Status: core.StatusBacklog, Priority: core.PriorityMedium, Labels: []string{}, Rank: "1/1", Dependencies: []string{}, CreatedAt: created, UpdatedAt: created}
	var parent *core.StateDocument
	result := make([]gitstore.HistoryCommit, 0, count)
	for i := 0; i < count; i++ {
		pack := core.OperationPack{Format: "workbook.operation-pack", Version: 1, ProjectID: testConfig().ProjectID, TaskID: id, HistoryGeneration: generation, Actor: core.Actor{ID: "validator@example.test"}, LogicalClock: uint64(i + 1), WallTime: created.Add(time.Duration(i) * time.Minute)}
		if i == 0 {
			pack.Operations = []core.Operation{{ID: validatorULID(base + i), Type: core.OperationTaskCreate, Task: &task}}
		} else {
			pack.Operations = []core.Operation{{ID: validatorULID(base + i), Type: core.OperationFieldSet, Field: "status", Value: string(core.StatusReady)}}
		}
		state, err := core.Apply(parent, pack, testConfig().Key)
		if err != nil {
			t.Fatalf("Apply(%d) error = %v", i, err)
		}
		objectID := fmt.Sprintf("commit-%s-%d", strings.ToLower(id), i)
		parents := []string{}
		if i > 0 {
			parents = []string{result[i-1].ObjectID}
		}
		result = append(result, gitstore.HistoryCommit{ObjectID: objectID, Parents: parents, Operation: pack, State: state})
		parent = &state
	}
	return result
}

func validatorULID(value int) string { return fmt.Sprintf("01K0M6B8A4FTT8C39MXXY%05X", value) }

func historyResult(id, head string, boundary bool, commits []gitstore.HistoryCommit) gitstore.TaskHistoryResult {
	return gitstore.TaskHistoryResult{TaskID: id, Head: head, BoundaryReached: boundary, CheckedCommits: len(commits), Commits: commits}
}

func headsFor(histories ...[]gitstore.HistoryCommit) []gitstore.TaskHead {
	heads := make([]gitstore.TaskHead, 0, len(histories))
	for _, history := range histories {
		heads = append(heads, gitstore.TaskHead{TaskID: history[0].Operation.TaskID, ObjectID: history[len(history)-1].ObjectID})
	}
	sort.Slice(heads, func(i, j int) bool { return heads[i].TaskID < heads[j].TaskID })
	return heads
}
