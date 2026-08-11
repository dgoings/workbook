package core

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// LegacyVocabulary and DefaultVocabulary are two names for the same six
// statuses today, and this test exists to make the day they stop being the same
// a deliberate edit.
//
// They answer different questions. DefaultVocabulary is what a project minted
// by this build starts with, and it is a product decision that will change.
// LegacyVocabulary is what a project that predates the configuration ledger was
// already using, which is a fact about those projects and cannot change under
// them. Seeding an existing project's genesis from the wrong one would silently
// re-columnize somebody's board.
func TestLegacyVocabularyStillMatchesDefaultVocabulary(t *testing.T) {
	legacy := LegacyVocabulary().Document()
	current := DefaultVocabulary().Document()
	if !reflect.DeepEqual(legacy, current) {
		t.Fatalf(
			"LegacyVocabulary() = %#v, want the same as DefaultVocabulary() = %#v; "+
				"if this divergence is intended, update this test and say why",
			legacy, current,
		)
	}
}

func TestDefaultVocabularyAssignsTheThreeRoles(t *testing.T) {
	vocabulary := DefaultVocabulary()

	if got, want := vocabulary.Default(), StatusBacklog; got != want {
		t.Fatalf("Default() = %q, want %q", got, want)
	}
	for _, status := range []Status{StatusBacklog, StatusBlocked, StatusInProgress, StatusInReview, StatusDone} {
		if vocabulary.IsNext(status) {
			t.Fatalf("IsNext(%q) = true, want only %q", status, StatusReady)
		}
	}
	if !vocabulary.IsNext(StatusReady) {
		t.Fatalf("IsNext(%q) = false, want true", StatusReady)
	}
	for _, status := range []Status{StatusBacklog, StatusReady, StatusBlocked, StatusInProgress, StatusInReview} {
		if vocabulary.IsDone(status) {
			t.Fatalf("IsDone(%q) = true, want only %q", status, StatusDone)
		}
	}
	if !vocabulary.IsDone(StatusDone) {
		t.Fatalf("IsDone(%q) = false, want true", StatusDone)
	}
	if err := vocabulary.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// The order the built-in vocabulary reports is the order the previous
// hard-coded array reported, position for position. Every list, board, and
// comparison reads it.
func TestDefaultVocabularyOrderMatchesTheShippedOrder(t *testing.T) {
	vocabulary := DefaultVocabulary()
	for index, status := range []Status{
		StatusBacklog, StatusReady, StatusBlocked, StatusInProgress, StatusInReview, StatusDone,
	} {
		if got := vocabulary.Order(status); got != index {
			t.Fatalf("Order(%q) = %d, want %d", status, got, index)
		}
	}
	// An unknown status sorts after every live one rather than failing, which
	// is what keeps a board readable while a rename propagates.
	if got, want := vocabulary.Order("shipped"), 6; got != want {
		t.Fatalf("Order(unknown) = %d, want %d", got, want)
	}
}

func TestVocabularyResolveFollowsRenameAndRetirementChains(t *testing.T) {
	vocabulary, err := NewVocabulary(
		[]StatusDefinition{
			{Status: "todo", Label: "Todo", Rank: "1/1", Tags: []StatusTag{StatusTagDefault, StatusTagNext}},
			{Status: "shipped", Label: "Shipped", Rank: "2/1", Tags: []StatusTag{StatusTagDone}},
		},
		[]StatusAlias{{From: "backlog", To: "queued"}, {From: "queued", To: "todo"}},
		[]RetiredStatus{{Status: "wontfix", Destination: "shipped"}, {Status: "icebox", Destination: "wontfix"}},
	)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}

	tests := map[Status]struct {
		want Status
		live bool
	}{
		"todo":    {want: "todo", live: true},
		"queued":  {want: "todo", live: true},
		"backlog": {want: "todo", live: true},
		"wontfix": {want: "shipped", live: true},
		"icebox":  {want: "shipped", live: true},
		"unknown": {want: "unknown", live: false},
	}
	for status, want := range tests {
		got, live := vocabulary.Resolve(status)
		if got != want.want || live != want.live {
			t.Errorf("Resolve(%q) = (%q, %t), want (%q, %t)", status, got, live, want.want, want.live)
		}
	}
}

