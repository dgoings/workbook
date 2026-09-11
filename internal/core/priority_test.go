package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The built-in set is today's three, most urgent first, with medium carrying
// the default tag the service used to hardcode.
func TestBuiltInPrioritiesAreTodaysThree(t *testing.T) {
	definitions := builtInPriorityDefinitions()

	var names []Priority
	for _, definition := range definitions {
		names = append(names, definition.Priority)
	}
	want := []Priority{PriorityHigh, PriorityMedium, PriorityLow}
	if len(names) != len(want) {
		t.Fatalf("built-in priorities = %v, want %v", names, want)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("built-in priorities = %v, want %v (most urgent first)", names, want)
		}
	}
	for _, definition := range definitions {
		if definition.Priority == PriorityMedium && !definition.HasTag(PriorityTagDefault) {
			t.Error("medium does not carry the default tag, so a new task has no priority to land on")
		}
	}
}

// Color is omitted when unset, so a definition that chose no color encodes to
// the same bytes it would have before the field existed. The "tags":null this
// pins is a bare literal marshaled directly, without passing through
// normalization — normalization always produces a non-nil slice, so no
// production path can ever emit these bytes; that does not make them wrong to
// pin here, only unreachable elsewhere.
func TestPriorityDefinitionOmitsAnUnsetColor(t *testing.T) {
	encoded, err := json.Marshal(PriorityDefinition{Priority: PriorityHigh, Label: "High", Rank: "1/1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(encoded); got != `{"priority":"high","label":"High","rank":"1/1","tags":null}` {
		t.Errorf("encoded = %s; an unset color must not appear", got)
	}
}

// The zero value means "this caller did not configure one", and reads as the
// built-in set — which is what lets every construction predating per-project
// priorities keep its behavior without being edited.
func TestZeroPriorityVocabularyReadsAsBuiltIn(t *testing.T) {
	var vocabulary PriorityVocabulary

	if !vocabulary.IsZero() {
		t.Fatal("the zero value does not report itself as unconfigured")
	}
	if got := vocabulary.Default(); got != PriorityMedium {
		t.Errorf("zero vocabulary Default() = %q, want medium", got)
	}
	if vocabulary.Order(PriorityHigh) >= vocabulary.Order(PriorityLow) {
		t.Error("high does not sort ahead of low in the built-in order")
	}
}

// A renamed priority keeps resolving, which is what stops a rename from
// rewriting task history.
func TestPriorityVocabularyResolvesARename(t *testing.T) {
	vocabulary, err := NewPriorityVocabulary(
		[]PriorityDefinition{{Priority: "critical", Label: "Critical", Rank: "1/1", Tags: []PriorityTag{PriorityTagDefault}}},
		[]PriorityAlias{{From: PriorityHigh, To: "critical"}},
		nil,
	)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary: %v", err)
	}
	got, ok := vocabulary.Resolve(PriorityHigh)
	if !ok || got != "critical" {
		t.Errorf("Resolve(high) = %q,%v; want critical,true", got, ok)
	}
}

