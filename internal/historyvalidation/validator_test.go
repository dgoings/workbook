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
	structuralMessage := "synthetic structural failure"
	source := &validatorSource{heads: headsFor(valid, badOne, badTwo), histories: map[string]gitstore.TaskHistoryResult{
		taskID(1): historyResult(taskID(1), valid[1].ObjectID, false, valid),
		taskID(2): historyResult(taskID(2), badOne[1].ObjectID, false, badOne),
		taskID(3): {
			TaskID: taskID(3), Head: badTwo[1].ObjectID, CheckedCommits: 2,
			Commits: badTwo[:1], Failure: &gitstore.HistoryFailure{TaskID: taskID(3), Commit: badTwo[1].ObjectID, Err: core.Errorf(core.CategoryCorruptData, "%s", structuralMessage)},
		},
	}}
	got, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, false)
	if category := core.CategoryOf(err); category != core.CategoryCorruptData {
		t.Fatalf("Validate() category = %q, want corrupt-data; error = %v", category, err)
	}
	if got.Valid != 1 || got.Invalid != 2 || len(got.Failures) != 2 || got.Failures[0].TaskID != taskID(2) || got.Failures[1].TaskID != taskID(3) {
		t.Fatalf("failure result = %#v, want sorted first failures for both corrupt tasks", got)
	}
	if got.Failures[0].Commit != badOne[1].ObjectID || got.Failures[0].Category != string(core.CategoryCorruptData) || got.Failures[0].Message != "stored checkpoint differs from computed state" {
		t.Fatalf("semantic failure = %#v, want exact first checkpoint failure", got.Failures[0])
	}
	if got.Failures[1].Commit != badTwo[1].ObjectID || got.Failures[1].Category != string(core.CategoryCorruptData) || got.Failures[1].Message != structuralMessage {
		t.Fatalf("structural failure = %#v, want exact transport failure", got.Failures[1])
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

func TestValidateValidatorVersionMismatchReplaysCompleteHistory(t *testing.T) {
	// Production mutation: retaining an older validator's boundary lets an unchanged head validate zero commits under new rules.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	history := validationHistory(t, taskID(1), generationID(1), 85, 3)
	head := gitstore.TaskHead{TaskID: taskID(1), ObjectID: history[2].ObjectID}
	if _, err := cache.Prepare(ctx, []gitstore.TaskHead{head}, false); err != nil {
		t.Fatal(err)
	}
	lastState, err := core.EncodeDocument(history[2].State)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Record(ctx, Completion{
		TaskID:               head.TaskID,
		ObservedHead:         head.ObjectID,
		Status:               StatusValid,
		LastValidCommit:      head.ObjectID,
		LastValidGeneration:  generationID(1),
		LastValidState:       lastState,
		ValidatedCommitIDs:   []string{history[0].ObjectID, history[1].ObjectID, history[2].ObjectID},
		ValidatedCommitCount: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.db.ExecContext(ctx, `UPDATE task_validation SET validator_version = ? WHERE task_id = ?`, ValidatorVersion-1, head.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.db.ExecContext(ctx, `UPDATE validated_commits SET validator_version = ? WHERE task_id = ?`, ValidatorVersion-1, head.TaskID); err != nil {
		t.Fatal(err)
	}

	source := &validatorSource{
		heads: []gitstore.TaskHead{head},
		historyForRequest: func(request gitstore.TaskHistoryRequest) gitstore.TaskHistoryResult {
			if request.StopAt == head.ObjectID {
				return historyResult(head.TaskID, head.ObjectID, true, nil)
			}
			return historyResult(head.TaskID, head.ObjectID, false, history)
		},
	}
	got, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.TasksChecked != 1 || got.CommitsChecked != 3 || got.CacheHits != 0 || got.Valid != 1 || got.Pending != 0 {
		t.Fatalf("version-mismatch result = %#v, want complete three-commit replay", got)
	}
	if len(source.lastRequests) != 1 || source.lastRequests[0].StopAt != "" {
		t.Fatalf("version-mismatch requests = %#v, want empty StopAt", source.lastRequests)
	}
	snapshot, err := cache.Snapshot(ctx, []string{head.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 1 || snapshot[0].ValidatorVersion != ValidatorVersion || snapshot[0].Status != StatusValid || snapshot[0].ValidatedCommitCount != 3 || snapshot[0].LastValidCommit != head.ObjectID {
		t.Fatalf("current-version completion = %#v, want replayed current result", snapshot)
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
	first := validationHistory(t, taskID(1), generationID(1), 120, 1)
	second := validationHistory(t, taskID(2), generationID(2), 130, 1)
	source := &validatorSource{heads: headsFor(first, second), histories: map[string]gitstore.TaskHistoryResult{
		taskID(1): historyResult(taskID(1), first[0].ObjectID, false, first),
		taskID(2): historyResult(taskID(2), second[0].ObjectID, false, second),
	}}
	v := &Validator{source: source, cache: cache, config: testConfig(), afterRecord: cancel}
	got, err := v.Validate(ctx, false)
	if err != context.Canceled || got.Valid != 1 || got.Pending != 1 || got.Invalid != 0 || got.TasksChecked != 1 {
		t.Fatalf("canceled result = %#v, error = %v; want first completion and second pending", got, err)
	}
}

func TestValidateInterruptedPrepareCountsMismatchedAndMissingFinalHeadsPending(t *testing.T) {
	// Production mutation: counting cache rows by task ID alone reports a previous head valid after final preparation is interrupted.
	for _, scenario := range []struct {
		name        string
		finalHeads  []gitstore.TaskHead
		wantValid   int
		wantPending int
	}{
		{name: "changed", finalHeads: []gitstore.TaskHead{{TaskID: taskID(1), ObjectID: "new-head"}}, wantPending: 1},
		{name: "added", finalHeads: []gitstore.TaskHead{{TaskID: taskID(1), ObjectID: "old-head"}, {TaskID: taskID(2), ObjectID: "added-head"}}, wantValid: 1, wantPending: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			cache := openTestCache(t, ctx, testConfig())
			seedPreparedValid(t, ctx, cache, gitstore.TaskHead{TaskID: taskID(1), ObjectID: "old-head"}, generationID(1))
			canceled, cancel := context.WithCancel(ctx)
			defer cancel()
			source := &validatorSource{headLists: [][]gitstore.TaskHead{{{TaskID: taskID(1), ObjectID: "old-head"}}, scenario.finalHeads}, cancelOnListCall: 2, cancel: cancel}
			got, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(canceled, false)
			if err != context.Canceled || got.Valid != scenario.wantValid || got.Pending != scenario.wantPending || got.Invalid != 0 || got.TaskCount != len(scenario.finalHeads) {
				t.Fatalf("interrupted %s result = %#v, error = %v", scenario.name, got, err)
			}
		})
	}
}

func TestValidateInterruptedInitialPrepareCountsEveryHeadPending(t *testing.T) {
	// Production mutation: returning the empty result after an interrupted initial Prepare omits known canonical heads.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := openTestCache(t, context.Background(), testConfig())
	source := &validatorSource{heads: []gitstore.TaskHead{{TaskID: taskID(1), ObjectID: "first-head"}, {TaskID: taskID(2), ObjectID: "second-head"}}, cancelOnListCall: 1, cancel: cancel}
	got, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, false)
	if err != context.Canceled || got.TaskCount != 2 || got.Valid != 0 || got.Invalid != 0 || got.Pending != 2 {
		t.Fatalf("initial interruption result = %#v, error = %v; want both known heads pending", got, err)
	}
}

func TestValidateInterruptedFullInitialPrepareBypassesCachedHead(t *testing.T) {
	// Production mutation: returning normal partial cache accounting during a full audit reports an uncompleted cached head as valid.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := openTestCache(t, context.Background(), testConfig())
	head := gitstore.TaskHead{TaskID: taskID(1), ObjectID: "cached-head"}
	seedPreparedValid(t, context.Background(), cache, head, generationID(1))
	source := &validatorSource{heads: []gitstore.TaskHead{head}, cancelOnListCall: 1, cancel: cancel}
	got, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, true)
	if err != context.Canceled || !got.Full || got.CacheHits != 0 || got.Valid != 0 || got.Invalid != 0 || got.Pending != 1 {
		t.Fatalf("interrupted full result = %#v, error = %v; want cached head pending", got, err)
	}
}

func TestValidateCorruptDataOutranksFinalHeadRace(t *testing.T) {
	// Production mutation: returning stale-write first hides corruption that the same run already observed.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	bad := validationHistory(t, taskID(1), generationID(1), 140, 1)
	bad[0].State.Task.Title = "tampered"
	good := validationHistory(t, taskID(2), generationID(2), 150, 1)
	source := &validatorSource{headLists: [][]gitstore.TaskHead{
		{{TaskID: taskID(1), ObjectID: bad[0].ObjectID}, {TaskID: taskID(2), ObjectID: good[0].ObjectID}},
		{{TaskID: taskID(1), ObjectID: bad[0].ObjectID}, {TaskID: taskID(2), ObjectID: "raced-head"}},
	}, histories: map[string]gitstore.TaskHistoryResult{
		taskID(1): historyResult(taskID(1), bad[0].ObjectID, false, bad),
		taskID(2): historyResult(taskID(2), good[0].ObjectID, false, good),
	}}
	got, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, false)
	if core.CategoryOf(err) != core.CategoryCorruptData || got.Invalid != 1 || got.Pending != 1 {
		t.Fatalf("combined corrupt/race result = %#v, error = %v; want corrupt-data before stale-write", got, err)
	}
}

func TestValidateCachesFiveChangedHeadsAcrossFiveHundredTasks(t *testing.T) {
	// Production mutation: replaying unchanged completed histories defeats the bounded 500-task cache contract.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	source := &validatorSource{histories: map[string]gitstore.TaskHistoryResult{}}
	initial := make([]gitstore.TaskHead, 0, 500)
	histories := make(map[string][]gitstore.HistoryCommit, 500)
	for index := 0; index < 500; index++ {
		id := "WB-" + validatorULID(1000+index)
		history := validationHistory(t, id, validatorULID(2000+index), 3000+index*3, 1)
		histories[id] = history
		initial = append(initial, gitstore.TaskHead{TaskID: id, ObjectID: history[0].ObjectID})
		source.histories[id] = historyResult(id, history[0].ObjectID, false, history)
	}
	source.heads = initial
	v := &Validator{source: source, cache: cache, config: testConfig()}
	if _, err := v.Validate(ctx, false); err != nil {
		t.Fatal(err)
	}
	changed := append([]gitstore.TaskHead(nil), initial...)
	for index := 0; index < 5; index++ {
		id := changed[index].TaskID
		history := append(histories[id], validationHistory(t, id, histories[id][0].State.History.Generation, 5000+index*3, 2)[1])
		// The second record must retain the real root parent and operation stream.
		history[1].Parents = []string{history[0].ObjectID}
		histories[id] = history
		changed[index].ObjectID = history[1].ObjectID
		source.histories[id] = historyResult(id, history[1].ObjectID, true, history[1:])
	}
	source.heads = changed
	got, err := v.Validate(ctx, false)
	if err != nil || got.TasksChecked != 5 || got.CommitsChecked != 5 || got.CacheHits != 495 || got.Valid != 500 || got.Pending != 0 {
		t.Fatalf("500-task incremental result = %#v, error = %v; want 5 tasks, 5 commits, 495 hits", got, err)
	}
}

func TestValidateRecordsEachTaskAtItsBoundaryBeforeReadingTheNext(t *testing.T) {
	// Production mutation: collecting every task's history before folding any of
	// them restores the peak memory that grows with the corpus. The observable
	// consequence is that no completion reaches the cache until the last task's
	// commits have been read.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	first := validationHistory(t, taskID(1), generationID(1), 160, 3)
	second := validationHistory(t, taskID(2), generationID(2), 170, 3)
	third := validationHistory(t, taskID(3), generationID(3), 180, 3)
	source := &validatorSource{
		heads: headsFor(first, second, third),
		histories: map[string]gitstore.TaskHistoryResult{
			taskID(1): historyResult(taskID(1), first[2].ObjectID, false, first),
			taskID(2): historyResult(taskID(2), second[2].ObjectID, false, second),
			taskID(3): historyResult(taskID(3), third[2].ObjectID, false, third),
		},
	}
	order := []string{taskID(1), taskID(2), taskID(3)}
	position := map[string]int{taskID(1): 0, taskID(2): 1, taskID(3): 2}
	source.observeCommit = func(id string, _ gitstore.HistoryCommit) {
		recorded, err := cache.Snapshot(ctx, order)
		if err != nil {
			t.Errorf("Snapshot() error = %v", err)
			return
		}
		completed := 0
		for _, task := range recorded {
			if task.Status == StatusValid {
				completed++
			}
		}
		if completed != position[id] {
			t.Errorf("while reading %s the cache held %d completed task(s), want %d recorded at earlier task boundaries", id, completed, position[id])
		}
	}
	got, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, true)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got.TasksChecked != 3 || got.CommitsChecked != 9 || got.Valid != 3 {
		t.Fatalf("streamed result = %#v, want 3 tasks, 9 commits, 3 valid", got)
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
		"01K0M6B8A4FTT8C39MXXYTW7G1",
		"01K0M6B8A4FTT8C39MXXYTW7H1",
		"01K0M6B8A4FTT8C39MXXYTW7J1",
		"01K0M6B8A4FTT8C39MXXYTW7K1",
		"01K0M6B8A4FTT8C39MXXYTW7M1",
	}
	index := 0
	newID := core.IDSourceFunc(func() (string, error) { value := ids[index]; index++; return value, nil })
	config, _, err := repo.Init(ctx, "WB", newID)
	if err != nil {
		t.Fatal(err)
	}
	service := core.Service{Config: config, Reader: repo, Writer: repo, IDs: newID, Now: func() time.Time { return time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC) }, Actor: "validator@example.test"}
	first, err := service.CreateMutation(ctx, core.CreateInput{Title: "first immutable history"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateMutation(ctx, core.CreateInput{Title: "second immutable history"})
	if err != nil {
		t.Fatal(err)
	}
	firstTitle := "first updated history"
	if _, err := service.UpdateMutation(ctx, first.Task.ID, core.UpdateInput{Title: &firstTitle}); err != nil {
		t.Fatal(err)
	}
	secondTitle := "second updated history"
	if _, err := service.UpdateMutation(ctx, second.Task.ID, core.UpdateInput{Title: &secondTitle}); err != nil {
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
	result, err := v.Validate(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskCount != 2 || result.CommitsChecked != 4 || result.Valid != 2 {
		t.Fatalf("real Git validation result = %#v, want two two-commit histories", result)
	}
	after, err := repo.Git(ctx, nil, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/workbook/tasks/")
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("canonical refs after validation = %q, want unchanged %q", after, before)
	}
}

func TestValidateReportsARepeatedOperationIDAsCorruptData(t *testing.T) {
	// Production mutation: checking each checkpoint alone vouches for a chain the
	// projection's operation-ID key cannot hold, so `validate --full` reports
	// VALID for a repository every projecting command already refuses.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	history := validationHistory(t, taskID(1), generationID(1), 140, 3)
	// Every checkpoint still folds: a repeated ULID changes no projected state,
	// which is exactly why checkpoint comparison cannot see it.
	history[2].Operation.Operations[0].ID = history[0].Operation.Operations[0].ID
	source := &validatorSource{heads: headsFor(history), histories: map[string]gitstore.TaskHistoryResult{
		taskID(1): historyResult(taskID(1), history[2].ObjectID, false, history),
	}}

	got, err := (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, true)
	if category := core.CategoryOf(err); category != core.CategoryCorruptData {
		t.Fatalf("Validate() category = %q, want corrupt-data; error = %v", category, err)
	}
	if got.Valid != 0 || got.Invalid != 1 || len(got.Failures) != 1 {
		t.Fatalf("duplicate-ULID result = %#v, want one invalid task", got)
	}
	failure := got.Failures[0]
	if failure.Commit != history[2].ObjectID || failure.Category != string(core.CategoryCorruptData) {
		t.Fatalf("failure = %#v, want corrupt data at the repeating commit", failure)
	}
	if !strings.Contains(failure.Message, history[0].Operation.Operations[0].ID) ||
		!strings.Contains(failure.Message, history[0].ObjectID) {
		t.Fatalf("failure message = %q, want the repeated operation and its first commit named", failure.Message)
	}
}

func TestValidateReportsARepeatedOperationIDAcrossACachedBoundary(t *testing.T) {
	// Production mutation: keeping the seen-operation set only for the commits one
	// run reads. The incremental run resumes at the cached boundary, so a new
	// commit repeating a ULID from the already-validated prefix passes unseen and
	// the default `workbook validate` reports VALID.
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	history := validationHistory(t, taskID(1), generationID(1), 150, 3)
	source := &validatorSource{
		heads:     []gitstore.TaskHead{{TaskID: taskID(1), ObjectID: history[1].ObjectID}},
		histories: map[string]gitstore.TaskHistoryResult{taskID(1): historyResult(taskID(1), history[1].ObjectID, false, history[:2])},
	}
	v := &Validator{source: source, cache: cache, config: testConfig()}
	if _, err := v.Validate(ctx, false); err != nil {
		t.Fatalf("first Validate() error = %v", err)
	}

	history[2].Operation.Operations[0].ID = history[0].Operation.Operations[0].ID
	source.heads = []gitstore.TaskHead{{TaskID: taskID(1), ObjectID: history[2].ObjectID}}
	source.histories[taskID(1)] = historyResult(taskID(1), history[2].ObjectID, true, history[2:])

	got, err := v.Validate(ctx, false)
	if category := core.CategoryOf(err); category != core.CategoryCorruptData {
		t.Fatalf("incremental Validate() category = %q, want corrupt-data; error = %v", category, err)
	}
	if got.Invalid != 1 || len(got.Failures) != 1 || got.Failures[0].Commit != history[2].ObjectID {
		t.Fatalf("incremental result = %#v, want the appended commit reported invalid", got)
	}
	if !strings.Contains(got.Failures[0].Message, history[0].Operation.Operations[0].ID) {
		t.Fatalf("failure message = %q, want the repeated operation named", got.Failures[0].Message)
	}
}

type validatorSource struct {
	heads             []gitstore.TaskHead
	headLists         [][]gitstore.TaskHead
	historyIndex      int
	histories         map[string]gitstore.TaskHistoryResult
	lastRequests      []gitstore.TaskHistoryRequest
	reads             int
	cancelOnRead      context.CancelFunc
	cancelOnListCall  int
	cancel            context.CancelFunc
	historyForRequest func(gitstore.TaskHistoryRequest) gitstore.TaskHistoryResult
	observeCommit     func(string, gitstore.HistoryCommit)
}

func (s *validatorSource) ListTaskHeads(_ context.Context, _ core.ProjectConfig) ([]gitstore.TaskHead, error) {
	s.historyIndex++
	if len(s.headLists) > 0 {
		index := s.historyIndex - 1
		if index >= len(s.headLists) {
			index = len(s.headLists) - 1
		}
		heads := append([]gitstore.TaskHead(nil), s.headLists[index]...)
		if s.cancel != nil && s.cancelOnListCall == s.historyIndex {
			s.cancel()
		}
		return heads, nil
	}
	heads := append([]gitstore.TaskHead(nil), s.heads...)
	if s.cancel != nil && s.cancelOnListCall == s.historyIndex {
		s.cancel()
	}
	return heads, nil
}

func (s *validatorSource) ReadTaskHistoriesStream(
	_ context.Context,
	_ core.ProjectConfig,
	requests []gitstore.TaskHistoryRequest,
	stream gitstore.TaskHistoryStream,
) error {
	s.reads++
	s.lastRequests = append([]gitstore.TaskHistoryRequest(nil), requests...)
	for _, request := range requests {
		history := s.histories[request.Head.TaskID]
		if s.historyForRequest != nil {
			history = s.historyForRequest(request)
		}
		if err := stream.Begin(gitstore.TaskHistoryStart{
			TaskID:          history.TaskID,
			Head:            history.Head,
			BoundaryReached: history.BoundaryReached,
		}); err != nil {
			return err
		}
		for _, commit := range history.Commits {
			if s.observeCommit != nil {
				s.observeCommit(history.TaskID, commit)
			}
			if err := stream.Commit(history.TaskID, commit); err != nil {
				return err
			}
		}
		summary := history
		summary.Commits = nil
		if err := stream.End(summary); err != nil {
			return err
		}
	}
	if s.cancelOnRead != nil {
		s.cancelOnRead()
		s.cancelOnRead = nil
	}
	return nil
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

func seedPreparedValid(t *testing.T, ctx context.Context, cache *Cache, head gitstore.TaskHead, generation string) {
	t.Helper()
	if _, err := cache.Prepare(ctx, []gitstore.TaskHead{head}, false); err != nil {
		t.Fatal(err)
	}
	if err := cache.Record(ctx, Completion{
		TaskID:               head.TaskID,
		ObservedHead:         head.ObjectID,
		Status:               StatusValid,
		LastValidCommit:      "cached-" + head.TaskID,
		LastValidGeneration:  generation,
		LastValidState:       canonicalState(t, head.TaskID, generation, "cached"),
		ValidatedCommitIDs:   []string{"cached-" + head.TaskID},
		ValidatedCommitCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
}
