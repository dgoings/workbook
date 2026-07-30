package perf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/gitstore"
)

func TestRunColdCLIIsolatesSelectedScenarioSamplesAndPreparesProjection(t *testing.T) {
	var mutex sync.Mutex
	var fixtureRoots []string
	var events []string
	fixture := testColdCLIFixture()
	dependencies := scenarioDependencies{
		buildFixture: func(_ context.Context, root string, spec FixtureSpec) (Fixture, error) {
			mutex.Lock()
			fixtureRoots = append(fixtureRoots, root)
			events = append(events, "build "+filepath.Base(root))
			mutex.Unlock()
			if err := os.MkdirAll(root, 0o755); err != nil {
				return Fixture{}, err
			}
			if err := os.WriteFile(filepath.Join(root, "fixture-sentinel"), []byte("fixture"), 0o644); err != nil {
				return Fixture{}, err
			}
			fixture.Root = root
			return fixture, nil
		},
		prepareProjection: func(_ context.Context, command CommandSpec, totalTasks int) error {
			if !reflect.DeepEqual(command.Args, []string{"rebuild", "--json"}) {
				t.Errorf("prepare args = %q, want rebuild JSON", command.Args)
			}
			if totalTasks != 11 {
				t.Errorf("prepare total tasks = %d, want 11", totalTasks)
			}
			mutex.Lock()
			events = append(events, "prepare "+filepath.Base(command.Directory))
			mutex.Unlock()
			return nil
		},
		measureCommand: func(_ context.Context, spec CommandSpec) Sample {
			mutex.Lock()
			events = append(events, "measure "+filepath.Base(spec.Directory))
			mutex.Unlock()
			return Sample{ExitCode: 0, GitProcesses: 1}
		},
		cleanupFixture: func(root string) error {
			mutex.Lock()
			events = append(events, "cleanup "+filepath.Base(root))
			mutex.Unlock()
			return os.RemoveAll(root)
		},
	}
	spec := RunSpec{
		WorkbookBinary: "workbook",
		Fixture: FixtureSpec{
			TotalTasks: 11, ActiveTasks: 10, TombstonedTasks: 1,
			OperationsPerTask: 4,
			ObjectFormat:      "sha1",
		},
		Samples:        2,
		CommandTimeout: time.Second,
	}
	fixtureRoot := t.TempDir()

	results, err := runColdCLI(context.Background(), spec, fixtureRoot, []string{"cli-delete", "cli-restore"}, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if len(result.Samples) != 2 {
			t.Errorf("%s samples = %d, want 2", result.Name, len(result.Samples))
		}
	}

	if got, want := len(fixtureRoots), 4; got != want {
		t.Fatalf("fixture builds = %d, want %d", got, want)
	}
	uniqueRoots := make(map[string]struct{}, len(fixtureRoots))
	for _, root := range fixtureRoots {
		uniqueRoots[root] = struct{}{}
	}
	if got, want := len(uniqueRoots), 4; got != want {
		t.Fatalf("unique fixture roots = %d, want %d", got, want)
	}
	for _, root := range fixtureRoots {
		if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("fixture root %q still exists after sample cleanup: %v", root, err)
		}
	}
	want := []string{
		"build cli-delete", "prepare cli-delete", "measure cli-delete", "cleanup cli-delete",
		"build cli-restore", "prepare cli-restore", "measure cli-restore", "cleanup cli-restore",
		"build cli-delete", "prepare cli-delete", "measure cli-delete", "cleanup cli-delete",
		"build cli-restore", "prepare cli-restore", "measure cli-restore", "cleanup cli-restore",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("lifecycle events = %#v, want %#v", events, want)
	}
}

func TestRunColdCLICleansFixtureOnSetupAndMeasurementErrors(t *testing.T) {
	tests := []struct {
		name       string
		buildErr   error
		prepareErr error
		activeIDs  []string
		wantEvents []string
		wantErr    string
	}{
		{
			name:       "build failure",
			buildErr:   errors.New("fixture import failed"),
			wantEvents: []string{"build", "cleanup"},
			wantErr:    "fixture import failed",
		},
		{
			name:       "projection preparation failure",
			prepareErr: errors.New("rebuild failed"),
			activeIDs:  []string{"WB-00"},
			wantEvents: []string{"build", "prepare", "cleanup"},
			wantErr:    "rebuild failed",
		},
		{
			name:       "measurement allocation failure",
			activeIDs:  []string{},
			wantEvents: []string{"build", "prepare", "cleanup"},
			wantErr:    "fixture has 0 active tasks",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var events []string
			cleanupErr := errors.New("cleanup failed")
			dependencies := scenarioDependencies{
				buildFixture: func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
					events = append(events, "build")
					return Fixture{Root: root, ActiveTaskIDs: test.activeIDs}, test.buildErr
				},
				prepareProjection: func(context.Context, CommandSpec, int) error {
					events = append(events, "prepare")
					return test.prepareErr
				},
				measureCommand: func(context.Context, CommandSpec) Sample {
					events = append(events, "measure")
					return Sample{ExitCode: 0}
				},
				cleanupFixture: func(string) error {
					events = append(events, "cleanup")
					return cleanupErr
				},
			}
			spec := RunSpec{
				WorkbookBinary: "workbook",
				Fixture: FixtureSpec{
					TotalTasks: 1, ActiveTasks: 1,
					OperationsPerTask: 2,
					ObjectFormat:      "sha1",
				},
				Samples:        1,
				CommandTimeout: time.Second,
			}

			_, err := runColdCLI(context.Background(), spec, t.TempDir(), []string{"cli-update"}, dependencies)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) || !strings.Contains(err.Error(), cleanupErr.Error()) {
				t.Fatalf("runColdCLI error = %v, want primary %q plus cleanup error", err, test.wantErr)
			}
			if !reflect.DeepEqual(events, test.wantEvents) {
				t.Fatalf("cold error lifecycle = %#v, want %#v", events, test.wantEvents)
			}
		})
	}
}

func TestRunColdCLISelectsOnlyRequestedScenario(t *testing.T) {
	var builds, prepares, measures []string
	dependencies := scenarioDependencies{
		buildFixture: func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
			builds = append(builds, filepath.Base(root))
			fixture := testColdCLIFixture()
			fixture.Root = root
			return fixture, nil
		},
		prepareProjection: func(_ context.Context, command CommandSpec, _ int) error {
			prepares = append(prepares, filepath.Base(command.Directory))
			return nil
		},
		measureCommand: func(_ context.Context, command CommandSpec) Sample {
			measures = append(measures, filepath.Base(command.Directory))
			return Sample{ExitCode: 0}
		},
	}
	spec := RunSpec{WorkbookBinary: "workbook", Fixture: FixtureSpec{TotalTasks: 11, ActiveTasks: 10, TombstonedTasks: 1, OperationsPerTask: 4, ObjectFormat: "sha1"}, Samples: 1, CommandTimeout: time.Second}

	results, err := runColdCLI(context.Background(), spec, t.TempDir(), []string{"cli-update"}, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "cli-update" {
		t.Fatalf("selected results = %#v, want cli-update only", results)
	}
	if !reflect.DeepEqual(builds, []string{"cli-update"}) || !reflect.DeepEqual(prepares, builds) || !reflect.DeepEqual(measures, builds) {
		t.Fatalf("selected lifecycle builds=%#v prepares=%#v measures=%#v, want cli-update only", builds, prepares, measures)
	}
}