// Arity: exactly one default, at least one priority.
func TestPriorityVocabularyValidateRefusesTwoDefaults(t *testing.T) {
	vocabulary, err := NewPriorityVocabulary([]PriorityDefinition{
		{Priority: "a", Label: "A", Rank: "1/1", Tags: []PriorityTag{PriorityTagDefault}},
		{Priority: "b", Label: "B", Rank: "2/1", Tags: []PriorityTag{PriorityTagDefault}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary: %v", err)
	}
	if err := vocabulary.Validate(); err == nil {
		t.Error("Validate accepted two default priorities")
	}
}

func TestPriorityVocabularyValidateRefusesAnEmptySet(t *testing.T) {
	vocabulary, err := NewPriorityVocabulary(nil, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary: %v", err)
	}
	if err := vocabulary.Validate(); err == nil {
		t.Error("Validate accepted a project with no priorities")
	}
}

// rankedPriorityVocabulary builds a vocabulary whose priorities are named
// a, b, c... at the given ranks, the priority-flavoured twin of
// vocabulary_authoring_test.go's rankedVocabulary.
func rankedPriorityVocabulary(t *testing.T, ranks ...string) PriorityVocabulary {
	t.Helper()
	definitions := make([]PriorityDefinition, 0, len(ranks))
	for index, rank := range ranks {
		definitions = append(definitions, PriorityDefinition{
			Priority: Priority(string(rune('a' + index))),
			Label:    "Priority",
			Rank:     rank,
			Tags:     []PriorityTag{},
		})
	}
	definitions[0].Tags = []PriorityTag{PriorityTagDefault}
	vocabulary, err := NewPriorityVocabulary(definitions, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return vocabulary
}

// insertRank's exhausted-gap message is shared with Vocabulary.InsertRank
// (ranked.go), and it used to hardcode "statuses" regardless of which caller
// asked. This is the priority side of Ruling 9: the message must name
// priorities, not statuses, mirroring
// TestVocabularyInsertRankRefusesAnExhaustedGap's setup.
func TestPriorityVocabularyInsertRankRefusesAnExhaustedGapNamingPriorities(t *testing.T) {
	vocabulary := rankedPriorityVocabulary(t, "1/1", "2/1", "2/1")

	_, err := vocabulary.InsertRank("a", "c", true)
	if CategoryOf(err) != CategoryValidation {
		t.Fatalf("InsertRank(into an equal-rank gap) error = %v, want a validation refusal", err)
	}
	if !strings.Contains(err.Error(), "priorities") {
		t.Errorf("InsertRank error = %q, want it to name priorities", err)
	}
	if strings.Contains(err.Error(), "statuses") {
		t.Errorf("InsertRank error = %q, wrongly names statuses", err)
	}
}

// forwardTerminates's cycle message is shared with normalizeVocabularyDocument
// and used to hardcode "status" regardless of which caller asked. This is the
// priority side of Ruling 9, mirroring TestNewVocabularyRejectsAForwardingCycle's
// setup.
func TestNewPriorityVocabularyRejectsAForwardingCycleNamingPriorities(t *testing.T) {
	_, err := NewPriorityVocabulary(
		[]PriorityDefinition{{Priority: "todo", Label: "Todo", Rank: "1/1", Tags: []PriorityTag{PriorityTagDefault}}},
		[]PriorityAlias{{From: "a", To: "b"}, {From: "b", To: "a"}},
		nil,
	)
	if err == nil {
		t.Fatal("NewPriorityVocabulary() error = nil, want a cycle rejection")
	}
	if !strings.Contains(err.Error(), "priority") {
		t.Errorf("cycle error = %q, want it to name a priority", err)
	}
	if strings.Contains(err.Error(), "status") {
		t.Errorf("cycle error = %q, wrongly names a status", err)
	}
}

// A project that configured priorities sorts by its own order, not by the
// built-in one.
func TestConfiguredPriorityOrderDrivesSorting(t *testing.T) {
	vocabulary, err := NewPriorityVocabulary([]PriorityDefinition{
		{Priority: "blocker", Label: "Blocker", Rank: "1/1"},
		{Priority: PriorityHigh, Label: "High", Rank: "2/1", Tags: []PriorityTag{PriorityTagDefault}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary: %v", err)
	}
	if vocabulary.Order("blocker") >= vocabulary.Order(PriorityHigh) {
		t.Error("blocker does not sort ahead of high under the project's own order")
	}
	if got := vocabulary.Default(); got != PriorityHigh {
		t.Errorf("Default() = %q, want high", got)
	}
}

// The zero vocabulary's ordinals are today's built-in three, most urgent
// first, and a priority nothing defines or forwards sorts last. Nothing
// asserted these exact numbers before; they were only guaranteed by reading
// the deleted priorityOrder switch this vocabulary replaced, whose default
// arm returned 3.
func TestZeroPriorityVocabularyOrderPinsTheBuiltInOrdinals(t *testing.T) {
	var vocabulary PriorityVocabulary
	tests := []struct {
		priority Priority
		want     int
	}{
		{PriorityHigh, 0},
		{PriorityMedium, 1},
		{PriorityLow, 2},
		{Priority("nobody-defined-this"), 3},
	}
	for _, tt := range tests {
		if got := vocabulary.Order(tt.priority); got != tt.want {
			t.Errorf("Order(%q) = %d, want %d", tt.priority, got, tt.want)
		}
	}
}

// customPriorityVocabulary is a project that renamed high to critical,
// keeping medium and low as they were. It is the priority-flavoured twin of
// service_vocabulary_test.go's customVocabulary, used to exercise Service's
// read path the same way that file exercises it for statuses.
func customPriorityVocabulary(t *testing.T) PriorityVocabulary {
	t.Helper()
	vocabulary, err := NewPriorityVocabulary(
		[]PriorityDefinition{
			{Priority: "critical", Label: "Critical", Rank: "1/1", Tags: []PriorityTag{PriorityTagDefault}},
			{Priority: PriorityMedium, Label: "Medium", Rank: "2/1"},
			{Priority: PriorityLow, Label: "Low", Rank: "3/1"},
		},
		[]PriorityAlias{{From: PriorityHigh, To: "critical"}},
		nil,
	)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return vocabulary
}

func priorityServiceUnderTest(store *memoryTaskStore, ids IDSource, priorities PriorityVocabulary) Service {
	service := serviceUnderTest(store, ids)
	service.Priorities = priorities
	return service
}

// A task stored under a priority a rename replaced reads as the live one, and
// reports what was actually stored — Project's priority half of
// TestServiceProjectResolvesStoredStatusesAndReportsTheStoredValue.
func TestServiceProjectResolvesAStoredPriorityAndReportsTheStoredValue(t *testing.T) {
	store := newMemoryTaskStore(
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
			Title: "Renamed", Status: StatusBacklog, Priority: PriorityHigh, Rank: "1/1",
		}),
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
			Title: "Live", Status: StatusBacklog, Priority: "critical", Rank: "2/1",
		}),
	)
	service := priorityServiceUnderTest(store, &sequenceIDSource{}, customPriorityVocabulary(t))

	tasks, err := service.List(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	byID := make(map[string]Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
	}

	tests := map[string]struct {
		priority Priority
		stored   Priority
	}{
		"WB-01K0M6B8A4FTT8C39MXXYTW7F1": {priority: "critical", stored: PriorityHigh},
		"WB-01K0M6B8A4FTT8C39MXXYTW7F2": {priority: "critical"},
	}
	for id, want := range tests {
		task := byID[id]
		if task.Priority != want.priority || task.StoredPriority != want.stored {
			t.Errorf(
				"%s: priority = %q, storedPriority = %q, want %q and %q",
				id, task.Priority, task.StoredPriority, want.priority, want.stored,
			)
		}
	}
}

// A task stored under a renamed priority sorts where the live priority
// sorts, not last. Before Project resolved a stored priority, every task
// under a rename's old name fell into Order's unknown arm and sorted after
// even the project's least urgent live priority — precisely the stranding
// forwarding exists to prevent.
func TestServiceListOrdersAStoredPriorityByItsResolvedRankNotLast(t *testing.T) {
	store := newMemoryTaskStore(
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
			Title: "Stored under the low priority", Status: StatusBacklog, Priority: PriorityLow, Rank: "1/1",
		}),
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
			// "high" was renamed to "critical"; this task has not been touched
			// since, so it is still stored under the retired name.
			Title: "Stored under the renamed priority", Status: StatusBacklog, Priority: PriorityHigh, Rank: "2/1",
		}),
	)
	service := priorityServiceUnderTest(store, &sequenceIDSource{}, customPriorityVocabulary(t))

	tasks, err := service.List(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("List() returned %d tasks, want 2", len(tasks))
	}
	// critical (the renamed task's live priority) outranks low, so the
	// renamed task must sort first despite having a later rank and despite
	// being stored second. Sorting by the stored token, or falling into
	// Order's unknown arm, would both put it last instead.
	if got, want := tasks[0].ID, "WB-01K0M6B8A4FTT8C39MXXYTW7F2"; got != want {
		t.Fatalf("List()[0].ID = %q, want %q (the task resolving to critical, sorted ahead of low)", got, want)
	}
	if got, want := tasks[0].Priority, Priority("critical"); got != want {
		t.Fatalf("List()[0].Priority = %q, want %q", got, want)
	}
}

