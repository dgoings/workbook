package core

import (
	"reflect"
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