func TestRunColdCLIUsesFixtureTombstoneAndDirectDependency(t *testing.T) {
	fixture := testColdCLIFixture()
	var commands []CommandSpec
	dependencies := scenarioDependencies{
		buildFixture: func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
			fixture.Root = root
			return fixture, nil
		},
		prepareProjection: func(context.Context, CommandSpec, int) error { return nil },
		measureCommand: func(_ context.Context, command CommandSpec) Sample {
			commands = append(commands, command)
			return Sample{ExitCode: 0}
		},
	}
	spec := RunSpec{WorkbookBinary: "workbook", Fixture: FixtureSpec{TotalTasks: 11, ActiveTasks: 10, TombstonedTasks: 1, OperationsPerTask: 4, ObjectFormat: "sha1"}, Samples: 1, CommandTimeout: time.Second}

	_, err := runColdCLI(context.Background(), spec, t.TempDir(), []string{"cli-free", "cli-restore"}, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := commands[0].Args, []string{"free", fixture.Dependencies[0].Dependent, fixture.Dependencies[0].Dependency, "--json"}; !reflect.DeepEqual(got, want) {
		t.Errorf("free args = %#v, want direct fixture dependency %#v", got, want)
	}
	if got, want := commands[1].Args, []string{"restore", fixture.TombstonedTaskIDs[0], "--json"}; !reflect.DeepEqual(got, want) {
		t.Errorf("restore args = %#v, want fixture tombstone %#v", got, want)
	}
}

func TestPrepareProjectionValidatesRebuildEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantErr string
	}{
		{name: "correct rebuild", output: `{"format":"workbook.result","version":1,"command":"rebuild","data":{"taskCount":11}}`},
		{name: "wrong format", output: `{"format":"workbook.error","version":1,"command":"rebuild","data":{"taskCount":11}}`, wantErr: "format"},
		{name: "wrong command", output: `{"format":"workbook.result","version":1,"command":"list","data":{"taskCount":11}}`, wantErr: "command"},
		{name: "wrong task count", output: `{"format":"workbook.result","version":1,"command":"rebuild","data":{"taskCount":10}}`, wantErr: "task count"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "workbook")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' '"+test.output+"'\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			err := prepareProjection(context.Background(), CommandSpec{Binary: binary, Directory: t.TempDir(), Timeout: time.Second}, 11)
			if test.wantErr == "" && err != nil {
				t.Fatalf("prepare projection: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("prepare projection error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func testColdCLIFixture() Fixture {
	active := []string{"WB-00", "WB-01", "WB-02", "WB-03", "WB-04", "WB-05", "WB-06", "WB-07", "WB-08", "WB-09"}
	return Fixture{
		TaskIDs:           append(append([]string(nil), active...), "WB-deleted"),
		ActiveTaskIDs:     active,
		TombstonedTaskIDs: []string{"WB-deleted"},
		Dependencies:      []FixtureDependency{{Dependent: "WB-01", Dependency: "WB-00"}},
	}
}

func TestWarmScenarioTaskAllocationUsesTenTaskFixture(t *testing.T) {
	taskIDs := []string{
		"WB-00", "WB-01", "WB-02", "WB-03", "WB-04",
		"WB-05", "WB-06", "WB-07", "WB-08", "WB-09",
	}

	warm, err := allocateWarmHTTPTasks(taskIDs)
	if err != nil {
		t.Fatalf("allocate warm HTTP tasks: %v", err)
	}
	if !reflect.DeepEqual(warm.independent, taskIDs) {
		t.Errorf("warm independent tasks = %#v, want all ten fixture tasks", warm.independent)
	}
}

func TestColdCLISampleFailureAllowsTimeoutsAndRejectsOtherFailures(t *testing.T) {
	tests := []struct {
		name   string
		sample Sample
		want   bool
	}{
		{name: "success", sample: Sample{ExitCode: 0}, want: false},
		{name: "timeout", sample: Sample{ExitCode: -1, TimedOut: true, Error: "signal: killed"}, want: false},
		{name: "nonzero exit", sample: Sample{ExitCode: 2, Error: "invalid invocation"}, want: true},
		{name: "immediate error", sample: Sample{ExitCode: 0, Error: "exec format error"}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := coldCLISampleFailed(test.sample); got != test.want {
				t.Fatalf("coldCLISampleFailed(%#v) = %t, want %t", test.sample, got, test.want)
			}
		})
	}
}

func coldCLISampleFailed(sample Sample) bool {
	return !sample.TimedOut && (sample.ExitCode != 0 || sample.Error != "")
}

func TestRunColdCLI(t *testing.T) {
	binary := buildWorkbookBinary(t)
	spec := RunSpec{
		WorkbookBinary: binary,
		Fixture: FixtureSpec{
			TotalTasks: 40, ActiveTasks: 39, TombstonedTasks: 1,
			OperationsPerTask: 4,
			ObjectFormat:      "sha1",
		},
		Samples:        1,
		CommandTimeout: 60 * time.Second,
	}

	results, err := RunColdCLI(context.Background(), spec, filepath.Join(t.TempDir(), "fixture"), []string{
		"cli-create", "cli-delete", "cli-depend", "cli-free", "cli-move", "cli-restore", "cli-update", "cli-burst-independent-10", "cli-burst-same-task-10",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"cli-create",
		"cli-delete",
		"cli-depend",
		"cli-free",
		"cli-move",
		"cli-restore",
		"cli-update",
		"cli-burst-independent-10",
		"cli-burst-same-task-10",
	}
	got := make([]string, len(results))
	for i, result := range results {
		got[i] = result.Name
		if result.Surface != "cold-cli" {
			t.Errorf("%s surface = %q, want cold-cli", result.Name, result.Surface)
		}
		if len(result.Samples) != 1 {
			t.Errorf("%s samples = %d, want 1", result.Name, len(result.Samples))
			continue
		}
		sample := result.Samples[0]
		if coldCLISampleFailed(sample) {
			t.Errorf("%s sample = %#v, want success or timeout", result.Name, sample)
			continue
		}
		if !sample.TimedOut && sample.GitProcesses < 1 {
			t.Errorf("%s Git processes = %d, want at least 1", result.Name, sample.GitProcesses)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scenario names = %#v, want %#v", got, want)
	}
}

func TestRunWarmHTTP(t *testing.T) {
	binary := buildWorkbookBinary(t)
	spec := RunSpec{
		WorkbookBinary: binary,
		Fixture: FixtureSpec{
			TotalTasks: 40, ActiveTasks: 40,
			OperationsPerTask: 4,
			ObjectFormat:      "sha1",
		},
		Samples:        1,
		CommandTimeout: 60 * time.Second,
	}

	results, err := RunWarmHTTP(context.Background(), spec, filepath.Join(t.TempDir(), "fixture"), []string{
		"api-update", "api-burst-independent-10", "api-burst-same-task-10",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"api-update",
		"api-burst-independent-10",
		"api-burst-same-task-10",
	}
	got := make([]string, len(results))
	for i, result := range results {
		got[i] = result.Name
		if result.Surface != "warm-http" {
			t.Errorf("%s surface = %q, want warm-http", result.Name, result.Surface)
		}
		if len(result.Samples) != 1 {
			t.Errorf("%s samples = %d, want 1", result.Name, len(result.Samples))
			continue
		}
		sample := result.Samples[0]
		if sample.ExitCode != 0 || sample.TimedOut || sample.Error != "" {
			t.Errorf("%s sample = %#v, want success", result.Name, sample)
			continue
		}
		if sample.GitProcesses < 1 {
			t.Errorf("%s Git processes = %d, want at least 1", result.Name, sample.GitProcesses)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scenario names = %#v, want %#v", got, want)
	}
}

func TestRunWarmHTTPIsolatesEveryScenarioSampleAndRetainsMeasuredMisses(t *testing.T) {
	fixtureRoot := t.TempDir()
	fixtureSpec := FixtureSpec{
		TotalTasks: 10, ActiveTasks: 10,
		OperationsPerTask: 2,
		ObjectFormat:      "sha1",
	}
	spec := RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        fixtureSpec,
		Samples:        2,
		CommandTimeout: time.Second,
	}
	var fixtureRoots []string
	var serverRoots []string
	closedServers := 0
	dependencies := warmHTTPDependencies{
		buildFixture: func(_ context.Context, root string, got FixtureSpec) (Fixture, error) {
			if !reflect.DeepEqual(got, fixtureSpec) {
				t.Fatalf("fixture spec = %#v, want exact requested spec %#v", got, fixtureSpec)
			}
			fixtureRoots = append(fixtureRoots, root)
			taskIDs := make([]string, got.ActiveTasks)
			for index := range taskIDs {
				taskIDs[index] = fmt.Sprintf("WB-%02d", index)
			}
			return Fixture{Root: root, TaskIDs: taskIDs, ActiveTaskIDs: taskIDs}, nil
		},
		startServer: func(_ context.Context, _ string, root string, timeout time.Duration) (warmScenarioServer, error) {
			if timeout != time.Second {
				t.Fatalf("server timeout = %s, want 1s", timeout)
			}
			serverRoots = append(serverRoots, root)
			return &recordingWarmScenarioServer{
				t:           t,
				role:        filepath.Base(root),
				sample:      filepath.Base(filepath.Dir(root)),
				closedCount: &closedServers,
			}, nil
		},
	}

	results, err := runWarmHTTP(context.Background(), spec, fixtureRoot, []string{
		"api-update", "api-burst-independent-10", "api-burst-same-task-10",
	}, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	wantRoots := []string{
		filepath.Join(fixtureRoot, "sample-001", "api-update"),
		filepath.Join(fixtureRoot, "sample-001", "api-burst-independent-10"),
		filepath.Join(fixtureRoot, "sample-001", "api-burst-same-task-10"),
		filepath.Join(fixtureRoot, "sample-002", "api-update"),
		filepath.Join(fixtureRoot, "sample-002", "api-burst-independent-10"),
		filepath.Join(fixtureRoot, "sample-002", "api-burst-same-task-10"),
	}
	if !reflect.DeepEqual(fixtureRoots, wantRoots) {
		t.Fatalf("fixture roots = %#v, want unique scenario/sample roots %#v", fixtureRoots, wantRoots)
	}
	if !reflect.DeepEqual(serverRoots, wantRoots) {
		t.Fatalf("server roots = %#v, want one warmed server per fixture %#v", serverRoots, wantRoots)
	}
	if closedServers != 6 {
		t.Fatalf("closed servers = %d, want 6", closedServers)
	}

	wantNames := []string{"api-update", "api-burst-independent-10", "api-burst-same-task-10"}
	for index, result := range results {
		if result.Name != wantNames[index] || len(result.Samples) != 2 {
			t.Fatalf("result %d = %#v, want %q with two samples", index, result, wantNames[index])
		}
	}
	if !results[1].Samples[0].TimedOut || results[1].Samples[0].ExitCode != -1 {
		t.Fatalf("sample 1 independent outcome = %#v, want retained timeout", results[1].Samples[0])
	}
	if results[1].Samples[1].ExitCode != http.StatusConflict ||
		!strings.Contains(results[1].Samples[1].Error, "task head changed") {
		t.Fatalf("sample 2 independent outcome = %#v, want retained HTTP conflict", results[1].Samples[1])
	}
	for _, sample := range results[2].Samples {
		if !sampleSucceeded(sample) {
			t.Fatalf("isolated same-task sample = %#v, want success unaffected by independent ambiguity", sample)
		}
	}

	var output bytes.Buffer
	report := Report{
		Format:    ReportFormat,
		Version:   ReportVersion,
		Phase:     "baseline",
		Fixture:   fixtureSpec,
		Scenarios: results,
	}
	if err := report.WriteJSON(&output); err != nil {
		t.Fatal(err)
	}
	var written Report
	if err := json.Unmarshal(output.Bytes(), &written); err != nil {
		t.Fatal(err)
	}
	var writtenConflict Sample
	for _, scenario := range written.Scenarios {
		if scenario.Name == "api-burst-independent-10" {
			writtenConflict = scenario.Samples[1]
		}
	}
	if writtenConflict.ExitCode != http.StatusConflict ||
		!strings.Contains(writtenConflict.Error, "task head changed") {
		t.Fatalf("written report conflict = %#v, want measured product evidence preserved", writtenConflict)
	}
}

// Mutation witness: starting every API scenario and filtering results afterward
// would create burst fixtures and servers even when only api-update is selected.
func TestRunWarmHTTPSelectsAndPreparesBeforeEveryMeasurement(t *testing.T) {
	fixtureRoot := t.TempDir()
	spec := RunSpec{
		WorkbookBinary: "workbook",
		Fixture: FixtureSpec{
			TotalTasks: 10, ActiveTasks: 10,
			OperationsPerTask: 2,
			ObjectFormat:      "sha1",
		},
		Samples:        2,
		CommandTimeout: time.Second,
	}
	var events []string
	var roots []string
	closedServers := 0
	dependencies := warmHTTPDependencies{
		buildFixture: func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
			events = append(events, "build "+filepath.Base(root))
			roots = append(roots, root)
			if err := os.MkdirAll(root, 0o755); err != nil {
				return Fixture{}, err
			}
			if err := os.WriteFile(filepath.Join(root, "fixture-sentinel"), []byte("fixture"), 0o644); err != nil {
				return Fixture{}, err
			}
			return Fixture{
				Root: root,
				ActiveTaskIDs: []string{
					"WB-00", "WB-01", "WB-02", "WB-03", "WB-04",
					"WB-05", "WB-06", "WB-07", "WB-08", "WB-09",
				},
			}, nil
		},
		startServer: func(_ context.Context, _ string, root string, _ time.Duration) (warmScenarioServer, error) {
			events = append(events, "start "+filepath.Base(root))
			return &recordingWarmScenarioServer{
				t:           t,
				role:        filepath.Base(root),
				sample:      filepath.Base(filepath.Dir(root)),
				events:      &events,
				closedCount: &closedServers,
			}, nil
		},
		cleanupFixture: func(root string) error {
			events = append(events, "cleanup "+filepath.Base(root))
			return os.RemoveAll(root)
		},
	}

	results, err := runWarmHTTP(context.Background(), spec, fixtureRoot, []string{"api-update"}, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "api-update" || len(results[0].Samples) != 2 {
		t.Fatalf("selected warm results = %#v, want only two api-update samples", results)
	}
	wantRoots := []string{
		filepath.Join(fixtureRoot, "sample-001", "api-update"),
		filepath.Join(fixtureRoot, "sample-002", "api-update"),
	}
	if !reflect.DeepEqual(roots, wantRoots) {
		t.Fatalf("warm fixture roots = %#v, want %#v", roots, wantRoots)
	}
	for _, root := range roots {
		if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("warm fixture root %q still exists after sample cleanup: %v", root, err)
		}
	}
	if closedServers != 2 {
		t.Fatalf("closed servers = %d, want 2", closedServers)
	}
	wantEvents := []string{
		"build api-update", "start api-update", "prepare api-update", "measure api-update", "close api-update", "cleanup api-update",
		"build api-update", "start api-update", "prepare api-update", "measure api-update", "close api-update", "cleanup api-update",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("warm lifecycle events = %#v, want %#v", events, wantEvents)
	}
}

func TestRunWarmHTTPCleansFixtureOnErrorPathsWithoutHidingPrimary(t *testing.T) {
	tests := []struct {
		name       string
		startErr   error
		prepareErr error
		measureErr error
		closeErr   error
		wantEvents []string
		wantErr    string
	}{
		{
			name:       "start failure",
			startErr:   errors.New("server start failed"),
			wantEvents: []string{"build", "start", "cleanup"},
			wantErr:    "server start failed",
		},
		{
			name:       "prepare failure",
			prepareErr: errors.New("task projection unavailable"),
			wantEvents: []string{"build", "start", "prepare api-update", "close api-update", "cleanup"},
			wantErr:    "task projection unavailable",
		},
		{
			name:       "measurement failure",
			measureErr: errors.New("malformed success response"),
			wantEvents: []string{"build", "start", "prepare api-update", "measure api-update", "close api-update", "cleanup"},
			wantErr:    "malformed success response",
		},
		{
			name:       "close failure",
			closeErr:   errors.New("server did not stop"),
			wantEvents: []string{"build", "start", "prepare api-update", "measure api-update", "close api-update", "cleanup"},
			wantErr:    "server did not stop",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var events []string
			closedServers := 0
			cleanupErr := errors.New("cleanup failed")
			dependencies := warmHTTPDependencies{
				buildFixture: func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
					events = append(events, "build")
					return Fixture{
						Root: root,
						ActiveTaskIDs: []string{
							"WB-00", "WB-01", "WB-02", "WB-03", "WB-04",
							"WB-05", "WB-06", "WB-07", "WB-08", "WB-09",
						},
					}, nil
				},
				startServer: func(context.Context, string, string, time.Duration) (warmScenarioServer, error) {
					events = append(events, "start")
					if test.startErr != nil {
						return nil, test.startErr
					}
					return &recordingWarmScenarioServer{
						t:           t,
						role:        "api-update",
						events:      &events,
						prepareErr:  test.prepareErr,
						measureErr:  test.measureErr,
						closeErr:    test.closeErr,
						closedCount: &closedServers,
					}, nil
				},
				cleanupFixture: func(string) error {
					events = append(events, "cleanup")
					return cleanupErr
				},
			}
			spec := RunSpec{
				WorkbookBinary: "workbook",
				Fixture: FixtureSpec{
					TotalTasks: 10, ActiveTasks: 10,
					OperationsPerTask: 2,
					ObjectFormat:      "sha1",
				},
				Samples:        1,
				CommandTimeout: time.Second,
			}

			_, err := runWarmHTTP(context.Background(), spec, t.TempDir(), []string{"api-update"}, dependencies)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) || !strings.Contains(err.Error(), cleanupErr.Error()) {
				t.Fatalf("runWarmHTTP error = %v, want primary %q plus cleanup error", err, test.wantErr)
			}
			if !reflect.DeepEqual(events, test.wantEvents) {
				t.Fatalf("warm error lifecycle = %#v, want %#v", events, test.wantEvents)
			}
		})
	}
}

func TestRunWarmHTTPClosesServerWhenProjectionPreparationFails(t *testing.T) {
	closedServers := 0
	dependencies := warmHTTPDependencies{
		buildFixture: func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
			return Fixture{Root: root, ActiveTaskIDs: []string{"WB-00"}}, nil
		},
		startServer: func(_ context.Context, _ string, _ string, _ time.Duration) (warmScenarioServer, error) {
			return &recordingWarmScenarioServer{
				t:           t,
				role:        "api-update",
				prepareErr:  errors.New("task projection unavailable"),
				closedCount: &closedServers,
			}, nil
		},
	}
	spec := RunSpec{
		WorkbookBinary: "workbook",
		Fixture: FixtureSpec{
			TotalTasks: 1, ActiveTasks: 1,
			OperationsPerTask: 2,
			ObjectFormat:      "sha1",
		},
		Samples:        1,
		CommandTimeout: time.Second,
	}

	_, err := runWarmHTTP(context.Background(), spec, t.TempDir(), []string{"api-update"}, dependencies)
	if err == nil || !strings.Contains(err.Error(), "prepare api-update") {
		t.Fatalf("preparation failure = %v, want contextual error", err)
	}
	if closedServers != 1 {
		t.Fatalf("closed servers = %d, want cleanup after failed preparation", closedServers)
	}
}

func TestWarmHTTPServerPrepareProjection(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    string
	}{
		{
			name:       "correct task envelope and count",
			statusCode: http.StatusOK,
			body:       `{"format":"workbook.tasks","version":1,"tasks":[{"id":"WB-1"},{"id":"WB-2"}]}`,
		},
		{
			name:       "wrong task count",
			statusCode: http.StatusOK,
			body:       `{"format":"workbook.tasks","version":1,"tasks":[{"id":"WB-1"}]}`,
			wantErr:    "task count = 1, want 2",
		},
		{
			name:       "malformed JSON",
			statusCode: http.StatusOK,
			body:       `{`,
			wantErr:    "decode task list response",
		},
		{
			name:       "wrong format",
			statusCode: http.StatusOK,
			body:       `{"format":"workbook.result","version":1,"tasks":[{"id":"WB-1"},{"id":"WB-2"}]}`,
			wantErr:    "task list response = \"workbook.result\" v1",
		},
		{
			name:       "wrong version",
			statusCode: http.StatusOK,
			body:       `{"format":"workbook.tasks","version":2,"tasks":[{"id":"WB-1"},{"id":"WB-2"}]}`,
			wantErr:    "task list response = \"workbook.tasks\" v2",
		},
		{
			name:       "non-OK response",
			statusCode: http.StatusServiceUnavailable,
			body:       `temporarily unavailable`,
			wantErr:    "HTTP 503 Service Unavailable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.URL.Path != "/api/tasks" {
					t.Fatalf("task preparation request = %s %s, want GET /api/tasks", request.Method, request.URL.Path)
				}
				writer.WriteHeader(test.statusCode)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer httpServer.Close()

			server := warmHTTPServer{baseURL: httpServer.URL, client: httpServer.Client()}
			err := server.prepareProjection(context.Background(), 2, time.Second)
			if test.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("preparation error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestWarmStatusDeadlineReturnsTimedOutSample(t *testing.T) {
	release := make(chan struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer httpServer.Close()
	defer close(release)

	server := warmHTTPServer{
		baseURL:   httpServer.URL,
		tracePath: emptyTraceFile(t),
		client:    httpServer.Client(),
	}
	sample, err := server.measureStatus(context.Background(), "WB-timeout", "ready", 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !sample.TimedOut || sample.ExitCode != -1 || !strings.Contains(sample.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("deadline sample = %#v, want retained timeout", sample)
	}
}

func TestWarmStatusNonOKResponseReturnsMeasuredSample(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "bad request", status: http.StatusBadRequest, body: "update does not change task"},
		{name: "conflict", status: http.StatusConflict, body: "task head changed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				http.Error(writer, test.body, test.status)
			}))
			defer httpServer.Close()

			server := warmHTTPServer{
				baseURL:   httpServer.URL,
				tracePath: emptyTraceFile(t),
				client:    httpServer.Client(),
			}
			sample, err := server.measureStatus(context.Background(), "WB-product-miss", "ready", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if sample.ExitCode != test.status || sample.TimedOut || sample.Duration <= 0 {
				t.Fatalf("HTTP %d sample = %#v, want retained nonzero measured outcome", test.status, sample)
			}
			if !strings.Contains(sample.Error, fmt.Sprintf("HTTP %d", test.status)) ||
				!strings.Contains(sample.Error, test.body) {
				t.Fatalf("HTTP %d sample error = %q, want status and body evidence", test.status, sample.Error)
			}
		})
	}
}

