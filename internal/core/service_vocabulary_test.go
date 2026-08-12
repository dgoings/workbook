package core

import (
	"context"
	"reflect"
	"testing"
)

// customVocabulary is a project that renamed a status, removed another, and
// assigned the three roles to names Workbook has never shipped. It exercises
// every accessor Service reads.
func customVocabulary(t *testing.T) Vocabulary {
	t.Helper()
	vocabulary, err := NewVocabulary(
		[]StatusDefinition{
			{Status: "triage", Label: "Triage", Rank: "1/1", Tags: []StatusTag{StatusTagDefault}},
			{Status: "queued", Label: "Queued", Rank: "2/1", Tags: []StatusTag{StatusTagNext}},
			{Status: "released", Label: "Released", Rank: "3/1", Tags: []StatusTag{StatusTagDone}},
		},
		[]StatusAlias{{From: "shipped", To: "released"}},
		[]RetiredStatus{{Status: "blocked", Destination: "triage"}},
	)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	return vocabulary
}

func vocabularyServiceUnderTest(store *memoryTaskStore, ids IDSource, vocabulary Vocabulary) Service {
	service := serviceUnderTest(store, ids)
	service.Vocabulary = vocabulary
	return service
}

// Every construction of a Service that predates per-project statuses leaves the
// field zero, and every one of them has to keep behaving exactly as it did.
func TestServiceWithNoConfiguredVocabularyUsesTheBuiltInOne(t *testing.T) {
	service := serviceUnderTest(newMemoryTaskStore(), &sequenceIDSource{})
	if got, want := service.vocabulary().Document(), DefaultVocabulary().Document(); !reflect.DeepEqual(got, want) {
		t.Fatalf("vocabulary() = %#v, want the built-in default %#v", got, want)
	}
}

func TestServiceCreateDefaultsToTheConfiguredDefaultStatus(t *testing.T) {
	store := newMemoryTaskStore()
	ids := &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7D2",
		"01K0M6B8A4FTT8C39MXXYTW7D3",
		"01K0M6B8A4FTT8C39MXXYTW7D4",
	}}
	service := vocabularyServiceUnderTest(store, ids, customVocabulary(t))

	result, err := service.CreateMutation(context.Background(), CreateInput{Title: "Task"})
	if err != nil {
		t.Fatalf("CreateMutation() error = %v", err)
	}
	if got, want := result.Task.Status, Status("triage"); got != want {
		t.Fatalf("CreateMutation() status = %q, want %q", got, want)
	}
}

// The membership check is the mutation boundary's job now, and it has to report
// the same category and message it did when NormalizeTask owned it.
func TestServiceMutationsRejectAStatusTheProjectDoesNotDefine(t *testing.T) {
	backlog := StatusBacklog
	tests := map[string]func(Service) error{
		"create": func(service Service) error {
			_, err := service.CreateMutation(context.Background(), CreateInput{Title: "Task", Status: backlog})
			return err
		},
		"update": func(service Service) error {
			_, err := service.UpdateMutation(
				context.Background(),
				"WB-01K0M6B8A4FTT8C39MXXYTW7F1",
				UpdateInput{Status: &backlog},
			)
			return err
		},
		"place": func(service Service) error {
			_, err := service.PlaceMutation(
				context.Background(),
				"WB-01K0M6B8A4FTT8C39MXXYTW7F1",
				PlaceInput{Status: backlog},
			)
			return err
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			store := newMemoryTaskStore(serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
				Title: "Task", Status: "triage", Priority: PriorityMedium, Rank: "1/1",
			}))
			service := vocabularyServiceUnderTest(store, &sequenceIDSource{}, customVocabulary(t))

			err := mutate(service)
			if err == nil {
				t.Fatal("mutation error = nil, want a rejection")
			}
			if got := CategoryOf(err); got != CategoryValidation {
				t.Fatalf("mutation category = %q, want %q", got, CategoryValidation)
			}
			if got, want := err.Error(), `invalid task status "backlog"`; got != want {
				t.Fatalf("mutation error = %q, want %q", got, want)
			}
			if got := len(store.writes); got != 0 {
				t.Fatalf("mutation wrote %d packs, want none", got)
			}
		})
	}
}

