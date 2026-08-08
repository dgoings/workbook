package perf

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestColdNextScenarioMeasuresTheFetchItPerformsBeforeAnswering pins the three
// properties that make cli-next worth measuring separately from cli-list: the
// measured command leaves automatic synchronization enabled, because `next`
// fetches before answering so two agents cannot claim the same task; it runs
// against a repository whose origin already holds the fixture's refs, so the
// sample covers the steady-state fetch rather than an initial publication; and
// the board actually holds an acquirable task when the timed command runs.
//
// That last one is the easiest to get wrong. `next` only ever selects a task
// whose status is `ready`, and the fixture's generator never leaves one there,
// so a scenario that skipped the setup mutation would measure a search that
// always comes up empty and publish it as the agent's acquire step.
//
// Mutation witnesses: adding `--no-sync` to the measured arguments would hide
// the fetch inside a local budget and make the scenario a duplicate of cli-list;
// dropping the setup update would measure an empty acquire; and dropping the
// projection re-settle would charge the timed command for refreshing the head
// the setup moved.
func TestColdNextScenarioMeasuresTheFetchItPerformsBeforeAnswering(t *testing.T) {
	fixture := testColdCLIFixture()
	var events []string
	var commands []CommandSpec
	var originAtMeasure string
	dependencies := scenarioDependencies{
		buildFixture: func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
			if err := initBenchmarkWorktree(t, root); err != nil {
				return Fixture{}, err
			}
			fixture.Root = root
			return fixture, nil
		},
		prepareProjection: func(_ context.Context, command CommandSpec, totalTasks int) error {
			events = append(events, "prepare")
			if want := []string{"rebuild", "--json"}; !reflect.DeepEqual(command.Args, want) {
				t.Fatalf("projection command = %#v, want %#v", command.Args, want)
			}
			if totalTasks != 11 {
				t.Fatalf("projection task oracle = %d, want the fixture's 11", totalTasks)
			}
			return nil
		},
		measureCommand: func(_ context.Context, command CommandSpec) Sample {
			events = append(events, "measure")
			commands = append(commands, command)
			originAtMeasure = gitConfigValue(t, command.Directory, "remote.origin.url")
			return Sample{ExitCode: 0}
		},
	}
	spec := RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        FixtureSpec{TotalTasks: 11, ActiveTasks: 10, TombstonedTasks: 1, OperationsPerTask: 4, ObjectFormat: "sha1"},
		Samples:        1,
		CommandTimeout: 10 * time.Second,
	}

	results, err := runColdCLI(context.Background(), spec, t.TempDir(), []string{"cli-next"}, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "cli-next" || results[0].Surface != "cold-cli" {
		t.Fatalf("cold next results = %#v, want one cold-cli cli-next result", results)
	}
	// The projection is settled once by the harness before the scenario runs,
	// and once more by the scenario after its setup mutation moved a head.
	if want := []string{"prepare", "measure", "prepare", "measure"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("cold next lifecycle = %#v, want %#v", events, want)
	}
	if len(commands) != 2 {
		t.Fatalf("commands = %d, want a setup update and the measured next", len(commands))
	}
	wantSetup := []string{"update", fixture.ActiveTaskIDs[0], "--status", "ready", "--no-sync", "--json"}
	if !reflect.DeepEqual(commands[0].Args, wantSetup) {
		t.Fatalf("setup args = %#v, want %#v", commands[0].Args, wantSetup)
	}
	if want := []string{"next", "--json"}; !reflect.DeepEqual(commands[1].Args, want) {
		t.Fatalf("measured args = %#v, want %#v", commands[1].Args, want)
	}
	if originAtMeasure == "" {
		t.Fatal("no origin remote was configured before the measured sample")
	}
}

// TestColdNextScenarioFailsWhenNoTaskCanBeAcquired keeps a broken setup from
// being reported as a fast acquire. If the mutation that makes a task `ready`
// fails, the board holds nothing for `next` to select, and the sample that
// followed would be evidence about an empty search rather than about the agent
// hot loop.
func TestColdNextScenarioFailsWhenNoTaskCanBeAcquired(t *testing.T) {
	fixture := testColdCLIFixture()
	measured := 0
	dependencies := scenarioDependencies{
		buildFixture: func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
			fixture.Root = root
			return fixture, nil
		},
		prepareProjection: func(context.Context, CommandSpec, int) error { return nil },
		measureCommand: func(_ context.Context, command CommandSpec) Sample {
			measured++
			return Sample{ExitCode: 5, Error: "status is not a recognized value"}
		},
	}
	spec := RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        FixtureSpec{TotalTasks: 11, ActiveTasks: 10, TombstonedTasks: 1, OperationsPerTask: 4, ObjectFormat: "sha1"},
		Samples:        1,
		CommandTimeout: time.Second,
	}

	_, err := runColdCLI(context.Background(), spec, t.TempDir(), []string{"cli-next"}, dependencies)
	if err == nil || !strings.Contains(err.Error(), "prepare an acquirable task") {
		t.Fatalf("error = %v, want a refusal to measure a board with nothing to acquire", err)
	}
	if measured != 1 {
		t.Fatalf("commands run = %d, want the scenario to stop after the failed setup", measured)
	}
}