func TestWarmStatusMalformedSuccessAndCallerCancellationRemainFatal(t *testing.T) {
	t.Run("malformed HTTP 200", func(t *testing.T) {
		httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(writer, "{")
		}))
		defer httpServer.Close()

		server := warmHTTPServer{
			baseURL:   httpServer.URL,
			tracePath: emptyTraceFile(t),
			client:    httpServer.Client(),
		}
		_, err := server.measureStatus(context.Background(), "WB-malformed", "ready", time.Second)
		if err == nil || !strings.Contains(err.Error(), "decode status response") {
			t.Fatalf("malformed success error = %v, want fatal JSON decode error", err)
		}
	})

	t.Run("wrong HTTP 200 envelope", func(t *testing.T) {
		httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"format":  "workbook.task",
				"version": 1,
				"task": map[string]string{
					"id":     "WB-wrong-envelope",
					"status": "ready",
				},
			})
		}))
		defer httpServer.Close()

		server := warmHTTPServer{
			baseURL:   httpServer.URL,
			tracePath: emptyTraceFile(t),
			client:    httpServer.Client(),
		}
		_, err := server.measureStatus(context.Background(), "WB-wrong-envelope", "ready", time.Second)
		if err == nil || !strings.Contains(err.Error(), "status response") {
			t.Fatalf("wrong success envelope error = %v, want fatal protocol error", err)
		}
	})

	t.Run("caller cancellation", func(t *testing.T) {
		httpServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer httpServer.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		server := warmHTTPServer{
			baseURL:   httpServer.URL,
			tracePath: emptyTraceFile(t),
			client:    httpServer.Client(),
		}
		_, err := server.measureStatus(ctx, "WB-canceled", "ready", time.Second)
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancellation error = %v, want fatal context cancellation", err)
		}
	})
}