// priorityVocabularyRenamingHighToMedium collapses high into medium, keeping
// every live priority a built-in token. It exists alongside
// customPriorityVocabulary for the tests below that exercise a mutation path:
// isValidPriority (task.go) checks built-in shape, not project membership, so
// a task field or a filter argument naming a project-only token such as
// "critical" is refused before forwarding ever runs. Renaming among the
// built-in three sidesteps that unrelated limitation while still exercising
// real forwarding.
func priorityVocabularyRenamingHighToMedium(t *testing.T) PriorityVocabulary {
	t.Helper()
	vocabulary, err := NewPriorityVocabulary(
		[]PriorityDefinition{
			{Priority: PriorityMedium, Label: "Medium", Rank: "1/1", Tags: []PriorityTag{PriorityTagDefault}},
			{Priority: PriorityLow, Label: "Low", Rank: "2/1"},
		},
		[]PriorityAlias{{From: PriorityHigh, To: PriorityMedium}},
		nil,
	)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return vocabulary
}

// A priority filter naming a retired token matches the tasks a reader would
// say carry it: everything that resolves to the live priority, whichever
// token each is actually stored under. Comparing the filter argument against
// an already-resolved stored priority without resolving the argument too
// would silently stop matching the moment a project renamed anything, which
// is the "no tasks are in ready" mistake List's own doc comment warns against
// for statuses.
func TestServiceListFilterResolvesAStoredPriorityFilterThroughTheChains(t *testing.T) {
	store := newMemoryTaskStore(
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
			Title: "Stored under the renamed priority", Status: StatusBacklog, Priority: PriorityHigh, Rank: "1/1",
		}),
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
			Title: "Stored under the live priority", Status: StatusBacklog, Priority: PriorityMedium, Rank: "2/1",
		}),
		serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F3", TaskData{
			Title: "A different priority entirely", Status: StatusBacklog, Priority: PriorityLow, Rank: "3/1",
		}),
	)
	service := priorityServiceUnderTest(store, &sequenceIDSource{}, priorityVocabularyRenamingHighToMedium(t))

	// The retired token ("high") must select both tasks that read as medium,
	// not only the one literally stored under "high".
	retired := Priority(PriorityHigh)
	tasks, err := service.List(context.Background(), ListFilter{Priority: &retired})
	if err != nil {
		t.Fatalf("List(%q) error = %v", retired, err)
	}
	if len(tasks) != 2 {
		t.Fatalf("List(%q) returned %d tasks, want 2 (both tasks resolving to medium)", retired, len(tasks))
	}

	// The live token selects the same two tasks, by construction: a filter
	// and the tasks it names must agree on which priority a token means.
	live := Priority(PriorityMedium)
	sameTasks, err := service.List(context.Background(), ListFilter{Priority: &live})
	if err != nil {
		t.Fatalf("List(%q) error = %v", live, err)
	}
	if len(sameTasks) != 2 {
		t.Fatalf("List(%q) returned %d tasks, want 2", live, len(sameTasks))
	}
}