// A checkpoint is data read off a ref. Resolve has to terminate on a document
// no fold could produce, because a hand-edited or corrupted one is exactly what
// a total function is for.
func TestVocabularyResolveTerminatesOnAForwardingCycle(t *testing.T) {
	vocabulary := newVocabularyFromCanonical(VocabularyDocument{
		Statuses: []StatusDefinition{
			{Status: "todo", Label: "Todo", Rank: "1/1", Tags: []StatusTag{StatusTagDefault, StatusTagNext, StatusTagDone}},
		},
		Aliases: []StatusAlias{{From: "a", To: "b"}, {From: "b", To: "a"}},
		Retired: []RetiredStatus{},
	})

	got, live := vocabulary.Resolve("a")
	if live || got != "a" {
		t.Fatalf("Resolve(a) = (%q, %t), want (a, false)", got, live)
	}
}

func TestNewVocabularyRejectsAForwardingCycle(t *testing.T) {
	_, err := NewVocabulary(
		[]StatusDefinition{{Status: "todo", Label: "Todo", Rank: "1/1", Tags: []StatusTag{StatusTagDefault}}},
		[]StatusAlias{{From: "a", To: "b"}, {From: "b", To: "a"}},
		nil,
	)
	if err == nil {
		t.Fatal("NewVocabulary() error = nil, want a cycle rejection")
	}
}

func TestNewVocabularyRejectsMalformedDefinitions(t *testing.T) {
	valid := []StatusDefinition{
		{Status: "todo", Label: "Todo", Rank: "1/1", Tags: []StatusTag{StatusTagDefault, StatusTagNext}},
		{Status: "shipped", Label: "Shipped", Rank: "2/1", Tags: []StatusTag{StatusTagDone}},
	}

	tests := map[string]struct {
		definitions []StatusDefinition
		aliases     []StatusAlias
		retired     []RetiredStatus
	}{
		"blank status": {definitions: []StatusDefinition{{Status: "", Label: "Todo", Rank: "1/1"}}},
		"uppercase status": {
			definitions: []StatusDefinition{{Status: "Todo", Label: "Todo", Rank: "1/1"}},
		},
		"spaced status": {
			definitions: []StatusDefinition{{Status: "in progress", Label: "Todo", Rank: "1/1"}},
		},
		"leading hyphen": {
			definitions: []StatusDefinition{{Status: "-todo", Label: "Todo", Rank: "1/1"}},
		},
		"double hyphen": {
			definitions: []StatusDefinition{{Status: "to--do", Label: "Todo", Rank: "1/1"}},
		},
		"oversized status": {
			definitions: []StatusDefinition{{Status: Status(strings.Repeat("a", MaxStatusNameBytes+1)), Label: "Todo", Rank: "1/1"}},
		},
		"blank label": {
			definitions: []StatusDefinition{{Status: "todo", Label: "  ", Rank: "1/1"}},
		},
		"oversized label": {
			definitions: []StatusDefinition{{Status: "todo", Label: strings.Repeat("a", MaxStatusLabelBytes+1), Rank: "1/1"}},
		},
		"malformed rank": {
			definitions: []StatusDefinition{{Status: "todo", Label: "Todo", Rank: "2/2"}},
		},
		"duplicate status": {
			definitions: []StatusDefinition{
				{Status: "todo", Label: "Todo", Rank: "1/1"},
				{Status: "todo", Label: "Other", Rank: "2/1"},
			},
		},
		"unknown tag": {
			definitions: []StatusDefinition{{Status: "todo", Label: "Todo", Rank: "1/1", Tags: []StatusTag{"urgent"}}},
		},
		"self alias": {
			definitions: valid,
			aliases:     []StatusAlias{{From: "old", To: "old"}},
		},
		"live status also forwarded": {
			definitions: valid,
			aliases:     []StatusAlias{{From: "todo", To: "shipped"}},
		},
		"status forwarded twice": {
			definitions: valid,
			aliases:     []StatusAlias{{From: "old", To: "todo"}},
			retired:     []RetiredStatus{{Status: "old", Destination: "shipped"}},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewVocabulary(test.definitions, test.aliases, test.retired); err == nil {
				t.Fatal("NewVocabulary() error = nil, want a rejection")
			} else if got := CategoryOf(err); got != CategoryValidation {
				t.Fatalf("NewVocabulary() category = %q, want %q", got, CategoryValidation)
			}
		})
	}
}

