package perf

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// Mutation witness: measuring the list read path without the untimed rebuild
// would fold projection bootstrap into the reported latency, and measuring any
// command other than `list --json` would not describe the read surface at all.
func TestColdListScenarioRebuildsProjectionBeforeTheTimedListCommand(t *testing.T) {
	var events []string
	var prepareArgs, measureArgs []string
	dependencies := scenarioDependencies{
		buildFixture: func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
			events = append(events, "build")
			fixture := testColdCLIFixture()
			fixture.Root = root
			return fixture, nil
		},
		prepareProjection: func(_ context.Context, command CommandSpec, _ int) error {
			events = append(events, "prepare")
			prepareArgs = command.Args
			return nil
		},
		measureCommand: func(_ context.Context, command CommandSpec) Sample {
			events = append(events, "measure")
			measureArgs = command.Args
			return Sample{ExitCode: 0}
		},
		cleanupFixture: func(string) error {
			events = append(events, "cleanup")
			return nil
		},
	}
	spec := RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        FixtureSpec{TotalTasks: 11, ActiveTasks: 10, TombstonedTasks: 1, OperationsPerTask: 4, ObjectFormat: "sha1"},
		Samples:        1,
		CommandTimeout: time.Second,
	}

	results, err := runColdCLI(context.Background(), spec, t.TempDir(), []string{"cli-list"}, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"build", "prepare", "measure", "cleanup"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("cold list lifecycle = %#v, want %#v", events, want)
	}
	if want := []string{"rebuild", "--json"}; !reflect.DeepEqual(prepareArgs, want) {
		t.Fatalf("untimed setup command = %#v, want %#v", prepareArgs, want)
	}
	if want := []string{"list", "--json"}; !reflect.DeepEqual(measureArgs, want) {
		t.Fatalf("measured command = %#v, want %#v", measureArgs, want)
	}
	if len(results) != 1 || results[0].Name != "cli-list" || results[0].Surface != "cold-cli" {
		t.Fatalf("cold list results = %#v, want one cold-cli cli-list result", results)
	}
}

// Mutation witness: attaching the approved 200 ms single-mutation budget to the
// new read path would publish a pass/fail classification nobody approved.
func TestColdListScenarioHasNoApprovedDurationTarget(t *testing.T) {
	for _, result := range coldCLIResults(1) {
		if result.Name != "cli-list" {
			continue
		}
		if result.Target != nil {
			t.Fatalf("cli-list target = %#v, want no approved target", result.Target)
		}
		return
	}
	t.Fatal("cli-list is not a registered cold CLI scenario")
}