// The filter accepts a status the project does not define and reports what it
// did with it. This is the relaxation PR-B deferred: a filter authors nothing,
// so refusing one buys nothing, and the empty result it produces is only honest
// because ResolveStatusFilter lets the caller say why it is empty.
func TestServiceListAcceptsAStatusOutsideTheVocabularyAndReportsIt(t *testing.T) {
	store := newMemoryTaskStore(serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Task", Status: "triage", Priority: PriorityMedium, Rank: "1/1",
	}))
	service := vocabularyServiceUnderTest(store, &sequenceIDSource{}, customVocabulary(t))

	for _, filtered := range []Status{"awaiting-review", "backlog", "Awaiting Review"} {
		status := filtered
		tasks, err := service.List(context.Background(), ListFilter{Status: &status})
		if err != nil {
			t.Fatalf("List(%q) error = %v, want the filter accepted", status, err)
		}
		if len(tasks) != 0 {
			t.Fatalf("List(%q) returned %d tasks, want none", status, len(tasks))
		}
		resolution := service.ResolveStatusFilter(status)
		if resolution.Known {
			t.Fatalf("ResolveStatusFilter(%q) = %#v, want an unknown status", status, resolution)
		}
	}

	// A status the project does define still filters.
	live := Status("triage")
	tasks, err := service.List(context.Background(), ListFilter{Status: &live})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got := len(tasks); got != 1 {
		t.Fatalf("List() returned %d tasks, want 1", got)
	}
	if resolution := service.ResolveStatusFilter(live); !resolution.Known || resolution.Forwarded {
		t.Fatalf("ResolveStatusFilter(%q) = %#v, want a live status", live, resolution)
	}
}

// A filter naming a value that still resolves selects the status it resolves
// to. Answering "no tasks are in shipped" about a project that merely renamed
// the column is the one answer that is actually wrong.
func TestServiceListResolvesAStoredFilterThroughTheChains(t *testing.T) {
	store := newMemoryTaskStore(serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Task", Status: "released", Priority: PriorityMedium, Rank: "1/1",
	}))
	service := vocabularyServiceUnderTest(store, &sequenceIDSource{}, customVocabulary(t))

	renamed := Status("shipped")
	tasks, err := service.List(context.Background(), ListFilter{Status: &renamed})
	if err != nil {
		t.Fatalf("List(%q) error = %v", renamed, err)
	}
	if len(tasks) != 1 {
		t.Fatalf("List(%q) returned %d tasks, want the renamed status's one task", renamed, len(tasks))
	}
	resolution := service.ResolveStatusFilter(renamed)
	if !resolution.Known || !resolution.Forwarded ||
		resolution.Resolved != "released" || resolution.Operation != ConfigStatusRename {
		t.Fatalf("ResolveStatusFilter(%q) = %#v, want a rename forwarded to released", renamed, resolution)
	}

	removed := Status("blocked")
	if resolution := service.ResolveStatusFilter(removed); !resolution.Forwarded ||
		resolution.Resolved != "triage" || resolution.Operation != ConfigStatusRemove {
		t.Fatalf("ResolveStatusFilter(%q) = %#v, want a removal forwarded to triage", removed, resolution)
	}
}

func TestServiceProjectResolvesStoredStatusesAndReportsTheStoredValue(t *testing.T) {
	store := newMemoryTaskStore(
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
			Title: "Renamed", Status: "shipped", Priority: PriorityMedium, Rank: "1/1",
		}),
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
			Title: "Retired", Status: "blocked", Priority: PriorityMedium, Rank: "2/1",
		}),
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F3", TaskData{
			Title: "Live", Status: "queued", Priority: PriorityMedium, Rank: "3/1",
		}),
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F4", TaskData{
			Title: "Stranded", Status: "awaiting-review", Priority: PriorityMedium, Rank: "4/1",
		}),
	)
	service := vocabularyServiceUnderTest(store, &sequenceIDSource{}, customVocabulary(t))

	tasks, err := service.List(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	byID := make(map[string]Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
	}

	tests := map[string]struct {
		status Status
		stored Status
	}{
		"WB-01K0M6B8A4FTT8C39MXXYTW7F1": {status: "released", stored: "shipped"},
		"WB-01K0M6B8A4FTT8C39MXXYTW7F2": {status: "triage", stored: "blocked"},
		"WB-01K0M6B8A4FTT8C39MXXYTW7F3": {status: "queued"},
		// A status no chain reaches is reported as it was stored, with no
		// correction claimed: the board shows it stranded rather than guessing.
		"WB-01K0M6B8A4FTT8C39MXXYTW7F4": {status: "awaiting-review"},
	}
	for id, want := range tests {
		task := byID[id]
		if task.Status != want.status || task.StoredStatus != want.stored {
			t.Errorf(
				"%s: status = %q, storedStatus = %q, want %q and %q",
				id, task.Status, task.StoredStatus, want.status, want.stored,
			)
		}
	}
}