func TestWarmIndependentBurstIssuesTenDistinctRequestsAndCountsTraceOnce(t *testing.T) {
	tracePath := emptyTraceFile(t)
	var mutex sync.Mutex
	var requests []recordedStatusRequest
	allArrived := make(chan struct{})
	release := make(chan struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorded, err := readRecordedStatusRequest(request)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}

		mutex.Lock()
		requests = append(requests, recorded)
		if len(requests) == 10 {
			if err := appendTraceStarts(tracePath, 10); err != nil {
				mutex.Unlock()
				http.Error(writer, err.Error(), http.StatusInternalServerError)
				return
			}
			close(allArrived)
		}
		mutex.Unlock()

		select {
		case <-allArrived:
			writeRecordedStatusResponse(writer, recorded)
		case <-release:
		case <-request.Context().Done():
		}
	}))
	defer httpServer.Close()
	defer close(release)

	server := warmHTTPServer{
		baseURL:   httpServer.URL,
		tracePath: tracePath,
		client:    httpServer.Client(),
	}
	taskIDs := make([]string, 10)
	for index := range taskIDs {
		taskIDs[index] = fmt.Sprintf("WB-independent-%02d", index+1)
	}
	sample, err := server.measureIndependentBurst(context.Background(), taskIDs, "ready", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if sample.ExitCode != 0 || sample.TimedOut || sample.Error != "" {
		t.Fatalf("independent burst sample = %#v, want success", sample)
	}
	if sample.GitProcesses != 10 {
		t.Fatalf("independent burst Git processes = %d, want 10 unique Trace2 starts", sample.GitProcesses)
	}

	mutex.Lock()
	defer mutex.Unlock()
	if len(requests) != 10 {
		t.Fatalf("independent requests = %d, want 10", len(requests))
	}
	targets := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		targets[request.taskID] = struct{}{}
		if request.status != "ready" {
			t.Errorf("%s status = %q, want ready", request.taskID, request.status)
		}
	}
	if len(targets) != 10 {
		t.Fatalf("independent targets = %d, want 10", len(targets))
	}
}

func TestWarmSameTaskBurstStopsAfterAmbiguousOutcome(t *testing.T) {
	tests := []struct {
		name         string
		timeout      time.Duration
		writeOutcome func(http.ResponseWriter, *http.Request)
		wantExitCode int
		wantTimedOut bool
		wantEvidence string
	}{
		{
			name:         "timeout",
			timeout:      20 * time.Millisecond,
			wantExitCode: -1,
			wantTimedOut: true,
			wantEvidence: "timed out",
		},
		{
			name:    "HTTP non-success",
			timeout: time.Second,
			writeOutcome: func(writer http.ResponseWriter, _ *http.Request) {
				http.Error(writer, "task head changed", http.StatusConflict)
			},
			wantExitCode: http.StatusConflict,
			wantEvidence: "task head changed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracePath := emptyTraceFile(t)
			var mutex sync.Mutex
			requests := 0
			release := make(chan struct{})
			httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				mutex.Lock()
				requests++
				requestNumber := requests
				mutex.Unlock()
				if requestNumber == 1 {
					if err := appendTraceStarts(tracePath, 1); err != nil {
						http.Error(writer, err.Error(), http.StatusInternalServerError)
						return
					}
					recorded, err := readRecordedStatusRequest(request)
					if err != nil {
						http.Error(writer, err.Error(), http.StatusBadRequest)
						return
					}
					writeRecordedStatusResponse(writer, recorded)
					return
				}
				if requestNumber == 2 {
					if test.wantTimedOut {
						select {
						case <-request.Context().Done():
						case <-release:
						}
						return
					}
					test.writeOutcome(writer, request)
					return
				}
				http.Error(writer, "unexpected request after ambiguous outcome", http.StatusInternalServerError)
			}))
			defer httpServer.Close()
			defer close(release)

			server := warmHTTPServer{
				baseURL:   httpServer.URL,
				tracePath: tracePath,
				client:    httpServer.Client(),
			}
			sample, err := server.measureSameTaskBurst(
				context.Background(),
				"WB-same",
				0,
				test.timeout,
			)
			if err != nil {
				t.Fatal(err)
			}
			if sample.ExitCode != test.wantExitCode || sample.TimedOut != test.wantTimedOut ||
				!strings.Contains(sample.Error, "command 2") || !strings.Contains(sample.Error, test.wantEvidence) {
				t.Fatalf("same-task aggregate = %#v, want retained second-command outcome", sample)
			}
			mutex.Lock()
			defer mutex.Unlock()
			if requests != 2 {
				t.Fatalf("same-task requests = %d, want stop after ambiguous second outcome", requests)
			}
		})
	}
}

