package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDependencyClosingPath(t *testing.T) {
	tests := []struct {
		name         string
		dependencies map[string][]string
		from         string
		to           string
		want         []string
	}{
		{
			name:         "no path leaves the edge safe",
			dependencies: map[string][]string{"a": {}, "b": {}},
			from:         "a",
			to:           "b",
		},
		{
			name:         "direct reverse edge closes immediately",
			dependencies: map[string][]string{"b": {"a"}},
			from:         "a",
			to:           "b",
			want:         []string{"b", "a"},
		},
		{
			name:         "transitive path is reported in full",
			dependencies: map[string][]string{"b": {"c"}, "c": {"d"}, "d": {"a"}},
			from:         "a",
			to:           "b",
			want:         []string{"b", "c", "d", "a"},
		},
		{
			name:         "self edge closes on the task itself",
			dependencies: map[string][]string{},
			from:         "a",
			to:           "a",
			want:         []string{"a"},
		},
		{
			// A tombstoned task is absent from the graph, matching the
			// eligibility rule task selection applies, so a path through one is
			// not a path at all.
			name:         "path through an absent task does not close",
			dependencies: map[string][]string{"b": {"gone"}},
			from:         "a",
			to:           "b",
		},
		{
			// An existing cycle elsewhere in the graph must not make the walk
			// loop forever or report an unrelated path.
			name:         "unrelated existing cycle terminates",
			dependencies: map[string][]string{"b": {"c"}, "c": {"b"}},
			from:         "a",
			to:           "b",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := DependencyClosingPath(test.dependencies, test.from, test.to)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("DependencyClosingPath() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestConflictErrorCarriesRetryableCategory(t *testing.T) {
	one := []Conflict{{TaskID: "WB-1", Type: ConflictDescription, Description: &DescriptionConflict{}}}
	err := ConflictError(one)
	if got, want := CategoryOf(err), CategoryConflict; got != want {
		t.Fatalf("ConflictError() category = %q, want %q", got, want)
	}
	if got, want := ExitCode(err), 8; got != want {
		t.Fatalf("ConflictError() exit code = %d, want %d", got, want)
	}

	many := append(one, Conflict{
		TaskID:    "WB-2",
		Type:      ConflictTombstone,
		Tombstone: &TombstoneConflict{Operation: OperationFieldSet},
	})
	if got := ConflictError(many).Error(); got == ConflictError(one).Error() {
		t.Fatalf("ConflictError() did not distinguish %d conflicts from one: %q", len(many), got)
	}
}

// Two clones renaming the same priority to different names is a lost decision,
// and the report has to name both so the discarded one is not lost silently.
func TestConfigConflictPriorityRenameDescribesBothSides(t *testing.T) {
	conflict := ConfigConflict{
		Type:     ConfigConflictPriorityRename,
		Priority: PriorityHigh,
		Ours:     "critical",
		Theirs:   "urgent",
	}
	got := ConfigConflictDetail(conflict)
	if !strings.Contains(got, "critical") || !strings.Contains(got, "urgent") {
		t.Errorf("Describe() = %q; it names neither side", got)
	}
}

// A priority removed on both sides into different destinations is the same
// lost decision as a status removed into two places: where the tasks belong
// is not something a tiebreak should pick.
func TestConfigConflictPriorityRetiredDescribesBothDestinations(t *testing.T) {
	conflict := ConfigConflict{
		Type:     ConfigConflictPriorityRetired,
		Priority: PriorityLow,
		Ours:     "medium",
		Theirs:   "high",
	}
	got := ConfigConflictDetail(conflict)
	if !strings.Contains(got, "medium") || !strings.Contains(got, "high") {
		t.Errorf("ConfigConflictDetail() = %q; it names neither destination", got)
	}
}

// Two clones defining the same priority differently — including only in its
// color — is the definition conflict, not a type of its own for color: a
// color lives inside the definition and is lost the same way a label is.
func TestConfigConflictPriorityDefinitionDescribesBothSides(t *testing.T) {
	conflict := ConfigConflict{
		Type:     ConfigConflictPriorityDefinition,
		Priority: PriorityMedium,
		Ours:     `"Urgent" at rank 2/1 colored #ff0000`,
		Theirs:   `"Urgent" at rank 2/1 colored #00ff00`,
	}
	got := ConfigConflictDetail(conflict)
	if !strings.Contains(got, "#ff0000") || !strings.Contains(got, "#00ff00") {
		t.Errorf("ConfigConflictDetail() = %q; it names neither color", got)
	}
}

// The arity conflict has no two sides to name — the repair picked a priority
// by position, so the report just says what happened, the same shape
// ConfigConflictStatusArity's line takes.
func TestConfigConflictPriorityArityNamesNoSides(t *testing.T) {
	got := ConfigConflictDetail(ConfigConflict{Type: ConfigConflictPriorityArity, Priority: PriorityLow})
	if got == string(ConfigConflictPriorityArity) {
		t.Fatal("ConfigConflictDetail() fell through to the bare type string")
	}
	if !strings.Contains(got, "priority role") {
		t.Fatalf("ConfigConflictDetail() = %q, want it to name what was repaired", got)
	}
}

// ConfigConflictError leads with "priority %s" rather than "status %s" for a
// priority conflict, because Status cannot name a priority and reusing its
// prefix would mislabel every one of these.
func TestConfigConflictErrorLeadsWithPriorityPrefix(t *testing.T) {
	err := ConfigConflictError([]ConfigConflict{{
		Type:     ConfigConflictPriorityRename,
		Priority: PriorityHigh,
		Ours:     "critical",
		Theirs:   "urgent",
	}})
	if CategoryOf(err) != CategoryConflict {
		t.Fatalf("ConfigConflictError() category = %q, want %q", CategoryOf(err), CategoryConflict)
	}
	if !strings.HasPrefix(err.Error(), "priority high: ") {
		t.Fatalf("ConfigConflictError() = %q, want it to lead with %q", err.Error(), "priority high: ")
	}
}

// A ConfigConflict recorded before the Priority field existed — every status,
// root-vocabulary and display-setting conflict already in a ledger — encodes
// to exactly the same bytes it always did. omitempty is what makes that true,
// and this pins it rather than trusting the struct tag by inspection.
func TestConfigConflictJSONOmitsPriorityWhenUnset(t *testing.T) {
	conflict := ConfigConflict{
		Type:   ConfigConflictStatusRename,
		Status: StatusBlocked,
		Ours:   "todo",
		Theirs: "doing",
	}
	encoded, err := json.Marshal(conflict)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "priority") {
		t.Fatalf("encoded conflict = %s, want no priority key for a status conflict", encoded)
	}
	want := `{"type":"status-rename","status":"blocked","ours":"todo","theirs":"doing"}`
	if string(encoded) != want {
		t.Fatalf("encoded conflict = %s, want %s", encoded, want)
	}
}

// A priority conflict's Priority field does encode, alongside the empty
// status every priority conflict still carries — Status has no omitempty, so
// it was always present and stays present.
func TestConfigConflictJSONEncodesPriorityWhenSet(t *testing.T) {
	conflict := ConfigConflict{
		Type:     ConfigConflictPriorityRename,
		Priority: PriorityHigh,
		Ours:     "critical",
		Theirs:   "urgent",
	}
	encoded, err := json.Marshal(conflict)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	want := `{"type":"priority-rename","status":"","priority":"high","ours":"critical","theirs":"urgent"}`
	if string(encoded) != want {
		t.Fatalf("encoded conflict = %s, want %s", encoded, want)
	}
}
