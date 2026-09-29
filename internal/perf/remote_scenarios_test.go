package perf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

func TestDecodeRemoteScenarioResult(t *testing.T) {
	t.Parallel()
	fetch := `{"format":"workbook.result","version":1,"command":"fetch","data":{"remote":"origin","status":"completed","tasks":[{"taskId":"task-a","status":"created"},{"taskId":"task-b","status":"created"}]}}`
	push := `{"format":"workbook.result","version":1,"command":"push","data":{"remote":"origin","status":"completed","tasks":[{"taskId":"task-a","status":"published"},{"taskId":"task-b","status":"up-to-date"}]}}`
	sync := `{"format":"workbook.result","version":1,"command":"sync","data":{"remote":"origin","fetch":{"remote":"origin","status":"completed","tasks":[{"taskId":"task-a","status":"unchanged"}]},"push":{"remote":"origin","status":"completed","tasks":[{"taskId":"task-a","status":"up-to-date"}]}}}`
	nonzero := `{"format":"workbook.error","version":1,"error":{"category":"stale-write","message":"diverged"}}`

	tests := []struct {
		name          string
		stdout        string
		stderr        string
		command       string
		expectFailure bool
		wantFetch     []gitstore.SyncTaskResult
		wantPush      []gitstore.SyncTaskResult
	}{
		{
			name:      "fetch",
			stdout:    fetch,
			command:   "fetch",
			wantFetch: []gitstore.SyncTaskResult{{TaskID: "task-a", Status: gitstore.SyncCreated}, {TaskID: "task-b", Status: gitstore.SyncCreated}},
		},
		{
			name:     "push",
			stdout:   push,
			command:  "push",
			wantPush: []gitstore.SyncTaskResult{{TaskID: "task-a", Status: gitstore.SyncPublished}, {TaskID: "task-b", Status: gitstore.SyncUpToDate}},
		},
		{
			name:      "sync",
			stdout:    sync,
			command:   "sync",
			wantFetch: []gitstore.SyncTaskResult{{TaskID: "task-a", Status: gitstore.SyncUnchanged}},
			wantPush:  []gitstore.SyncTaskResult{{TaskID: "task-a", Status: gitstore.SyncUpToDate}},
		},
		{
			name:          "nonzero sync",
			stdout:        sync,
			stderr:        nonzero,
			command:       "sync",
			expectFailure: true,
			wantFetch:     []gitstore.SyncTaskResult{{TaskID: "task-a", Status: gitstore.SyncUnchanged}},
			wantPush:      []gitstore.SyncTaskResult{{TaskID: "task-a", Status: gitstore.SyncUpToDate}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeRemoteScenarioResult([]byte(test.stdout), []byte(test.stderr), test.command, test.expectFailure)
			if err != nil {
				t.Fatal(err)
			}
			if err := requireRemoteTaskResults("fetch", got.Fetch.Tasks, test.wantFetch); err != nil {
				t.Fatal(err)
			}
			if err := requireRemoteTaskResults("push", got.Push.Tasks, test.wantPush); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunRemoteScenariosUsesTopologyCommandsAndVerifiesResults(t *testing.T) {
	t.Parallel()
	workbook := buildRemoteScenarioWorkbook(t)
	tests := []struct {
		name    string
		command string
		// expectFailure marks a scenario whose measured command fails by
		// design, so its samples record the nonzero exit as evidence.
		expectFailure bool
	}{
		{name: "sync-fresh-checkout", command: "fetch"},
		{name: "sync-initial-publication", command: "push"},
		{name: "sync-already-synchronized", command: "sync"},
		{name: "sync-small-changed-ref-set", command: "sync"},
		{name: "sync-divergent-tips", command: "sync"},
		{name: "sync-malformed-local-tip", command: "push", expectFailure: true},
		{name: "sync-malformed-remote-tip", command: "fetch", expectFailure: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var commands []CommandSpec
			results, err := runRemoteScenarios(context.Background(), RunSpec{
				WorkbookBinary: workbook,
				Fixture:        FixtureSpec{TotalTasks: 10, ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
				// A real `workbook` runs under this bound and a timed-out sample
				// would report the outcome "timeout" instead of the contract
				// below, so the bound is a hang guard: nothing here asserts how
				// long anything took.
				Samples: 1, CommandTimeout: 120 * time.Second,
			}, filepath.Join(t.TempDir(), "scenarios"), []string{test.name}, remoteScenarioDependencies{
				buildFixture: buildRemoteFixtureWithinTimeout,
				measureCommand: func(ctx context.Context, spec CommandSpec) CommandMeasurement {
					commands = append(commands, spec)
					measurement := MeasureCommandOutput(ctx, spec)
					measurement.Sample.GitProcesses = 7
					return measurement
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || len(commands) != 1 {
				t.Fatalf("results/commands = %d/%d, want 1/1", len(results), len(commands))
			}
			result := results[0]
			wantOutcome := "completed"
			if test.expectFailure {
				wantOutcome = "failed"
			}
			if got := scenarioOutcome(result); got != wantOutcome {
				t.Fatalf("%s outcome = %q, want %s", result.Name, got, wantOutcome)
			}
			if len(result.Samples) != 1 || result.Samples[0].GitProcesses != 7 {
				t.Fatalf("%s samples = %#v, want one fixed-count sample", result.Name, result.Samples)
			}
			if got := commands[0].Args; !reflect.DeepEqual(got, []string{test.command, "--json"}) {
				t.Fatalf("%s command args = %v, want %s --json", result.Name, got, test.command)
			}
		})
	}
}

func TestRunRemoteScenariosBuildsOnlySelectedTopology(t *testing.T) {
	t.Parallel()
	workbook := buildRemoteScenarioWorkbook(t)
	var built []RemoteTopology
	_, err := runRemoteScenarios(context.Background(), RunSpec{
		WorkbookBinary: workbook,
		Fixture:        FixtureSpec{TotalTasks: 10, ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
		// MeasureCommandOutput below runs the real `workbook`, so this is the same
		// hang guard the topology and verification tests carry: a timed-out sample
		// fails the run for a reason that has nothing to do with which topology was
		// built, and nothing here asserts how long anything took.
		Samples: 1, CommandTimeout: 120 * time.Second,
	}, filepath.Join(t.TempDir(), "scenarios"), []string{"sync-fresh-checkout"}, remoteScenarioDependencies{
		buildFixture: func(ctx context.Context, root string, spec FixtureSpec, topology RemoteTopology) (RemoteFixture, error) {
			built = append(built, topology)
			return buildRemoteFixtureWithinTimeout(ctx, root, spec, topology)
		},
		measureCommand: MeasureCommandOutput,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(built, []RemoteTopology{RemoteFreshCheckout}) {
		t.Fatalf("built topologies = %v, want fresh checkout only", built)
	}
}

func TestRemoteScenarioProcessCountDoesNotScaleWithFixtureSize(t *testing.T) {
	t.Parallel()
	workbook := buildRemoteScenarioWorkbook(t)
	counts := make([]int, 0, 2)
	for _, fixture := range []FixtureSpec{
		{TotalTasks: 10, ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
		{TotalTasks: 25, ActiveTasks: 25, OperationsPerTask: 7, ObjectFormat: "sha1"},
	} {
		results, err := runRemoteScenarios(context.Background(), RunSpec{
			// A real `workbook sync` runs under this bound, on the larger of the
			// two fixtures as well: a hang guard, and the injected process count
			// below is what the test asserts rather than any elapsed time.
			WorkbookBinary: workbook, Fixture: fixture, Samples: 1, CommandTimeout: 120 * time.Second,
		}, filepath.Join(t.TempDir(), "scenarios"), []string{"sync-fresh-checkout"}, remoteScenarioDependencies{
			buildFixture: buildRemoteFixtureWithinTimeout,
			measureCommand: func(ctx context.Context, spec CommandSpec) CommandMeasurement {
				measurement := MeasureCommandOutput(ctx, spec)
				measurement.Sample.GitProcesses = 9
				return measurement
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		counts = append(counts, results[0].Samples[0].GitProcesses)
	}
	if !reflect.DeepEqual(counts, []int{9, 9}) {
		t.Fatalf("Git process counts = %v, want stable count", counts)
	}
}

func buildRemoteScenarioWorkbook(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "workbook")
	command := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "./cmd/workbook")
	command.Dir = filepath.Clean(filepath.Join("..", ".."))
	command.Env = goToolchainEnvironment(t)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build workbook: %v\n%s", err, output)
	}
	return binary
}

func TestDecodeRemoteScenarioResultRejectsInvalidEnvelopesAndTaskSets(t *testing.T) {
	t.Parallel()
	valid := func(command string, tasks []gitstore.SyncTaskResult) string {
		document := struct {
			Format  string              `json:"format"`
			Version int                 `json:"version"`
			Command string              `json:"command"`
			Data    gitstore.SyncResult `json:"data"`
		}{"workbook.result", 1, command, gitstore.SyncResult{Remote: "origin", Status: gitstore.SyncPhaseCompleted, Tasks: tasks}}
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	goodTasks := []gitstore.SyncTaskResult{{TaskID: "task-a", Status: gitstore.SyncCreated}, {TaskID: "task-b", Status: gitstore.SyncCreated}}
	tests := []struct {
		name    string
		stdout  string
		stderr  string
		command string
		want    string
	}{
		{name: "wrong format", stdout: strings.Replace(valid("fetch", goodTasks), "workbook.result", "wrong", 1), command: "fetch", want: "format"},
		{name: "wrong version", stdout: strings.Replace(valid("fetch", goodTasks), `"version":1`, `"version":2`, 1), command: "fetch", want: "version"},
		{name: "wrong command", stdout: valid("push", goodTasks), command: "fetch", want: "command"},
		{name: "duplicate", stdout: valid("fetch", []gitstore.SyncTaskResult{{TaskID: "task-a", Status: gitstore.SyncCreated}, {TaskID: "task-a", Status: gitstore.SyncCreated}}), command: "fetch", want: "duplicate"},
		{name: "missing", stdout: valid("fetch", []gitstore.SyncTaskResult{{TaskID: "task-a", Status: gitstore.SyncCreated}}), command: "fetch", want: "missing"},
		{name: "unexpected", stdout: valid("fetch", append(goodTasks, gitstore.SyncTaskResult{TaskID: "task-c", Status: gitstore.SyncCreated})), command: "fetch", want: "unexpected"},
		{name: "wrong error format", stdout: valid("fetch", goodTasks), stderr: `{"format":"wrong","version":1,"error":{"category":"stale-write","message":"diverged"}}`, command: "fetch", want: "error format"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeRemoteScenarioResult([]byte(test.stdout), []byte(test.stderr), test.command, test.stderr != "")
			if err == nil {
				if test.name == "missing" || test.name == "unexpected" {
					err = requireRemoteTaskResults("fetch", got.Fetch.Tasks, goodTasks)
				}
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decode error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodeRemoteScenarioResultEnforcesRemotePhasesErrorCategoryAndWarnings(t *testing.T) {
	t.Parallel()
	contract := remoteScenarioContract{
		command:       "sync",
		fetchStatus:   gitstore.SyncPhaseCompleted,
		pushStatus:    gitstore.SyncPhaseSkipped,
		expectFailure: true,
		errorCategory: core.CategoryStaleWrite,
	}
	valid := `{"format":"workbook.result","version":1,"command":"sync","warnings":[],"data":{"remote":"origin","fetch":{"remote":"origin","status":"completed","tasks":[]},"push":{"remote":"origin","status":"skipped","tasks":[]}}}`
	validError := `{"format":"workbook.error","version":1,"error":{"category":"stale-write","message":"diverged"}}`
	tests := []struct {
		name   string
		stdout string
		stderr string
		want   string
	}{
		{name: "wrong run remote", stdout: strings.Replace(valid, `"remote":"origin"`, `"remote":"other"`, 1), stderr: validError, want: "sync remote"},
		{name: "missing phase remote", stdout: strings.Replace(valid, `"fetch":{"remote":"origin"`, `"fetch":{"remote":""`, 1), stderr: validError, want: "fetch remote"},
		{name: "wrong phase", stdout: strings.Replace(valid, `"status":"skipped"`, `"status":"completed"`, 1), stderr: validError, want: "push status"},
		{name: "wrong error category", stdout: valid, stderr: strings.Replace(validError, "stale-write", "corrupt-data", 1), want: "error category"},
		{name: "malformed warnings", stdout: strings.Replace(valid, `"warnings":[]`, `"warnings":[{"code":""}]`, 1), stderr: validError, want: "warning"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeRemoteScenarioResultWithContract([]byte(test.stdout), []byte(test.stderr), contract)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decode error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunRemoteScenariosUsesIndependentFixturesForEachSample(t *testing.T) {
	t.Parallel()
	var roots []string
	measures := 0
	results, err := runRemoteScenarios(context.Background(), RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        FixtureSpec{TotalTasks: 10, ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
		Samples:        2,
		CommandTimeout: time.Second,
	}, t.TempDir(), []string{"sync-fresh-checkout"}, remoteScenarioDependencies{
		buildFixture: func(_ context.Context, root string, _ FixtureSpec, _ RemoteTopology) (RemoteFixture, error) {
			roots = append(roots, root)
			return RemoteFixture{LocalRoot: root}, nil
		},
		measureCommand: func(context.Context, CommandSpec) CommandMeasurement {
			measures++
			return CommandMeasurement{Sample: Sample{ExitCode: -1, TimedOut: true}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || len(results[0].Samples) != 2 || measures != 2 || len(roots) != 2 {
		t.Fatalf("samples/builds/measures = %#v/%d/%d, want two each", results, len(roots), measures)
	}
	if roots[0] == roots[1] || !strings.Contains(roots[0], "sample-001") || !strings.Contains(roots[1], "sample-002") {
		t.Fatalf("sample fixture roots = %v, want independent indexed roots", roots)
	}
}

func TestRunRemoteScenariosRejectsZeroSamples(t *testing.T) {
	t.Parallel()
	_, err := runRemoteScenarios(context.Background(), RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        FixtureSpec{TotalTasks: 10, ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
		CommandTimeout: time.Second,
	}, t.TempDir(), []string{"sync-fresh-checkout"}, remoteScenarioDependencies{})
	if err == nil || !strings.Contains(err.Error(), "samples must be positive") {
		t.Fatalf("runRemoteScenarios error = %v, want samples validation", err)
	}
}

func TestSmallChangedRefSetKeepsTrackingAtPrePushRemoteTip(t *testing.T) {
	t.Parallel()
	workbook := buildRemoteScenarioWorkbook(t)
	var fixture RemoteFixture
	_, err := runRemoteScenarios(context.Background(), RunSpec{
		WorkbookBinary: workbook,
		Fixture:        FixtureSpec{TotalTasks: 10, ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
		Samples:        1,
		// A half-pushed set of refs would fail the invariant below for a reason
		// that has nothing to do with it, so a real `workbook sync` gets room to
		// finish. Nothing here asserts how long it took.
		CommandTimeout: 120 * time.Second,
	}, t.TempDir(), []string{"sync-small-changed-ref-set"}, remoteScenarioDependencies{
		buildFixture: func(ctx context.Context, root string, spec FixtureSpec, topology RemoteTopology) (RemoteFixture, error) {
			built, err := buildRemoteFixtureWithinTimeout(ctx, root, spec, topology)
			fixture = built
			return built, err
		},
		measureCommand: MeasureCommandOutput,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := fixtureRefMapForRoot(context.Background(), fixture.LocalRoot, "refs/workbook/tasks/")
	if err != nil {
		t.Fatal(err)
	}
	tracking, err := fixtureRefMapForRoot(context.Background(), fixture.LocalRoot, "refs/workbook/remotes/origin/tasks/")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := fixtureRemoteRefMapForRoot(context.Background(), fixture.OriginRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, taskID := range fixture.TaskIDs[:5] {
		before := fixture.Expected[taskID]
		if canonical[taskID] != before.Canonical || remote[taskID] != before.Canonical || tracking[taskID] != before.Tracking {
			t.Fatalf("%s refs after sync = canonical %q tracking %q remote %q; want local tip %q, pre-push tracking %q", taskID, canonical[taskID], tracking[taskID], remote[taskID], before.Canonical, before.Tracking)
		}
	}
}

func TestSmallChangedRefSetExpectedResultsChangeOnlyTenTasks(t *testing.T) {
	t.Parallel()
	taskIDs := make([]string, 12)
	for index := range taskIDs {
		taskIDs[index] = fmt.Sprintf("WB-%02d", index)
	}
	fetch, push := expectedRemoteTaskResults(RemoteSmallChangedRefSet, taskIDs)
	for index := range taskIDs {
		wantFetch := gitstore.SyncUnchanged
		wantPush := gitstore.SyncUpToDate
		switch {
		case index < 5:
			wantFetch = gitstore.SyncLocalAhead
			wantPush = gitstore.SyncPublished
		case index < 10:
			wantFetch = gitstore.SyncFastForwarded
		}
		if fetch[index].Status != wantFetch || push[index].Status != wantPush {
			t.Fatalf(
				"task %d results = fetch %q push %q, want fetch %q push %q",
				index,
				fetch[index].Status,
				push[index].Status,
				wantFetch,
				wantPush,
			)
		}
	}
}

func TestRemoteScenarioVerificationGitCallsDoNotScaleWithFixture(t *testing.T) {
	t.Parallel()
	workbook := buildRemoteScenarioWorkbook(t)
	fixtureSpecs := []FixtureSpec{
		{TotalTasks: 10, ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
		{TotalTasks: 25, ActiveTasks: 25, OperationsPerTask: 7, ObjectFormat: "sha1"},
	}
	type outcome struct {
		count int
		err   error
	}
	outcomes := make(chan outcome, len(fixtureSpecs))
	var waiting sync.WaitGroup
	for _, fixtureSpec := range fixtureSpecs {
		waiting.Add(1)
		go func(fixtureSpec FixtureSpec) {
			defer waiting.Done()
			calls := 0
			results, err := runRemoteScenarios(context.Background(), RunSpec{
				// Same class as the two above: a real `workbook` runs under this
				// bound, and the verification readers this test counts only run
				// for a measurement that did not time out. Nothing here asserts
				// how long anything took.
				WorkbookBinary: workbook, Fixture: fixtureSpec, Samples: 1, CommandTimeout: 120 * time.Second,
			}, t.TempDir(), []string{"sync-fresh-checkout"}, remoteScenarioDependencies{
				buildFixture: buildRemoteFixtureWithinTimeout,
				measureCommand: func(ctx context.Context, spec CommandSpec) CommandMeasurement {
					measurement := MeasureCommandOutput(ctx, spec)
					measurement.Sample.GitProcesses = 9
					return measurement
				},
				readCanonicalRefs: func(ctx context.Context, root string) (map[string]string, error) {
					calls++
					return fixtureRefMapForRoot(ctx, root, "refs/workbook/tasks/")
				},
				readTrackingRefs: func(ctx context.Context, root string) (map[string]string, error) {
					calls++
					return fixtureRefMapForRoot(ctx, root, "refs/workbook/remotes/origin/tasks/")
				},
				readRemoteRefs: func(ctx context.Context, root string) (map[string]string, error) {
					calls++
					return fixtureRemoteRefMapForRoot(ctx, root)
				},
			})
			if err != nil {
				outcomes <- outcome{err: err}
				return
			}
			if got := results[0].Samples[0].GitProcesses; got != 9 {
				outcomes <- outcome{err: fmt.Errorf("product Git processes = %d, want fixed injected count 9", got)}
				return
			}
			outcomes <- outcome{count: calls}
		}(fixtureSpec)
	}
	waiting.Wait()
	close(outcomes)
	counts := make([]int, 0, len(fixtureSpecs))
	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		counts = append(counts, outcome.count)
	}
	sort.Ints(counts)
	if !reflect.DeepEqual(counts, []int{3, 3}) {
		t.Fatalf("verification Git calls = %v, want constant canonical/tracking/remote count", counts)
	}
}

func TestRequireRemoteScenarioRefsPassesCallerCancellationToReader(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reads := 0
	err := requireRemoteScenarioRefs(ctx, RemoteFreshCheckout, RemoteFixture{
		LocalRoot:  "local",
		OriginRoot: "origin",
		TaskIDs:    []string{"task-a"},
		Expected:   map[string]ExpectedRefs{"task-a": {Remote: "remote-tip"}},
	}, remoteScenarioDependencies{
		readCanonicalRefs: func(got context.Context, root string) (map[string]string, error) {
			reads++
			if root != "local" {
				t.Fatalf("reader root = %q, want local", root)
			}
			if !errors.Is(got.Err(), context.Canceled) {
				t.Fatalf("reader context error = %v, want context canceled", got.Err())
			}
			return nil, got.Err()
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("verification error = %v, want context canceled", err)
	}
	if reads != 1 {
		t.Fatalf("ref readers called %d times, want canonical reader only", reads)
	}
}

// The fixture build is bounded by FixtureTimeout, not by the timeout of the
// command being measured. The flake this pins: a remote fixture's init, two
// clones and dozens of plumbing calls all ran inside the measured command's
// budget, and runFixtureGitInRoot's exec.CommandContext turns that deadline
// into a SIGKILL, so a healthy fixture push died of CPU contention.
func TestRunRemoteScenariosBoundsFixtureBuildsWithTheFixtureTimeout(t *testing.T) {
	t.Parallel()
	var measuredTimeouts []time.Duration
	results, err := runRemoteScenarios(context.Background(), RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        FixtureSpec{TotalTasks: 10, ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
		Samples:        1,
		CommandTimeout: 10 * time.Millisecond,
		FixtureTimeout: 30 * time.Second,
	}, t.TempDir(), []string{"sync-fresh-checkout"}, remoteScenarioDependencies{
		buildFixture: sleepingRemoteFixtureBuilder(80 * time.Millisecond),
		measureCommand: func(_ context.Context, command CommandSpec) CommandMeasurement {
			measuredTimeouts = append(measuredTimeouts, command.Timeout)
			return CommandMeasurement{Sample: Sample{ExitCode: -1, TimedOut: true}}
		},
	})
	if err != nil {
		t.Fatalf("runRemoteScenarios with a generous fixture budget: %v", err)
	}
	if len(results) != 1 || len(results[0].Samples) != 1 {
		t.Fatalf("results = %#v, want one scenario with one sample", results)
	}
	if !results[0].Samples[0].TimedOut {
		t.Fatalf("sample = %#v, want the measured command reported as timed out", results[0].Samples[0])
	}
	// The measured command keeps the operator's timeout exactly: the new field
	// must not have widened what the report calls a timeout.
	if !reflect.DeepEqual(measuredTimeouts, []time.Duration{10 * time.Millisecond}) {
		t.Fatalf("measured command timeouts = %v, want the command timeout unchanged", measuredTimeouts)
	}
}

// Mutation witness for the pairing: a FixtureTimeout too small for the build
// still kills it, so the test above proves the generous value was used rather
// than that the deadline stopped mattering.
func TestRunRemoteScenariosFailsWhenFixtureTimeoutCannotCoverTheBuild(t *testing.T) {
	t.Parallel()
	measures := 0
	_, err := runRemoteScenarios(context.Background(), RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        FixtureSpec{TotalTasks: 10, ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
		Samples:        1,
		CommandTimeout: 10 * time.Millisecond,
		FixtureTimeout: 10 * time.Millisecond,
	}, t.TempDir(), []string{"sync-fresh-checkout"}, remoteScenarioDependencies{
		// Far longer than the deadline, so which of the two the builder
		// notices first is never a scheduling accident.
		buildFixture: sleepingRemoteFixtureBuilder(10 * time.Second),
		measureCommand: func(context.Context, CommandSpec) CommandMeasurement {
			measures++
			return CommandMeasurement{}
		},
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runRemoteScenarios error = %v, want the fixture build to exceed its deadline", err)
	}
	if !strings.Contains(err.Error(), "build sync-fresh-checkout sample 1 fixture") {
		t.Fatalf("error %q does not name the fixture build that failed", err)
	}
	if measures != 0 {
		t.Fatalf("measured commands = %d, want none after a failed fixture build", measures)
	}
}

func TestRunRemoteScenariosRejectsNegativeFixtureTimeout(t *testing.T) {
	t.Parallel()
	_, err := runRemoteScenarios(context.Background(), RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        FixtureSpec{TotalTasks: 10, ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
		Samples:        1,
		CommandTimeout: time.Second,
		FixtureTimeout: -time.Second,
	}, t.TempDir(), []string{"sync-fresh-checkout"}, remoteScenarioDependencies{})
	if err == nil || !strings.Contains(err.Error(), "fixture timeout must be positive") {
		t.Fatalf("runRemoteScenarios error = %v, want fixture timeout validation", err)
	}
}

// A spec that leaves FixtureTimeout zero — every spec this package's own tests
// build directly — gets the same bound a production caller sets by hand, because a
// fixture build is not the thing being measured either way.
//
// The value matters and a behavioral check alone does not prove it: a default of
// one nanosecond, or of the command timeout, would also kill a build that outran
// the command timeout, and the command timeout is precisely the default that
// SIGKILLed five healthy fixture builds under whole-tree load. So the resolver is
// checked by value first, and the run below is what shows the resolved value is the
// one the runner actually uses.
func TestRunRemoteScenariosDefaultsFixtureTimeoutToTheFixtureBound(t *testing.T) {
	t.Parallel()
	unset := RunSpec{CommandTimeout: 37 * time.Second}
	if want, got := FixtureTimeoutFactor*unset.CommandTimeout, unset.fixtureBuildTimeout(); got != want {
		t.Fatalf("resolved fixture timeout = %s, want %s exactly", got, want)
	}
	if unset.fixtureBuildTimeout() <= unset.CommandTimeout {
		t.Fatalf("resolved fixture timeout = %s, want more than the command timeout %s",
			unset.fixtureBuildTimeout(), unset.CommandTimeout)
	}
	set := unset
	set.FixtureTimeout = 11 * time.Second
	if got := set.fixtureBuildTimeout(); got != set.FixtureTimeout {
		t.Fatalf("resolved fixture timeout = %s, want the field's own %s", got, set.FixtureTimeout)
	}

	// The resolved default still bounds the build: a builder that cannot finish
	// inside FixtureTimeoutFactor times a 10 ms command timeout still dies of it.
	_, err := runRemoteScenarios(context.Background(), RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        FixtureSpec{TotalTasks: 10, ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
		Samples:        1,
		CommandTimeout: 10 * time.Millisecond,
	}, t.TempDir(), []string{"sync-fresh-checkout"}, remoteScenarioDependencies{
		buildFixture: sleepingRemoteFixtureBuilder(10 * time.Second),
		measureCommand: func(context.Context, CommandSpec) CommandMeasurement {
			t.Error("measurement must not run after a fixture build that exceeded its deadline")
			return CommandMeasurement{}
		},
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runRemoteScenarios error = %v, want the resolved default to bound the build", err)
	}
}

// sleepingRemoteFixtureBuilder stands in for a fixture build that takes longer
// than one measured command without doing a real build's work. It honors the
// context the runner hands it, which is what a real build does through
// exec.CommandContext.
func sleepingRemoteFixtureBuilder(work time.Duration) func(context.Context, string, FixtureSpec, RemoteTopology) (RemoteFixture, error) {
	return func(ctx context.Context, root string, _ FixtureSpec, _ RemoteTopology) (RemoteFixture, error) {
		timer := time.NewTimer(work)
		defer timer.Stop()
		select {
		case <-timer.C:
			return RemoteFixture{LocalRoot: root}, nil
		case <-ctx.Done():
			return RemoteFixture{}, ctx.Err()
		}
	}
}
