package perf

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testenv"
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
			name: "active tasks is negative despite matching total",
			spec: FixtureSpec{TotalTasks: 1, ActiveTasks: -1, TombstonedTasks: 2, OperationsPerTask: 2, ObjectFormat: "sha1"},
			want: "active tasks must not be negative",
		},
		{
			name: "tombstoned tasks is negative despite matching total",
			spec: FixtureSpec{TotalTasks: 1, ActiveTasks: 2, TombstonedTasks: -1, OperationsPerTask: 2, ObjectFormat: "sha1"},
			want: "tombstoned tasks must not be negative",
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
	statuses := make(map[core.Status]struct{})
	priorities := make(map[core.Priority]struct{})
	operationTypes := make(map[core.OperationType]struct{})
	operationFields := make(map[string]struct{})
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
		statuses[firstSnapshot.State.Task.Status] = struct{}{}
		priorities[firstSnapshot.State.Task.Priority] = struct{}{}
		history := readFixtureHistory(t, first, taskID)
		if got, want := len(history), 8; got != want {
			t.Errorf("task %q history count = %d, want %d", taskID, got, want)
		}
		assertFixtureHistoryHasNoNoopOperations(t, history)
		for _, commit := range history {
			for _, operation := range commit.Pack.Operations {
				operationTypes[operation.Type] = struct{}{}
				if operation.Field != "" {
					operationFields[operation.Field] = struct{}{}
				}
			}
		}
	}
	if !fractionalRank {
		t.Error("fixture has no literal non-integer canonical rank")
	}
	if got, want := deletedTips, 2; got != want {
		t.Errorf("deleted tips = %d, want %d", got, want)
	}
	if len(statuses) < 2 {
		t.Errorf("final statuses = %#v, want deterministic variation", statuses)
	}
	if len(priorities) < 2 {
		t.Errorf("final priorities = %#v, want deterministic variation", priorities)
	}
	for _, want := range []core.OperationType{
		core.OperationTaskCreate,
		core.OperationFieldSet,
		core.OperationSetAdd,
		core.OperationTaskTombstone,
	} {
		if _, found := operationTypes[want]; !found {
			t.Errorf("operation types = %#v, want %q", operationTypes, want)
		}
	}
	for _, want := range []string{"description", "status", "priority", "labels", "dependencies", "rank"} {
		if _, found := operationFields[want]; !found {
			t.Errorf("operation fields = %#v, want %q semantics", operationFields, want)
		}
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

// Mutation witness: shrinking either population, shortening histories, losing
// cross-build determinism, or replacing set/rank operations with scalar
// padding invalidates the exact local acceptance fixture.
func TestBuildFixtureCreatesExactAcceptancePopulationAcrossObjectFormats(t *testing.T) {
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			if objectFormat == "sha256" && !supportsObjectFormat(t, objectFormat) {
				testenv.MissingCapability(t, "Git does not support SHA-256 repositories")
			}
			spec := FixtureSpec{
				TotalTasks:        500,
				ActiveTasks:       475,
				TombstonedTasks:   25,
				OperationsPerTask: 20,
				ObjectFormat:      objectFormat,
			}
			first, err := BuildFixture(context.Background(), filepath.Join(t.TempDir(), "first"), spec)
			if err != nil {
				t.Fatal(err)
			}
			second, err := BuildFixture(context.Background(), filepath.Join(t.TempDir(), "second"), spec)
			if err != nil {
				t.Fatal(err)
			}

			if got := [3]int{len(first.TaskIDs), len(first.ActiveTaskIDs), len(first.TombstonedTaskIDs)}; got != [3]int{500, 475, 25} {
				t.Fatalf("fixture populations = %v, want [500 475 25]", got)
			}
			if !reflect.DeepEqual(first.TaskIDs, second.TaskIDs) ||
				!reflect.DeepEqual(first.ActiveTaskIDs, second.ActiveTaskIDs) ||
				!reflect.DeepEqual(first.TombstonedTaskIDs, second.TombstonedTaskIDs) ||
				!reflect.DeepEqual(first.Dependencies, second.Dependencies) {
				t.Fatal("acceptance fixture IDs and populations are not deterministic")
			}

			firstRepository, err := gitstore.Open(context.Background(), first.Root)
			if err != nil {
				t.Fatal(err)
			}
			secondRepository, err := gitstore.Open(context.Background(), second.Root)
			if err != nil {
				t.Fatal(err)
			}
			firstSnapshots, err := firstRepository.List(context.Background(), first.Config)
			if err != nil {
				t.Fatal(err)
			}
			secondSnapshots, err := secondRepository.List(context.Background(), second.Config)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(firstSnapshots, secondSnapshots) {
				t.Fatal("acceptance fixture heads and states are not deterministic within the object format")
			}
			if got, want := len(firstSnapshots), 500; got != want {
				t.Fatalf("fixture snapshots = %d, want %d", got, want)
			}

			deleted := 0
			statuses := make(map[core.Status]struct{})
			priorities := make(map[core.Priority]struct{})
			fractionalRank := false
			for _, snapshot := range firstSnapshots {
				if got, want := snapshot.State.LogicalClock, uint64(20); got != want {
					t.Fatalf("task %q logical clock = %d, want %d", snapshot.State.TaskID, got, want)
				}
				if snapshot.State.Task.Deleted {
					deleted++
				}
				statuses[snapshot.State.Task.Status] = struct{}{}
				priorities[snapshot.State.Task.Priority] = struct{}{}
				if strings.HasSuffix(snapshot.State.Task.Rank, "/2") {
					fractionalRank = true
				}
			}
			if deleted != 25 {
				t.Fatalf("deleted tips = %d, want 25", deleted)
			}
			if len(statuses) < 2 || len(priorities) < 2 || !fractionalRank {
				t.Fatalf("representative final states: statuses=%#v priorities=%#v fractionalRank=%t", statuses, priorities, fractionalRank)
			}

			taskRefs := make([]string, 0, len(first.TaskIDs)+2)
			taskRefs = append(taskRefs, "rev-list", "--count")
			for _, taskID := range first.TaskIDs {
				taskRefs = append(taskRefs, "refs/workbook/tasks/"+taskID)
			}
			if got := strings.TrimSpace(runGit(t, first.Root, taskRefs...)); got != strconv.Itoa(500*20) {
				t.Fatalf("reachable task commits = %q, want %d", got, 500*20)
			}

			representativeIDs := []string{
				first.TaskIDs[0],
				first.Dependencies[0].Dependent,
				first.TombstonedTaskIDs[0],
			}
			operationTypes := make(map[core.OperationType]struct{})
			operationFields := make(map[string]struct{})
			for _, taskID := range representativeIDs {
				history := readFixtureHistory(t, first, taskID)
				if got, want := len(history), 20; got != want {
					t.Fatalf("task %q history count = %d, want %d", taskID, got, want)
				}
				assertFixtureHistoryHasNoNoopOperations(t, history)
				for _, commit := range history {
					for _, operation := range commit.Pack.Operations {
						operationTypes[operation.Type] = struct{}{}
						if operation.Field != "" {
							operationFields[operation.Field] = struct{}{}
						}
					}
				}
			}
			for _, want := range []core.OperationType{
				core.OperationTaskCreate,
				core.OperationFieldSet,
				core.OperationSetAdd,
				core.OperationSetRemove,
				core.OperationTaskTombstone,
			} {
				if _, found := operationTypes[want]; !found {
					t.Errorf("representative operation types = %#v, want %q", operationTypes, want)
				}
			}
			for _, want := range []string{"description", "status", "priority", "labels", "dependencies", "rank"} {
				if _, found := operationFields[want]; !found {
					t.Errorf("representative operation fields = %#v, want %q", operationFields, want)
				}
			}
		})
	}
}

