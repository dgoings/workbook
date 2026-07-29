package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Mutation witness: sharing a fixture or measuring setup commands would let
// cache state from one topology change another topology's result.
func TestValidationScenariosUseIndependentFixturesAndCommands(t *testing.T) {
	var roots []string
	var measured []CommandSpec
	var setup []CommandSpec
	results, err := runValidationScenarios(context.Background(), RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        FixtureSpec{ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
		Samples:        1,
		CommandTimeout: time.Second,
	}, t.TempDir(), validationScenarioNames(), validationScenarioDependencies{
		buildFixture: func(_ context.Context, root string, spec FixtureSpec) (Fixture, error) {
			roots = append(roots, root)
			return Fixture{Root: root, TaskIDs: fixtureTaskIDs(spec.ActiveTasks)}, nil
		},
		runSetup: func(_ context.Context, spec CommandSpec) CommandMeasurement {
			setup = append(setup, spec)
			return CommandMeasurement{Sample: Sample{ExitCode: 0}, Stdout: successfulValidationEnvelope(false, 10, 0, 0, 10)}
		},
		measureCommand: func(_ context.Context, spec CommandSpec) CommandMeasurement {
			measured = append(measured, spec)
			if strings.Contains(spec.Directory, "validate-five-changed") {
				return CommandMeasurement{Sample: Sample{ExitCode: 0, GitProcesses: 7}, Stdout: successfulValidationEnvelope(false, 10, 5, 5, 5)}
			}
			return CommandMeasurement{Sample: Sample{ExitCode: 0, GitProcesses: 7}, Stdout: validationEnvelopeForArgs(spec.Args, 10, 4)}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || len(roots) != 3 || len(measured) != 3 {
		t.Fatalf("results/fixtures/measured = %d/%d/%d, want 3/3/3", len(results), len(roots), len(measured))
	}
	if roots[0] == roots[1] || roots[1] == roots[2] || roots[0] == roots[2] {
		t.Fatalf("fixture roots = %v, want one independent root per scenario", roots)
	}
	wantMeasured := [][]string{{"validate", "--full", "--json"}, {"validate", "--json"}, {"validate", "--json"}}
	for index, want := range wantMeasured {
		if got := measured[index].Args; !reflect.DeepEqual(got, want) {
			t.Fatalf("measured command %d = %v, want %v", index, got, want)
		}
	}
	if len(setup) != 7 { // cached validation plus cached validation and five updates.
		t.Fatalf("setup commands = %d, want 7", len(setup))
	}
	if got := setup[0].Args; !reflect.DeepEqual(got, []string{"validate", "--json"}) {
		t.Fatalf("cached setup = %v, want validate --json", got)
	}
	if got := setup[1].Args; !reflect.DeepEqual(got, []string{"validate", "--json"}) {
		t.Fatalf("five-changed setup = %v, want validate --json", got)
	}
	for index, command := range setup[2:] {
		if len(command.Args) != 5 || command.Args[0] != "update" || command.Args[2] != "--description" || command.Args[4] != "--json" {
			t.Fatalf("change setup %d = %v, want literal product update", index, command.Args)
		}
	}
}

// Mutation witness: sending setup through MeasureCommandOutput would add it to
// the measurement callback and contaminate elapsed time and Trace2 counts.
func TestValidationScenarioSetupIsExcludedFromMeasurement(t *testing.T) {
	setupCalls := 0
	measureCalls := 0
	_, err := runValidationScenarios(context.Background(), RunSpec{
		WorkbookBinary: "workbook", Fixture: FixtureSpec{ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"}, Samples: 1, CommandTimeout: time.Second,
	}, t.TempDir(), []string{"validate-five-changed"}, validationScenarioDependencies{
		buildFixture: func(_ context.Context, root string, spec FixtureSpec) (Fixture, error) {
			return Fixture{Root: root, TaskIDs: fixtureTaskIDs(spec.ActiveTasks)}, nil
		},
		runSetup: func(_ context.Context, _ CommandSpec) CommandMeasurement {
			setupCalls++
			return CommandMeasurement{Sample: Sample{ExitCode: 0}, Stdout: successfulValidationEnvelope(false, 10, 0, 0, 10)}
		},
		measureCommand: func(_ context.Context, spec CommandSpec) CommandMeasurement {
			measureCalls++
			return CommandMeasurement{Sample: Sample{ExitCode: 0}, Stdout: successfulValidationEnvelope(false, 10, 5, 5, 5)}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if setupCalls != 6 || measureCalls != 1 {
		t.Fatalf("setup/measurement calls = %d/%d, want 6/1", setupCalls, measureCalls)
	}
}

// Mutation witness: accepting a mismatched validator count would record an
// untrustworthy product measurement as benchmark evidence.
func TestValidationScenarioOracleRejectsWrongCounts(t *testing.T) {
	measurement := CommandMeasurement{
		Sample: Sample{ExitCode: 0},
		Stdout: successfulValidationEnvelope(false, 10, 1, 4, 10),
	}
	if err := verifyValidationMeasurement("validate-cached-unchanged", measurement, FixtureSpec{ActiveTasks: 10, OperationsPerTask: 4}); err == nil || !strings.Contains(err.Error(), "tasksChecked") {
		t.Fatalf("wrong cached counts error = %v, want tasksChecked oracle rejection", err)
	}
}

// Mutation witness: treating the limit as inclusive would report twelve Git
// process samples as passing despite the approved fewer-than-twelve contract.
func TestValidationScenarioTargetsUseExclusiveProcessLimit(t *testing.T) {
	for _, definition := range validationScenarioDefinitions {
		if definition.target.MaxGitProcesses != 12 {
			t.Fatalf("%s process target = %d, want 12", definition.name, definition.target.MaxGitProcesses)
		}
		result := ScenarioResult{Name: definition.name, Target: &definition.target, Samples: []Sample{{ExitCode: 0, GitProcesses: 12}}}
		if got := scenarioOutcome(result); got != "miss" {
			t.Fatalf("%s outcome at 12 processes = %q, want miss", definition.name, got)
		}
	}
}

// Mutation witness: switching validation to per-history commands would make
// the seven-deep fixture use more processes than its four-deep counterpart.
func TestValidationScenarioProcessCountDoesNotScaleWithHistoryDepth(t *testing.T) {
	workbook := buildValidationScenarioWorkbook(t)
	counts := make(map[string][]int)
	for _, fixture := range []FixtureSpec{
		{ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"},
		{ActiveTasks: 10, OperationsPerTask: 7, ObjectFormat: "sha1"},
	} {
		results, err := RunValidationScenarios(context.Background(), RunSpec{
			WorkbookBinary: workbook, Fixture: fixture, Samples: 1, CommandTimeout: 20 * time.Second,
		}, filepath.Join(t.TempDir(), "validation"), validationScenarioNames())
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range results {
			got := result.Samples[0].GitProcesses
			t.Logf("%s at %d operations Git processes = %d", result.Name, fixture.OperationsPerTask, got)
			if got >= 12 {
				t.Fatalf("%s at %d operations Git processes = %d, want fewer than 12", result.Name, fixture.OperationsPerTask, got)
			}
			counts[result.Name] = append(counts[result.Name], got)
		}
	}
	for _, name := range validationScenarioNames() {
		if got := counts[name]; len(got) != 2 || got[0] != got[1] {
			t.Fatalf("%s process counts = %v, want identical four- and seven-operation counts", name, got)
		}
	}
}

func fixtureTaskIDs(count int) []string {
	ids := make([]string, count)
	for index := range ids {
		ids[index] = fmt.Sprintf("WB-%03d", index+1)
	}
	return ids
}

func successfulValidationEnvelope(full bool, tasks, tasksChecked, commitsChecked, cacheHits int) []byte {
	payload := map[string]any{
		"format": "workbook.result", "version": 1, "command": "validate",
		"data": map[string]any{
			"validatorVersion": 1, "full": full, "taskCount": tasks,
			"tasksChecked": tasksChecked, "commitsChecked": commitsChecked, "cacheHits": cacheHits,
			"valid": tasks, "invalid": 0, "pending": 0, "cachePath": "/tmp/validation.sqlite", "failures": []any{},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return encoded
}

func validationEnvelopeForArgs(args []string, tasks, operations int) []byte {
	if reflect.DeepEqual(args, []string{"validate", "--full", "--json"}) {
		return successfulValidationEnvelope(true, tasks, tasks, tasks*operations, 0)
	}
	return successfulValidationEnvelope(false, tasks, 0, 0, tasks)
}

func buildValidationScenarioWorkbook(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "workbook")
	command := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "./cmd/workbook")
	command.Dir = filepath.Clean(filepath.Join("..", ".."))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build workbook: %v\n%s", err, output)
	}
	return binary
}