// sameBucket must resolve both sides of the priority comparison, the same way
// it already resolves both sides of the status comparison beside it: two
// tasks whose stored priority tokens differ while resolving to one live
// priority are drawn in one group on the board, so an anchor check that
// disagreed would refuse a neighbour the board draws right beside it.
func TestServicePlaceMutationAcceptsAnAnchorSharingAResolvedPriorityBucket(t *testing.T) {
	parent := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F1", TaskData{
		Title: "Parent", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1",
	})
	anchor := serviceSnapshot("WB-01K0M6B8A4FTT8C39MXXYTW7F2", TaskData{
		// "high" was renamed to "medium"; this task has not been touched
		// since, so it is still stored under the retired name.
		Title: "Anchor", Status: StatusBacklog, Priority: PriorityHigh, Rank: "2/1",
	})
	store := newMemoryTaskStore(parent, anchor)
	ids := &sequenceIDSource{values: []string{
		"01K0M6B8A4FTT8C39MXXYTW7E1",
		"01K0M6B8A4FTT8C39MXXYTW7E2",
	}}
	service := priorityServiceUnderTest(store, ids, priorityVocabularyRenamingHighToMedium(t))

	_, err := service.PlaceMutation(context.Background(), parent.State.TaskID, PlaceInput{
		Status: StatusBacklog,
		After:  anchor.State.TaskID,
	})
	if err != nil {
		t.Fatalf("PlaceMutation() error = %v, want the anchor accepted as sharing the resolved priority bucket", err)
	}
}
