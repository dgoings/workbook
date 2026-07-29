package perf

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/gitstore"
)

func TestDecodeRemoteScenarioResult(t *testing.T) {
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
	workbook := buildRemoteScenarioWorkbook(t)
	tests := []struct {
		name    string
		command string
	}{
		{name: "sync-fresh-checkout", command: "fetch"},
		{name: "sync-initial-publication", command: "push"},
		{name: "sync-already-synchronized", command: "sync"},
		{name: "sync-small-changed-ref-set", command: "sync"},
		{name: "sync-divergent-tips", command: "sync"},
		{name: "sync-malformed-local-tip", command: "push"},
		{name: "sync-malformed-remote-tip", command: "fetch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var commands []CommandSpec
			results, err := runRemoteScenarios(context.Background(), RunSpec{
				WorkbookBinary: workbook,
				Fixture:        FixtureSpec{ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
				Samples:        1, CommandTimeout: 5 * time.Second,
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
			if result.Target == nil || result.Target.MaxGitProcesses <= 7 || result.Target.MaxMilliseconds <= 0 {
				t.Fatalf("%s target = %#v, want approved target above measured count", result.Name, result.Target)
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
	workbook := buildRemoteScenarioWorkbook(t)
	var built []RemoteTopology
	_, err := runRemoteScenarios(context.Background(), RunSpec{
		WorkbookBinary: workbook,
		Fixture:        FixtureSpec{ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
		Samples:        1, CommandTimeout: 5 * time.Second,
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
	workbook := buildRemoteScenarioWorkbook(t)
	counts := make([]int, 0, 2)
	for _, fixture := range []FixtureSpec{
		{ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
		{ActiveTasks: 25, OperationsPerTask: 7, ObjectFormat: "sha1"},
	} {
		results, err := runRemoteScenarios(context.Background(), RunSpec{
			WorkbookBinary: workbook, Fixture: fixture, Samples: 1, CommandTimeout: 5 * time.Second,
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
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build workbook: %v\n%s", err, output)
	}
	return binary
}

func TestDecodeRemoteScenarioResultRejectsInvalidEnvelopesAndTaskSets(t *testing.T) {
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