// A task nobody touches keeps resolving forever. The moment something writes to
// one, the write settles the stored value too.
func TestServiceUpdateCorrectsAStaleStoredStatusOnTouch(t *testing.T) {
	parent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Task", Status: "shipped", Priority: PriorityMedium, Rank: "1/1",
	})
	store := newMemoryTaskStore(parent)
	ids := &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7E1",
		"01K0M6B8A4FTT8C39MXXYTW7E2",
	}}
	service := vocabularyServiceUnderTest(store, ids, customVocabulary(t))

	title := "Retitled"
	result, err := service.UpdateMutation(
		context.Background(),
		parent.State.TaskID,
		UpdateInput{Title: &title},
	)
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	if result.StatusCorrected == nil {
		t.Fatal("UpdateMutation() reported no status correction, want one")
	}
	if got, want := *result.StatusCorrected, (StatusCorrection{From: "shipped", To: "released"}); got != want {
		t.Fatalf("StatusCorrected = %#v, want %#v", got, want)
	}
	// A correction is not a warning: nothing went wrong.
	if got := len(result.Warnings); got != 0 {
		t.Fatalf("UpdateMutation() warnings = %d, want none", got)
	}

	if got, want := len(store.writes), 1; got != want {
		t.Fatalf("Write() calls = %d, want %d", got, want)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E1", Type: OperationFieldSet, Field: "title", Value: "Retitled"},
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E2", Type: OperationFieldSet, Field: "status", Value: "released"},
	})
	if got, want := store.writes[0].state.Task.Status, Status("released"); got != want {
		t.Fatalf("written status = %q, want %q", got, want)
	}
}

// A caller that named a status meant it. The correction must not append a
// second, contradicting operation.
func TestServiceUpdateDoesNotCorrectWhenTheCallerSetTheStatus(t *testing.T) {
	parent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Task", Status: "shipped", Priority: PriorityMedium, Rank: "1/1",
	})
	store := newMemoryTaskStore(parent)
	ids := &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E1"}}
	service := vocabularyServiceUnderTest(store, ids, customVocabulary(t))

	status := Status("triage")
	result, err := service.UpdateMutation(
		context.Background(),
		parent.State.TaskID,
		UpdateInput{Status: &status},
	)
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	if result.StatusCorrected != nil {
		t.Fatalf("StatusCorrected = %#v, want none", result.StatusCorrected)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E1", Type: OperationFieldSet, Field: "status", Value: "triage"},
	})
}

// A tombstoned task's ref must not gain edits, and an update refuses one before
// any correction could be appended.
func TestServiceUpdateNeverCorrectsATombstonedTask(t *testing.T) {
	parent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Task", Status: "shipped", Priority: PriorityMedium, Rank: "1/1", Deleted: true,
	})
	store := newMemoryTaskStore(parent)
	service := vocabularyServiceUnderTest(store, &sequenceIDSource{}, customVocabulary(t))

	title := "Retitled"
	if _, err := service.UpdateMutation(context.Background(), parent.State.TaskID, UpdateInput{Title: &title}); err == nil {
		t.Fatal("UpdateMutation() error = nil, want a rejection")
	}
	if got := len(store.writes); got != 0 {
		t.Fatalf("UpdateMutation() wrote %d packs, want none", got)
	}
}