func TestWarmSameTaskBurstIssuesTenSequentialAlternatingRequests(t *testing.T) {
	tracePath := emptyTraceFile(t)
	var mutex sync.Mutex
	var requests []recordedStatusRequest
	active := 0
	maxActive := 0
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorded, err := readRecordedStatusRequest(request)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}

		mutex.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		requests = append(requests, recorded)
		traceErr := appendTraceStarts(tracePath, 1)
		mutex.Unlock()
		if traceErr != nil {
			mutex.Lock()
			active--
			mutex.Unlock()
			http.Error(writer, traceErr.Error(), http.StatusInternalServerError)
			return
		}
		writeRecordedStatusResponse(writer, recorded)
		mutex.Lock()
		active--
		mutex.Unlock()
	}))
	defer httpServer.Close()

	server := warmHTTPServer{
		baseURL:   httpServer.URL,
		tracePath: tracePath,
		client:    httpServer.Client(),
	}
	sample, err := server.measureSameTaskBurst(context.Background(), "WB-same", 0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if sample.ExitCode != 0 || sample.TimedOut || sample.Error != "" || sample.GitProcesses != 10 {
		t.Fatalf("same-task burst sample = %#v, want ten successful traced requests", sample)
	}

	mutex.Lock()
	defer mutex.Unlock()
	if len(requests) != 10 {
		t.Fatalf("same-task requests = %d, want 10", len(requests))
	}
	if maxActive != 1 {
		t.Fatalf("maximum concurrent same-task requests = %d, want 1", maxActive)
	}
	for index, request := range requests {
		if request.taskID != "WB-same" {
			t.Errorf("request %d task = %q, want WB-same", index+1, request.taskID)
		}
		if want := alternatingStatus(index); request.status != want {
			t.Errorf("request %d status = %q, want %q", index+1, request.status, want)
		}
	}
}

func TestWarmSameTaskBurstStartsWithLiteralStatusSafeForGeneratedFixtures(t *testing.T) {
	tests := []struct {
		name              string
		operationsPerTask int
		wantInitialStatus string
	}{
		{name: "two operations remain backlog", operationsPerTask: 2, wantInitialStatus: "backlog"},
		{name: "status operation sets in-progress", operationsPerTask: 3, wantInitialStatus: "in-progress"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, err := BuildFixture(context.Background(), filepath.Join(t.TempDir(), "fixture"), FixtureSpec{
				TotalTasks: 10, ActiveTasks: 10,
				OperationsPerTask: test.operationsPerTask,
				ObjectFormat:      "sha1",
			})
			if err != nil {
				t.Fatal(err)
			}
			repository, err := gitstore.Open(context.Background(), fixture.Root)
			if err != nil {
				t.Fatal(err)
			}
			taskID := fixture.TaskIDs[1]
			snapshot, err := repository.Get(context.Background(), fixture.Config, taskID)
			if err != nil {
				t.Fatal(err)
			}
			initialStatus := string(snapshot.State.Task.Status)
			if initialStatus != test.wantInitialStatus {
				t.Fatalf("generated fixture status = %q, want literal %q", initialStatus, test.wantInitialStatus)
			}

			tracePath := emptyTraceFile(t)
			var mutex sync.Mutex
			var firstRequestedStatus string
			httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				recorded, err := readRecordedStatusRequest(request)
				if err != nil {
					http.Error(writer, err.Error(), http.StatusBadRequest)
					return
				}
				mutex.Lock()
				if firstRequestedStatus == "" {
					firstRequestedStatus = recorded.status
				}
				mutex.Unlock()
				if err := appendTraceStarts(tracePath, 1); err != nil {
					http.Error(writer, err.Error(), http.StatusInternalServerError)
					return
				}
				writeRecordedStatusResponse(writer, recorded)
			}))
			defer httpServer.Close()

			server := warmHTTPServer{
				baseURL:   httpServer.URL,
				tracePath: tracePath,
				client:    httpServer.Client(),
			}
			sample, err := server.measureSameTaskBurst(context.Background(), taskID, 0, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if !sampleSucceeded(sample) {
				t.Fatalf("same-task burst sample = %#v, want success", sample)
			}

			mutex.Lock()
			defer mutex.Unlock()
			if firstRequestedStatus != "ready" {
				t.Fatalf("first same-task status = %q, want literal safe status %q", firstRequestedStatus, "ready")
			}
			if firstRequestedStatus == initialStatus {
				t.Fatalf("first same-task status %q matches generated fixture status", firstRequestedStatus)
			}
		})
	}
}

func TestMeasureRepository(t *testing.T) {
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			if objectFormat == "sha256" && !supportsObjectFormat(t, objectFormat) {
				return
			}
			if objectFormat == "sha256" {
				t.Setenv("GIT_CONFIG_COUNT", "1")
				t.Setenv("GIT_CONFIG_KEY_0", "init.defaultObjectFormat")
				t.Setenv("GIT_CONFIG_VALUE_0", "sha1")
			}
			binary := buildWorkbookBinary(t)
			fixture, err := BuildFixture(context.Background(), filepath.Join(t.TempDir(), "fixture"), FixtureSpec{
				TotalTasks: 10, ActiveTasks: 10,
				OperationsPerTask: 2,
				ObjectFormat:      objectFormat,
			})
			if err != nil {
				t.Fatal(err)
			}

			metrics, results, err := MeasureRepository(context.Background(), binary, fixture.Root, 60*time.Second)
			if err != nil {
				t.Fatal(err)
			}

			want := []string{
				"projection-rebuild",
				"projection-refresh-unchanged",
				"projection-refresh-one-changed",
				"sync-initial-local-bare",
				"sync-unchanged-local-bare",
			}
			got := make([]string, len(results))
			for i, result := range results {
				got[i] = result.Name
				if result.Surface != "repository" {
					t.Errorf("%s surface = %q, want repository", result.Name, result.Surface)
				}
				if len(result.Samples) != 1 {
					t.Errorf("%s samples = %d, want 1", result.Name, len(result.Samples))
					continue
				}
				sample := result.Samples[0]
				if sample.TimedOut && i >= 3 {
					continue
				}
				if i == 4 && results[3].Samples[0].TimedOut &&
					sample.Error == "not measured: initial sync timed out before remote completion" {
					continue
				}
				if sample.ExitCode != 0 || sample.TimedOut || sample.Error != "" {
					t.Errorf("%s sample = %#v, want success", result.Name, sample)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("scenario names = %#v, want %#v", got, want)
			}

			if metrics.LooseRefEnumerationMilliseconds <= 0 {
				t.Errorf("loose ref enumeration = %f ms, want positive", metrics.LooseRefEnumerationMilliseconds)
			}
			if metrics.PackedRefEnumerationMilliseconds <= 0 {
				t.Errorf("packed ref enumeration = %f ms, want positive", metrics.PackedRefEnumerationMilliseconds)
			}
			if metrics.LooseObjects <= 0 {
				t.Errorf("loose objects = %d, want positive", metrics.LooseObjects)
			}
			if metrics.LooseObjectBytes <= 0 {
				t.Errorf("loose object bytes = %d, want positive", metrics.LooseObjectBytes)
			}
			if metrics.PackedObjects <= 0 {
				t.Errorf("packed objects = %d, want positive", metrics.PackedObjects)
			}
			if metrics.PackBytes <= 0 {
				t.Errorf("pack bytes = %d, want positive", metrics.PackBytes)
			}
		})
	}
}