// TestColdNextScenarioCarriesTheSynchronizedBudget records the deliberate target
// choice. `next` fetches before answering, so holding it to the 200 ms local
// budget would publish a classification the command cannot meet by design and
// would hide the round trip instead of pricing it.
func TestColdNextScenarioCarriesTheSynchronizedBudget(t *testing.T) {
	want := ScenarioTarget{DurationStatistic: DurationP95, DurationComparison: DurationAtMost, MaxMilliseconds: 1000}
	for _, result := range coldCLIResults(1) {
		if result.Name != "cli-next" {
			continue
		}
		if result.Target == nil || *result.Target != want {
			t.Fatalf("cli-next target = %#v, want the synchronized budget %#v", result.Target, want)
		}
		return
	}
	t.Fatal("cli-next is not a registered cold CLI scenario")
}

// TestColdShowScenarioMeasuresOneLocalTaskRead pins that the agent's read step
// is measured against a real task ID from the fixture and performs no
// synchronization: `workbook show` opens a read-only service, so its cost is the
// local class and nothing about the measurement should suggest otherwise.
func TestColdShowScenarioMeasuresOneLocalTaskRead(t *testing.T) {
	fixture := testColdCLIFixture()
	var events []string
	var measured CommandSpec
	dependencies := scenarioDependencies{
		buildFixture: func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
			events = append(events, "build")
			fixture.Root = root
			return fixture, nil
		},
		prepareProjection: func(context.Context, CommandSpec, int) error {
			events = append(events, "prepare")
			return nil
		},
		measureCommand: func(_ context.Context, command CommandSpec) Sample {
			events = append(events, "measure")
			measured = command
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

	results, err := runColdCLI(context.Background(), spec, t.TempDir(), []string{"cli-show"}, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"build", "prepare", "measure", "cleanup"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("cold show lifecycle = %#v, want %#v", events, want)
	}
	if len(results) != 1 || results[0].Name != "cli-show" || results[0].Surface != "cold-cli" {
		t.Fatalf("cold show results = %#v, want one cold-cli cli-show result", results)
	}
	want := []string{"show", fixture.ActiveTaskIDs[0], "--json"}
	if !reflect.DeepEqual(measured.Args, want) {
		t.Fatalf("measured args = %#v, want %#v", measured.Args, want)
	}
}

// TestWarmTaskListScenarioReadsThePopulatedBoard pins that the warm read
// scenario measures a GET of the board's task collection and verifies the
// populated response, so a server that answered with an empty board could not
// be reported as a fast read.
func TestWarmTaskListScenarioReadsThePopulatedBoard(t *testing.T) {
	fixtureRoot := t.TempDir()
	fixtureSpec := FixtureSpec{TotalTasks: 10, ActiveTasks: 10, OperationsPerTask: 2, ObjectFormat: "sha1"}
	spec := RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        fixtureSpec,
		Samples:        2,
		CommandTimeout: time.Second,
	}
	var readActiveTasks []int
	closedServers := 0
	dependencies := warmHTTPDependencies{
		buildFixture: func(_ context.Context, root string, got FixtureSpec) (Fixture, error) {
			taskIDs := make([]string, got.ActiveTasks)
			for index := range taskIDs {
				taskIDs[index] = "WB-task"
			}
			return Fixture{Root: root, TaskIDs: taskIDs, ActiveTaskIDs: taskIDs}, nil
		},
		startServer: func(context.Context, string, string, time.Duration) (warmScenarioServer, error) {
			return &recordingTaskListServer{activeTasks: &readActiveTasks, closed: &closedServers}, nil
		},
	}

	results, err := runWarmHTTP(context.Background(), spec, fixtureRoot, []string{"api-tasks"}, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "api-tasks" || results[0].Surface != "warm-http" {
		t.Fatalf("warm read results = %#v, want one warm-http api-tasks result", results)
	}
	if len(results[0].Samples) != 2 {
		t.Fatalf("warm read samples = %d, want 2", len(results[0].Samples))
	}
	if want := []int{10, 10}; !reflect.DeepEqual(readActiveTasks, want) {
		t.Fatalf("verified active-task populations = %#v, want %#v", readActiveTasks, want)
	}
	if closedServers != 2 {
		t.Fatalf("closed servers = %d, want 2", closedServers)
	}
}

// TestWarmTaskListScenarioHasNoApprovedDurationTarget keeps the new read
// surface descriptive. The 100 ms warm budget was approved for a mutation, and
// attaching it to a whole-board read would publish a pass/fail classification
// nobody approved.
func TestWarmTaskListScenarioHasNoApprovedDurationTarget(t *testing.T) {
	for _, result := range warmHTTPResults(1) {
		if result.Name != "api-tasks" {
			continue
		}
		if result.Target != nil {
			t.Fatalf("api-tasks target = %#v, want no approved target", result.Target)
		}
		return
	}
	t.Fatal("api-tasks is not a registered warm HTTP scenario")
}

type recordingTaskListServer struct {
	activeTasks *[]int
	closed      *int
}

func (server *recordingTaskListServer) prepareProjection(_ context.Context, activeTasks int, _ time.Duration) error {
	return nil
}

func (server *recordingTaskListServer) measureTaskList(_ context.Context, activeTasks int, _ time.Duration) (Sample, error) {
	*server.activeTasks = append(*server.activeTasks, activeTasks)
	return Sample{ExitCode: 0}, nil
}

func (server *recordingTaskListServer) measureStatus(context.Context, string, string, time.Duration) (Sample, error) {
	return Sample{}, nil
}

func (server *recordingTaskListServer) measureIndependentBurst(context.Context, []string, string, time.Duration) (Sample, error) {
	return Sample{}, nil
}

func (server *recordingTaskListServer) measureSameTaskBurst(context.Context, string, int, time.Duration) (Sample, error) {
	return Sample{}, nil
}

func (server *recordingTaskListServer) close(time.Duration) error {
	*server.closed++
	return nil
}