// A stored status whose chain does not terminate at a live status has no answer
// to write, so the write leaves it alone rather than guessing.
func TestServiceUpdateLeavesAnUnresolvableStatusAlone(t *testing.T) {
	parent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Task", Status: "awaiting-review", Priority: PriorityMedium, Rank: "1/1",
	})
	store := newMemoryTaskStore(parent)
	ids := &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E1"}}
	service := vocabularyServiceUnderTest(store, ids, customVocabulary(t))

	title := "Retitled"
	result, err := service.UpdateMutation(context.Background(), parent.State.TaskID, UpdateInput{Title: &title})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	if result.StatusCorrected != nil {
		t.Fatalf("StatusCorrected = %#v, want none", result.StatusCorrected)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E1", Type: OperationFieldSet, Field: "title", Value: "Retitled"},
	})
}

// An update that changes nothing must stay a rejection. A correction rides
// along with a write; it does not create one.
func TestServiceUpdateThatChangesNothingStillFailsDespiteAStaleStatus(t *testing.T) {
	parent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Task", Status: "shipped", Priority: PriorityMedium, Rank: "1/1",
	})
	store := newMemoryTaskStore(parent)
	service := vocabularyServiceUnderTest(store, &sequenceIDSource{}, customVocabulary(t))

	title := "Task"
	if _, err := service.UpdateMutation(context.Background(), parent.State.TaskID, UpdateInput{Title: &title}); err == nil {
		t.Fatal("UpdateMutation() error = nil, want the unchanged-update rejection")
	}
	if got := len(store.writes); got != 0 {
		t.Fatalf("UpdateMutation() wrote %d packs, want none", got)
	}
}

// Placing a task into the status its stale token already resolves to writes the
// settlement and reports it as a correction rather than as a move.
func TestServicePlaceReportsASettlementAsACorrection(t *testing.T) {
	parent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Task", Status: "shipped", Priority: PriorityMedium, Rank: "1/1",
	})
	store := newMemoryTaskStore(parent)
	ids := &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E1"}}
	service := vocabularyServiceUnderTest(store, ids, customVocabulary(t))

	result, err := service.PlaceMutation(
		context.Background(),
		parent.State.TaskID,
		PlaceInput{Status: "released"},
	)
	if err != nil {
		t.Fatalf("PlaceMutation() error = %v", err)
	}
	if result.StatusCorrected == nil {
		t.Fatal("PlaceMutation() reported no status correction, want one")
	}
	if got, want := *result.StatusCorrected, (StatusCorrection{From: "shipped", To: "released"}); got != want {
		t.Fatalf("StatusCorrected = %#v, want %#v", got, want)
	}
	assertOperations(t, store.writes[0].pack.Operations, []Operation{
		{ID: "01K0M6B8A4FTT8C39MXXYTW7E1", Type: OperationFieldSet, Field: "status", Value: "released"},
	})
}

// A genuine move is not a correction, however stale the token it started from.
func TestServicePlaceIntoADifferentStatusIsNotACorrection(t *testing.T) {
	parent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Task", Status: "shipped", Priority: PriorityMedium, Rank: "1/1",
	})
	store := newMemoryTaskStore(parent)
	ids := &sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E1"}}
	service := vocabularyServiceUnderTest(store, ids, customVocabulary(t))

	result, err := service.PlaceMutation(
		context.Background(),
		parent.State.TaskID,
		PlaceInput{Status: "triage"},
	)
	if err != nil {
		t.Fatalf("PlaceMutation() error = %v", err)
	}
	if result.StatusCorrected != nil {
		t.Fatalf("StatusCorrected = %#v, want none", result.StatusCorrected)
	}
}