func TestMeasureLocalBareSyncAgainstNewOriginPreservesPackedRefs(t *testing.T) {
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			if objectFormat == "sha256" && !supportsObjectFormat(t, objectFormat) {
				return
			}
			binary := buildWorkbookBinary(t)
			fixture, err := BuildFixture(context.Background(), filepath.Join(t.TempDir(), "fixture"), FixtureSpec{
				TotalTasks: 10, ActiveTasks: 10,
				OperationsPerTask: 2,
				ObjectFormat:      objectFormat,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := runRepositoryGit(context.Background(), time.Minute, fixture.Root, "pack-refs", "--all"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := runRepositoryGit(context.Background(), time.Minute, fixture.Root, "gc"); err != nil {
				t.Fatal(err)
			}

			origin := filepath.Join(t.TempDir(), "origin.git")
			results, err := measureLocalBareSyncAgainstNewOrigin(
				context.Background(), binary, fixture.Root, origin, time.Minute, MeasureCommand,
			)
			if err != nil {
				t.Fatal(err)
			}
			wantNames := []string{"sync-initial-local-bare", "sync-unchanged-local-bare"}
			gotNames := make([]string, len(results))
			for i, result := range results {
				gotNames[i] = result.Name
				if len(result.Samples) != 1 {
					t.Errorf("%s samples = %d, want 1", result.Name, len(result.Samples))
					continue
				}
				sample := result.Samples[0]
				if sample.ExitCode != 0 || sample.TimedOut || sample.Error != "" {
					t.Errorf("%s sample = %#v, want success", result.Name, sample)
				}
			}
			if !reflect.DeepEqual(gotNames, wantNames) {
				t.Fatalf("scenario names = %#v, want %#v", gotNames, wantNames)
			}

			canonical := fixtureRefMap(t, fixture.Root, "refs/workbook/tasks/")
			remote := fixtureRemoteRefMap(t, origin)
			if !reflect.DeepEqual(remote, canonical) {
				t.Fatalf("remote task refs = %#v, want exact canonical refs %#v", remote, canonical)
			}
		})
	}
}

func TestMeasureProjectionScenariosRetainMeasuredProductMisses(t *testing.T) {
	repository := t.TempDir()
	samples := []Sample{
		{ExitCode: -1, TimedOut: true, Error: "rebuild timed out"},
		{ExitCode: 2, Error: "list failed"},
		{ExitCode: 0},
		{ExitCode: 3, Error: "changed list failed"},
	}
	wantArgs := [][]string{
		{"rebuild", "--json"},
		{"list", "--json"},
		{"update", "WB-task", "--status", "ready", "--json"},
		{"list", "--json"},
	}
	call := 0
	results, err := measureProjectionScenarios(
		context.Background(),
		"workbook",
		repository,
		"WB-task",
		time.Second,
		func(_ context.Context, spec CommandSpec) Sample {
			if call >= len(wantArgs) {
				t.Fatalf("unexpected command %d: %#v", call+1, spec)
			}
			if spec.Binary != "workbook" || spec.Directory != repository || spec.Timeout != time.Second ||
				!reflect.DeepEqual(spec.Args, wantArgs[call]) {
				t.Fatalf("command %d = %#v, want args %#v in %q with 1s timeout", call+1, spec, wantArgs[call], repository)
			}
			sample := samples[call]
			call++
			return sample
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if call != 4 {
		t.Fatalf("measurement calls = %d, want 4", call)
	}
	if got, want := len(results), 3; got != want {
		t.Fatalf("projection scenarios = %d, want %d", got, want)
	}
	wantSamples := []Sample{samples[0], samples[1], samples[3]}
	for index := range results {
		if !reflect.DeepEqual(results[index].Samples, []Sample{wantSamples[index]}) {
			t.Errorf("%s samples = %#v, want retained sample %#v", results[index].Name, results[index].Samples, wantSamples[index])
		}
	}
}

func TestMeasureProjectionScenariosRejectSetupMutationFailure(t *testing.T) {
	call := 0
	_, err := measureProjectionScenarios(
		context.Background(),
		"workbook",
		t.TempDir(),
		"WB-task",
		time.Second,
		func(_ context.Context, _ CommandSpec) Sample {
			call++
			if call == 3 {
				return Sample{ExitCode: 2, Error: "setup update failed"}
			}
			return Sample{ExitCode: 0}
		},
	)
	if err == nil || !strings.Contains(err.Error(), "prepare projection-refresh-one-changed") {
		t.Fatalf("setup mutation error = %v, want harness failure", err)
	}
}

func TestMeasureRepositoryRunsUnchangedSyncOnlyAfterInitialCompletes(t *testing.T) {
	t.Run("initial timeout", func(t *testing.T) {
		calls := 0
		repository := t.TempDir()
		results, err := measureLocalBareSync(
			context.Background(),
			"workbook",
			repository,
			time.Second,
			func(_ context.Context, spec CommandSpec) Sample {
				calls++
				assertSyncCommandSpec(t, spec, repository)
				return Sample{ExitCode: -1, TimedOut: true, Error: "timed out"}
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("sync calls = %d, want 1 after initial timeout", calls)
		}
		got := make([]string, len(results))
		for index := range results {
			got[index] = results[index].Name
		}
		want := []string{"sync-initial-local-bare", "sync-unchanged-local-bare"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sync scenarios = %#v, want complete scenario set %#v", got, want)
		}
		if !results[0].Samples[0].TimedOut || results[0].Summary.TimedOut != 1 {
			t.Fatalf("initial timeout result = %#v, want retained timeout", results[0])
		}
		unavailable := results[1].Samples[0]
		if unavailable.ExitCode != -1 || unavailable.TimedOut ||
			unavailable.Error != "not measured: initial sync timed out before remote completion" {
			t.Fatalf("unchanged sync result = %#v, want explicit unavailability", unavailable)
		}
	})

	t.Run("initial product failure", func(t *testing.T) {
		calls := 0
		repository := t.TempDir()
		results, err := measureLocalBareSync(
			context.Background(),
			"workbook",
			repository,
			time.Second,
			func(_ context.Context, spec CommandSpec) Sample {
				calls++
				assertSyncCommandSpec(t, spec, repository)
				return Sample{ExitCode: 2, Error: "remote rejected update"}
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("sync calls = %d, want 1 after initial product failure", calls)
		}
		failed := results[0].Samples[0]
		if failed.ExitCode != 2 || failed.TimedOut || failed.Error != "remote rejected update" {
			t.Fatalf("initial product failure = %#v, want retained nonzero sample", failed)
		}
		unavailable := results[1].Samples[0]
		if unavailable.ExitCode != -1 || unavailable.TimedOut ||
			unavailable.Error != "not measured: initial sync failed before remote completion" {
			t.Fatalf("unchanged sync result = %#v, want explicit unavailability", unavailable)
		}
	})

	t.Run("initial completion", func(t *testing.T) {
		calls := 0
		repository := t.TempDir()
		results, err := measureLocalBareSync(
			context.Background(),
			"workbook",
			repository,
			time.Second,
			func(_ context.Context, spec CommandSpec) Sample {
				calls++
				assertSyncCommandSpec(t, spec, repository)
				return Sample{ExitCode: 0}
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if calls != 2 {
			t.Fatalf("sync calls = %d, want initial and unchanged", calls)
		}
		got := []string{results[0].Name, results[1].Name}
		want := []string{"sync-initial-local-bare", "sync-unchanged-local-bare"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sync scenarios = %#v, want %#v", got, want)
		}
	})
}

func TestMeasureRepositoryParsesObjectCountsAndConvertsKiBToBytes(t *testing.T) {
	before := []byte("count: 7\nsize: 3\nin-pack: 2\nsize-pack: 1\n")
	after := []byte("count: 0\nsize: 0\nin-pack: 11\nsize-pack: 5\n")

	got, err := repositoryMetricsFromCounts(time.Millisecond, 2*time.Millisecond, before, after)
	if err != nil {
		t.Fatal(err)
	}
	want := RepositoryMetrics{
		LooseRefEnumerationMilliseconds:  1,
		PackedRefEnumerationMilliseconds: 2,
		LooseObjects:                     7,
		LooseObjectBytes:                 3 * 1024,
		PackedObjects:                    11,
		PackBytes:                        5 * 1024,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repository metrics = %#v, want %#v", got, want)
	}
}

func TestRunRepositoryGitBoundsCommandAndDescendants(t *testing.T) {
	binaryDirectory := t.TempDir()
	gitPath := filepath.Join(binaryDirectory, "git")
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\n/bin/sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binaryDirectory)

	startedAt := time.Now()
	_, _, err := runRepositoryGit(context.Background(), 20*time.Millisecond, "", "--version")
	elapsed := time.Since(startedAt)
	if err == nil || !strings.Contains(err.Error(), "timed out after 20ms") {
		t.Fatalf("runRepositoryGit error = %v, want bounded timeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("runRepositoryGit returned after %s, want bounded descendant termination", elapsed)
	}
}

type recordedStatusRequest struct {
	taskID string
	status string
}

func emptyTraceFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendTraceStarts(path string, count int) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	for range count {
		if _, err := fmt.Fprintln(file, `{"event":"start","argv":["git","status"]}`); err != nil {
			return err
		}
	}
	return nil
}

func readRecordedStatusRequest(request *http.Request) (recordedStatusRequest, error) {
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		return recordedStatusRequest{}, err
	}
	const prefix = "/api/tasks/"
	const suffix = "/status"
	taskID := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, prefix), suffix)
	if taskID == request.URL.Path || taskID == "" {
		return recordedStatusRequest{}, fmt.Errorf("invalid status path %q", request.URL.Path)
	}
	return recordedStatusRequest{taskID: taskID, status: body.Status}, nil
}

func writeRecordedStatusResponse(writer http.ResponseWriter, request recordedStatusRequest) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"format":  "workbook.task-mutation",
		"version": 1,
		"task": map[string]string{
			"id":     request.taskID,
			"status": request.status,
		},
	})
}