// A size ceiling is not a shape rule, and NewVocabulary must represent a
// vocabulary that is over one. The fold can reach that state without any author
// asking for it, and a value this package refuses to construct is a value the
// fold cannot produce.
func TestNewVocabularyRepresentsAVocabularyOverEveryCeiling(t *testing.T) {
	if _, err := NewVocabulary(manyStatuses(MaxStatusCount+1), nil, nil); err != nil {
		t.Fatalf("NewVocabulary(%d statuses) error = %v", MaxStatusCount+1, err)
	}
	if _, err := NewVocabulary(manyStatuses(1), manyAliases(MaxStatusAliasCount+1), nil); err != nil {
		t.Fatalf("NewVocabulary(%d aliases) error = %v", MaxStatusAliasCount+1, err)
	}
	if _, err := NewVocabulary(manyStatuses(1), nil, manyRetirements(MaxStatusRetiredCount+1)); err != nil {
		t.Fatalf("NewVocabulary(%d retirements) error = %v", MaxStatusRetiredCount+1, err)
	}
}

// Growth past a ceiling is refused; shrinkage while over one is not. A rule
// that refused every pack while over a ceiling would refuse the removals that
// are the only way back under it.
func TestValidateVocabularyGrowthRefusesOnlyGrowth(t *testing.T) {
	over := VocabularyDocument{Statuses: manyStatuses(MaxStatusCount + 1)}
	atCeiling := VocabularyDocument{Statuses: manyStatuses(MaxStatusCount)}
	further := VocabularyDocument{Statuses: manyStatuses(MaxStatusCount + 2)}

	if err := validateVocabularyGrowth(atCeiling, over); err == nil {
		t.Fatal("growth past the status ceiling was allowed, want a refusal")
	} else if !strings.Contains(err.Error(), "workbook status delete") {
		// The command has to be the one the CLI actually implements. This
		// message named `workbook status remove` before that verb family
		// existed, which would have sent a reader to a command Workbook does
		// not have.
		t.Fatalf("refusal = %q, want it to name the removing command", err)
	}
	if err := validateVocabularyGrowth(over, further); err == nil {
		t.Fatal("further growth while over the ceiling was allowed, want a refusal")
	}
	if err := validateVocabularyGrowth(over, atCeiling); err != nil {
		t.Fatalf("shrinking back to the ceiling was refused: %v", err)
	}
	if err := validateVocabularyGrowth(over, over); err != nil {
		t.Fatalf("a pack that holds the count steady while over was refused: %v", err)
	}
}