// TestMoveAndPlaceAgreeAboutAStaleAnchor closes the gap PR-A pinned as a
// deliberate failure, and keeps it closed.
//
// Both mutations ask whether two tasks share a bucket, and both used to ask it
// of the stored status. That was unobservable while every project used the
// built-in vocabulary, because no two tokens could resolve to one column. It
// became ordinary the moment a project could rename one: a task stored under
// "shipped" and a task stored under "released" are drawn in the same column,
// and the two commands disagreed about whether they were neighbours — Move
// refused, Place did the same reordering and succeeded.
//
// The bucket is the resolved status now, in the anchor checks and in the rank
// walk alike, which is the same answer the board draws from. So the two verbs
// accept the same anchor and compute the same rank for it.
func TestMoveAndPlaceAgreeAboutAStaleAnchor(t *testing.T) {
	stale := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Stored under the old name", Status: "shipped", Priority: PriorityMedium, Rank: "3/1",
	})
	live := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
		Title: "Stored under the new name", Status: "released", Priority: PriorityMedium, Rank: "2/1",
	})
	vocabulary := customVocabulary(t)

	// Both resolve into the same column, which is what makes a disagreement
	// visible to a person in the first place.
	service := vocabularyServiceUnderTest(newMemoryTaskStore(stale, live), &sequenceIDSource{
		values: []string{"01K0M6B8A4FTT8C39MXXYTW7E1", "01K0M6B8A4FTT8C39MXXYTW7E2"},
	}, vocabulary)
	if got := service.Project(stale).Status; got != "released" {
		t.Fatalf("stale task resolved status = %q, want released", got)
	}
	if got := service.Project(live).Status; got != "released" {
		t.Fatalf("live task resolved status = %q, want released", got)
	}

	// The mover carries the stale token and the anchor carries the live one.
	moved, moveErr := service.MoveMutation(context.Background(), stale.State.TaskID, MoveInput{Before: live.State.TaskID})
	if moveErr != nil {
		t.Fatalf("MoveMutation() error = %v, want the move against a resolved-equal anchor to be accepted", moveErr)
	}
	// The write settles the stale token on its way past, which is what every
	// other mutation that touches a task does.
	if got, want := moved.Task.Status, Status("released"); got != want {
		t.Fatalf("MoveMutation() status = %q, want %q", got, want)
	}
	if moved.StatusCorrected == nil || moved.StatusCorrected.From != "shipped" || moved.StatusCorrected.To != "released" {
		t.Fatalf("MoveMutation() StatusCorrected = %#v, want shipped settled into released", moved.StatusCorrected)
	}

	placed := vocabularyServiceUnderTest(newMemoryTaskStore(stale, live), &sequenceIDSource{
		values: []string{"01K0M6B8A4FTT8C39MXXYTW7E3", "01K0M6B8A4FTT8C39MXXYTW7E4"},
	}, vocabulary)
	placement, placeErr := placed.PlaceMutation(context.Background(), stale.State.TaskID, PlaceInput{
		Status: "released",
		Before: live.State.TaskID,
	})
	if placeErr != nil {
		t.Fatalf("PlaceMutation() error = %v", placeErr)
	}
	// The same reordering, so the same rank: agreeing about the anchor is worth
	// nothing if the two walks still measure different neighbours.
	if moved.Task.Rank != placement.Task.Rank {
		t.Fatalf("move ranked the task %q and place ranked it %q", moved.Task.Rank, placement.Task.Rank)
	}
}

// The other direction of the same pair: the anchor is the one holding the live
// token, and the mover holds the stale one. Both verbs refuse an anchor that
// resolves somewhere else, so the check is a bucket comparison rather than a
// relaxation that accepts anything.
func TestMoveRefusesAnAnchorThatResolvesToAnotherColumn(t *testing.T) {
	mover := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Released", Status: "shipped", Priority: PriorityMedium, Rank: "1/1",
	})
	// "blocked" was removed into "triage", so this one is drawn in a different
	// column from the mover no matter which token either of them stores.
	elsewhere := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
		Title: "Triaged", Status: "blocked", Priority: PriorityMedium, Rank: "2/1",
	})
	service := vocabularyServiceUnderTest(
		newMemoryTaskStore(mover, elsewhere),
		&sequenceIDSource{values: []string{"01K0M6B8A4FTT8C39MXXYTW7E1"}},
		customVocabulary(t),
	)

	_, err := service.MoveMutation(context.Background(), mover.State.TaskID, MoveInput{Before: elsewhere.State.TaskID})
	if err == nil {
		t.Fatal("MoveMutation() = nil error, want a refusal across two columns")
	}
	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("MoveMutation() category = %q, want %q; error = %v", got, want, err)
	}
}

