package perf

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/gitstore"
)

// Mutation witness: accepting inconsistent populations, an empty history, or
// a tombstone without a preceding active state would make benchmark fixtures
// describe a population the product cannot represent.
func TestFixtureSpec(t *testing.T) {
	tests := []struct {
		name string
		spec FixtureSpec
		want string
	}{
		{
			name: "population sum differs from total",
			spec: FixtureSpec{TotalTasks: 10, ActiveTasks: 8, TombstonedTasks: 1, OperationsPerTask: 8, ObjectFormat: "sha1"},
			want: "active tasks + tombstoned tasks must equal total tasks",
		},
		{
			name: "total tasks is zero",
			spec: FixtureSpec{TotalTasks: 0, ActiveTasks: 0, TombstonedTasks: 0, OperationsPerTask: 8, ObjectFormat: "sha1"},
			want: "total tasks must be positive",
		},
		{
			name: "history depth is zero",
			spec: FixtureSpec{TotalTasks: 1, ActiveTasks: 1, TombstonedTasks: 0, OperationsPerTask: 0, ObjectFormat: "sha1"},
			want: "operations per task must be positive",
		},
		{
			name: "tombstone has no preceding operation",
			spec: FixtureSpec{TotalTasks: 1, ActiveTasks: 0, TombstonedTasks: 1, OperationsPerTask: 1, ObjectFormat: "sha1"},
			want: "tombstoned tasks require at least two operations per task",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildFixture(context.Background(), filepath.Join(t.TempDir(), "fixture"), test.spec)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("BuildFixture() error = %v, want %q", err, test.want)
			}
		})
	}
}