// Every arity message has to name the command that fixes the state it
// describes. A validation failure that only says what is wrong leaves the
// reader to guess the verb, and the verbs here are new.
func TestVocabularyValidateNamesTheFixingCommand(t *testing.T) {
	tests := map[string]struct {
		definitions []StatusDefinition
		wantCommand string
	}{
		"no statuses": {
			definitions: nil,
			wantCommand: "workbook status add",
		},
		"no default": {
			definitions: []StatusDefinition{
				{Status: "todo", Label: "Todo", Rank: "1/1", Tags: []StatusTag{StatusTagNext}},
				{Status: "shipped", Label: "Shipped", Rank: "2/1", Tags: []StatusTag{StatusTagDone}},
			},
			wantCommand: "workbook status tag <status> --tag default",
		},
		"two defaults": {
			definitions: []StatusDefinition{
				{Status: "todo", Label: "Todo", Rank: "1/1", Tags: []StatusTag{StatusTagDefault, StatusTagNext}},
				{Status: "shipped", Label: "Shipped", Rank: "2/1", Tags: []StatusTag{StatusTagDefault, StatusTagDone}},
			},
			wantCommand: "workbook status tag <status> --tag default",
		},
		"no next": {
			definitions: []StatusDefinition{
				{Status: "todo", Label: "Todo", Rank: "1/1", Tags: []StatusTag{StatusTagDefault}},
				{Status: "shipped", Label: "Shipped", Rank: "2/1", Tags: []StatusTag{StatusTagDone}},
			},
			wantCommand: "workbook status tag <status> --tag next",
		},
		"no done": {
			definitions: []StatusDefinition{
				{Status: "todo", Label: "Todo", Rank: "1/1", Tags: []StatusTag{StatusTagDefault, StatusTagNext}},
				{Status: "shipped", Label: "Shipped", Rank: "2/1"},
			},
			wantCommand: "workbook status tag <status> --tag done",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			vocabulary, err := NewVocabulary(test.definitions, nil, nil)
			if err != nil {
				t.Fatalf("NewVocabulary() error = %v", err)
			}
			err = vocabulary.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want a rejection")
			}
			if got := CategoryOf(err); got != CategoryValidation {
				t.Fatalf("Validate() category = %q, want %q", got, CategoryValidation)
			}
			if !strings.Contains(err.Error(), test.wantCommand) {
				t.Fatalf("Validate() = %q, want it to name %q", err, test.wantCommand)
			}
		})
	}
}

// Definitions hands out a copy because callers sort it and hand it to
// templates. A shared backing array would let one page's sort reorder the next.
func TestVocabularyDefinitionsAreACopy(t *testing.T) {
	vocabulary := DefaultVocabulary()
	definitions := vocabulary.Definitions()
	definitions[0].Label = "Mutated"
	definitions[0].Tags = append(definitions[0].Tags, StatusTagDone)

	if got := vocabulary.Definitions()[0].Label; got != "Backlog" {
		t.Fatalf("Definitions()[0].Label = %q after mutating a copy, want %q", got, "Backlog")
	}
	if vocabulary.IsDone(StatusBacklog) {
		t.Fatal("mutating a returned tag slice changed the vocabulary")
	}
}

func TestVocabularyLabelFallsBackToTheRawStatus(t *testing.T) {
	vocabulary := DefaultVocabulary()
	if got, want := vocabulary.Label(StatusInProgress), "In Progress"; got != want {
		t.Fatalf("Label(%q) = %q, want %q", StatusInProgress, got, want)
	}
	if got, want := vocabulary.Label("shipped"), "shipped"; got != want {
		t.Fatalf("Label(unknown) = %q, want %q", got, want)
	}
}

// The zero value has to be distinguishable from a configured vocabulary,
// because Service reads it as "substitute the built-in default" and every
// construction that predates per-project statuses relies on that.
func TestZeroVocabularyIsRecognizable(t *testing.T) {
	if !(Vocabulary{}).IsZero() {
		t.Fatal("Vocabulary{}.IsZero() = false, want true")
	}
	if DefaultVocabulary().IsZero() {
		t.Fatal("DefaultVocabulary().IsZero() = true, want false")
	}
}

func manyStatuses(count int) []StatusDefinition {
	definitions := make([]StatusDefinition, count)
	for index := range definitions {
		definitions[index] = StatusDefinition{
			Status: Status("s" + strconv.Itoa(index)),
			Label:  "S",
			Rank:   strconv.Itoa(index+1) + "/1",
		}
	}
	return definitions
}

func manyAliases(count int) []StatusAlias {
	aliases := make([]StatusAlias, count)
	for index := range aliases {
		aliases[index] = StatusAlias{From: Status("a" + strconv.Itoa(index)), To: "todo"}
	}
	return aliases
}

func manyRetirements(count int) []RetiredStatus {
	retired := make([]RetiredStatus, count)
	for index := range retired {
		retired[index] = RetiredStatus{Status: Status("r" + strconv.Itoa(index)), Destination: "shipped"}
	}
	return retired
}