func readFixtureHistory(t *testing.T, fixture Fixture, taskID string) []fixtureCommit {
	t.Helper()
	head := strings.TrimSpace(runGit(t, fixture.Root, "rev-parse", "refs/workbook/tasks/"+taskID))
	revisions := strings.Fields(runGit(t, fixture.Root, "rev-list", "--reverse", head))
	history := make([]fixtureCommit, 0, len(revisions))
	for _, revision := range revisions {
		commit, err := readFixtureCommit(context.Background(), fixture.Root, revision)
		if err != nil {
			t.Fatal(err)
		}
		history = append(history, commit)
	}
	return history
}

func assertFixtureHistoryHasNoNoopOperations(t *testing.T, history []fixtureCommit) {
	t.Helper()
	var parent *core.StateDocument
	for _, commit := range history {
		for _, operation := range commit.Pack.Operations {
			if parent != nil && fixtureOperationIsNoop(parent.Task, operation) {
				t.Errorf("task %q logical clock %d operation %#v is a no-op", commit.State.TaskID, commit.State.LogicalClock, operation)
			}
		}
		state := commit.State
		parent = &state
	}
}

func fixtureOperationIsNoop(task core.TaskData, operation core.Operation) bool {
	switch operation.Type {
	case core.OperationFieldSet:
		switch operation.Field {
		case "title":
			return task.Title == operation.Value
		case "description":
			return task.Description == operation.Value
		case "status":
			return task.Status == core.Status(operation.Value)
		case "priority":
			return task.Priority == core.Priority(operation.Value)
		case "rank":
			return task.Rank == operation.Value
		}
	case core.OperationSetAdd:
		if operation.Field == "labels" {
			return containsString(task.Labels, operation.Value)
		}
		if operation.Field == "dependencies" {
			return containsString(task.Dependencies, operation.Value)
		}
	case core.OperationSetRemove:
		if operation.Field == "labels" {
			return !containsString(task.Labels, operation.Value)
		}
		if operation.Field == "dependencies" {
			return !containsString(task.Dependencies, operation.Value)
		}
	case core.OperationTaskTombstone:
		return task.Deleted
	case core.OperationTaskRestore:
		return !task.Deleted
	}
	return false
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
				testenv.MissingCapability(t, "Git does not support SHA-256 repositories")
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