// Appending to a column ranks against everything drawn in it, including the
// tasks still stored under a token a rename replaced. Ranking against only the
// live token would put the new task on top of a neighbour it shares a column
// with — two tasks at the same rank, in the same visible column, from a create
// that did nothing unusual.
func TestCreateRanksAgainstTheResolvedColumn(t *testing.T) {
	stale := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Stored under the old name", Status: "shipped", Priority: PriorityMedium, Rank: "7/1",
	})
	service := vocabularyServiceUnderTest(newMemoryTaskStore(stale), &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7E1",
		"01K0M6B8A4FTT8C39MXXYTW7E2",
		"01K0M6B8A4FTT8C39MXXYTW7E3",
	}}, customVocabulary(t))

	result, err := service.CreateMutation(context.Background(), CreateInput{
		Title: "Appended", Status: "released", Priority: PriorityMedium,
	})
	if err != nil {
		t.Fatalf("CreateMutation() error = %v", err)
	}
	if got, want := result.Task.Rank, "8/1"; got != want {
		t.Fatalf("CreateMutation() rank = %q, want %q: one past the stale-token task already in that column", got, want)
	}
}

// `workbook next` asks the vocabulary which statuses are eligible, and asks it
// of the resolved value, so a task still carrying a renamed token competes.
func TestServiceNextReadsTheNextTagThroughResolution(t *testing.T) {
	store := newMemoryTaskStore(
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
			Title: "Not eligible", Status: "triage", Priority: PriorityHigh, Rank: "1/1",
		}),
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
			Title: "Eligible", Status: "queued", Priority: PriorityMedium, Rank: "2/1",
		}),
	)
	service := vocabularyServiceUnderTest(store, &sequenceIDSource{}, customVocabulary(t))

	selected, err := service.Next(context.Background())
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if selected == nil {
		t.Fatal("Next() = nil, want the queued task")
	}
	if got, want := selected.ID, "WB-01K0M6B8A4FTT8C39MXXYTW7F2"; got != want {
		t.Fatalf("Next() task = %q, want %q", got, want)
	}
}

func TestServiceNextReadsTheDoneTagThroughResolution(t *testing.T) {
	dependency := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		// Stored under the pre-rename token, which resolves to the status the
		// project tagged done.
		Title: "Dependency", Status: "shipped", Priority: PriorityMedium, Rank: "1/1",
	})
	waiting := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
		Title: "Waiting", Status: "queued", Priority: PriorityMedium, Rank: "2/1",
		Dependencies: []string{"WB-01K0M6B8A4FTT8C39MXXYTW7F1"},
	})
	service := vocabularyServiceUnderTest(newMemoryTaskStore(dependency, waiting), &sequenceIDSource{}, customVocabulary(t))

	selected, err := service.Next(context.Background())
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if selected == nil || selected.ID != waiting.State.TaskID {
		t.Fatalf("Next() = %v, want the waiting task to be eligible", selected)
	}

	// The same dependency in a status with no done tag blocks it.
	blocked := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Dependency", Status: "triage", Priority: PriorityMedium, Rank: "1/1",
	})
	service = vocabularyServiceUnderTest(newMemoryTaskStore(blocked, waiting), &sequenceIDSource{}, customVocabulary(t))
	selected, err = service.Next(context.Background())
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if selected != nil {
		t.Fatalf("Next() = %v, want nil while the dependency is unfinished", selected)
	}
}

// List orders by the vocabulary's rank order, not by a hard-coded array.
func TestServiceListOrdersByTheConfiguredVocabulary(t *testing.T) {
	store := newMemoryTaskStore(
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
			Title: "Released", Status: "released", Priority: PriorityMedium, Rank: "1/1",
		}),
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
			Title: "Triage", Status: "triage", Priority: PriorityMedium, Rank: "1/1",
		}),
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F3", TaskData{
			Title: "Stranded", Status: "awaiting-review", Priority: PriorityMedium, Rank: "1/1",
		}),
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F4", TaskData{
			Title: "Queued", Status: "queued", Priority: PriorityMedium, Rank: "1/1",
		}),
	)
	service := vocabularyServiceUnderTest(store, &sequenceIDSource{}, customVocabulary(t))

	tasks, err := service.List(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	got := make([]string, len(tasks))
	for index, task := range tasks {
		got[index] = task.Title
	}
	// A status the vocabulary does not define sorts after every one it does.
	want := []string{"Triage", "Queued", "Released", "Stranded"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() order = %v, want %v", got, want)
	}
}