type recordingWarmScenarioServer struct {
	t           *testing.T
	role        string
	sample      string
	ambiguous   bool
	events      *[]string
	prepareErr  error
	measureErr  error
	closeErr    error
	closedCount *int
}

func (server *recordingWarmScenarioServer) prepareProjection(_ context.Context, activeTasks int, _ time.Duration) error {
	server.t.Helper()
	if server.role == "api-update" && activeTasks != 10 && server.prepareErr == nil {
		server.t.Fatalf("prepared active tasks = %d, want 10", activeTasks)
	}
	if server.events != nil {
		*server.events = append(*server.events, "prepare "+server.role)
	}
	return server.prepareErr
}

func (server *recordingWarmScenarioServer) measureStatus(
	_ context.Context,
	taskID string,
	status string,
	_ time.Duration,
) (Sample, error) {
	server.t.Helper()
	if server.role != "api-update" || taskID != "WB-00" || status != "ready" {
		server.t.Fatalf("update role = %q task = %q status = %q, want isolated api-update on WB-00 to ready", server.role, taskID, status)
	}
	if server.events != nil {
		*server.events = append(*server.events, "measure "+server.role)
	}
	if server.measureErr != nil {
		return Sample{}, server.measureErr
	}
	return Sample{ExitCode: 0, GitProcesses: 1}, nil
}

func (server *recordingWarmScenarioServer) measureIndependentBurst(
	_ context.Context,
	taskIDs []string,
	status string,
	_ time.Duration,
) (Sample, error) {
	server.t.Helper()
	if server.role != "api-burst-independent-10" || len(taskIDs) != 10 || status != "ready" {
		server.t.Fatalf("independent role = %q tasks = %d status = %q, want ten ready requests", server.role, len(taskIDs), status)
	}
	targets := make(map[string]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		targets[taskID] = struct{}{}
	}
	if len(targets) != 10 {
		server.t.Fatalf("independent distinct task IDs = %d, want 10", len(targets))
	}
	server.ambiguous = true
	if server.sample == "sample-001" {
		return Sample{ExitCode: -1, TimedOut: true, Error: "request timed out"}, nil
	}
	return Sample{ExitCode: http.StatusConflict, Error: "HTTP 409 Conflict: task head changed"}, nil
}

func (server *recordingWarmScenarioServer) measureSameTaskBurst(
	_ context.Context,
	taskID string,
	statusOffset int,
	_ time.Duration,
) (Sample, error) {
	server.t.Helper()
	if server.role != "api-burst-same-task-10" || taskID != "WB-01" || statusOffset != 0 {
		server.t.Fatalf("same-task role = %q task = %q offset = %d, want isolated WB-01 starting at ready", server.role, taskID, statusOffset)
	}
	if server.ambiguous {
		server.t.Fatal("same-task burst reused a server with ambiguous independent state")
	}
	return Sample{ExitCode: 0, GitProcesses: 10}, nil
}

func (server *recordingWarmScenarioServer) close(time.Duration) error {
	if server.events != nil {
		*server.events = append(*server.events, "close "+server.role)
	}
	(*server.closedCount)++
	return server.closeErr
}

func assertSyncCommandSpec(t *testing.T, got CommandSpec, repository string) {
	t.Helper()
	if got.Binary != "workbook" || got.Directory != repository || got.Timeout != time.Second ||
		!reflect.DeepEqual(got.Args, []string{"sync", "--json"}) {
		t.Fatalf("sync command = %#v, want workbook sync --json in %q with 1s timeout", got, repository)
	}
}

func buildWorkbookBinary(t *testing.T) string {
	t.Helper()
	root := filepath.Clean(filepath.Join("..", ".."))
	binary := filepath.Join(t.TempDir(), "workbook")
	command := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "./cmd/workbook")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build workbook: %v\n%s", err, output)
	}
	return binary
}