// Mutation witness: omitting a representative field, allowing a forward or
// cyclic dependency, appending after a tombstone, or deriving IDs/timestamps
// from the host would produce a fixture that does not model its stated work.
func TestBuildFixtureCreatesRepresentativeDeterministicHistories(t *testing.T) {
	spec := FixtureSpec{
		TotalTasks: 10, ActiveTasks: 8, TombstonedTasks: 2,
		OperationsPerTask: 8, ObjectFormat: "sha1",
	}
	first, err := BuildFixture(context.Background(), filepath.Join(t.TempDir(), "first"), spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildFixture(context.Background(), filepath.Join(t.TempDir(), "second"), spec)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := len(first.TaskIDs), 10; got != want {
		t.Fatalf("all task IDs = %d, want %d", got, want)
	}
	if got, want := len(first.ActiveTaskIDs), 8; got != want {
		t.Fatalf("active task IDs = %d, want %d", got, want)
	}
	if got, want := len(first.TombstonedTaskIDs), 2; got != want {
		t.Fatalf("tombstoned task IDs = %d, want %d", got, want)
	}
	if !reflect.DeepEqual(first.TaskIDs, second.TaskIDs) ||
		!reflect.DeepEqual(first.ActiveTaskIDs, second.ActiveTaskIDs) ||
		!reflect.DeepEqual(first.TombstonedTaskIDs, second.TombstonedTaskIDs) ||
		!reflect.DeepEqual(first.Dependencies, second.Dependencies) {
		t.Fatalf("fixture populations differ:\nfirst=%#v\nsecond=%#v", first, second)
	}

	firstRepository, err := gitstore.Open(context.Background(), first.Root)
	if err != nil {
		t.Fatal(err)
	}
	secondRepository, err := gitstore.Open(context.Background(), second.Root)
	if err != nil {
		t.Fatal(err)
	}
	firstIndex := make(map[string]int, len(first.TaskIDs))
	deletedTips := 0
	fractionalRank := false
	for index, taskID := range first.TaskIDs {
		firstIndex[taskID] = index
		firstSnapshot, err := firstRepository.Get(context.Background(), first.Config, taskID)
		if err != nil {
			t.Fatal(err)
		}
		secondSnapshot, err := secondRepository.Get(context.Background(), second.Config, taskID)
		if err != nil {
			t.Fatal(err)
		}
		if firstSnapshot.Head != secondSnapshot.Head || !reflect.DeepEqual(firstSnapshot.State, secondSnapshot.State) {
			t.Fatalf("task %q is not deterministic:\nfirst=%#v\nsecond=%#v", taskID, firstSnapshot, secondSnapshot)
		}
		if got, want := firstSnapshot.State.LogicalClock, uint64(8); got != want {
			t.Errorf("task %q logical clock = %d, want %d", taskID, got, want)
		}
		if strings.TrimSpace(firstSnapshot.State.Task.Description) == "" {
			t.Errorf("task %q description is blank", taskID)
		}
		if len(firstSnapshot.State.Task.Labels) == 0 {
			t.Errorf("task %q labels are empty", taskID)
		}
		if firstSnapshot.State.Task.Rank == "1/2" || firstSnapshot.State.Task.Rank == "3/2" {
			fractionalRank = true
		}
		if firstSnapshot.State.Task.Deleted {
			deletedTips++
		}
		if got := strings.TrimSpace(runGit(t, first.Root, "rev-list", "--count", firstSnapshot.Head)); got != "8" {
			t.Errorf("task %q history count = %q, want 8", taskID, got)
		}
	}
	if !fractionalRank {
		t.Error("fixture has no literal non-integer canonical rank")
	}
	if got, want := deletedTips, 2; got != want {
		t.Errorf("deleted tips = %d, want %d", got, want)
	}
	if len(first.Dependencies) == 0 {
		t.Fatal("fixture has no direct dependencies")
	}
	for _, dependency := range first.Dependencies {
		dependentIndex, dependentFound := firstIndex[dependency.Dependent]
		dependencyIndex, dependencyFound := firstIndex[dependency.Dependency]
		if !dependentFound || !dependencyFound || dependencyIndex >= dependentIndex {
			t.Errorf("dependency = %#v, want a direct backward dependency", dependency)
		}
		state, err := firstRepository.Get(context.Background(), first.Config, dependency.Dependent)
		if err != nil {
			t.Fatal(err)
		}
		if !containsString(state.State.Task.Dependencies, dependency.Dependency) {
			t.Errorf("task %q dependencies = %#v, want %q", dependency.Dependent, state.State.Task.Dependencies, dependency.Dependency)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestBuildFixtureCreatesCompleteTipStatesWithoutReplay(t *testing.T) {
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			if objectFormat == "sha256" && !supportsObjectFormat(t, objectFormat) {
				t.Skip("Git does not support SHA-256 repositories")
			}

			spec := FixtureSpec{TotalTasks: 3, ActiveTasks: 3, OperationsPerTask: 4, ObjectFormat: objectFormat}
			fixture, err := BuildFixture(context.Background(), filepath.Join(t.TempDir(), "fixture"), spec)
			if err != nil {
				t.Fatal(err)
			}
			if len(fixture.TaskIDs) != 3 {
				t.Fatalf("task IDs = %d, want 3", len(fixture.TaskIDs))
			}

			repository, err := gitstore.Open(context.Background(), fixture.Root)
			if err != nil {
				t.Fatal(err)
			}
			for _, taskID := range fixture.TaskIDs {
				snapshot, err := repository.Get(context.Background(), fixture.Config, taskID)
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.State.LogicalClock != 4 || snapshot.State.Task.Title == "" {
					t.Fatalf("tip = %#v", snapshot.State)
				}
				output := runGit(t, fixture.Root, "rev-list", "--count", snapshot.Head)
				if strings.TrimSpace(output) != "4" {
					t.Fatalf("history count = %q, want 4", output)
				}
			}
		})
	}
}

func supportsObjectFormat(t *testing.T, objectFormat string) bool {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	command := exec.Command("git", "init", "--object-format="+objectFormat, probe)
	if _, err := command.CombinedOutput(); err != nil {
		return false
	}
	return true
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}
