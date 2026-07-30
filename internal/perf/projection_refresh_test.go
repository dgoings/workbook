package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

func TestProjectionRefreshScenarioNamesCoverEveryRequestedChangeCount(t *testing.T) {
	want := map[string]int{
		"projection-refresh-unchanged":            0,
		"projection-refresh-one-changed":          1,
		"projection-refresh-five-changed":         5,
		"projection-refresh-fifty-changed":        50,
		"projection-refresh-five-hundred-changed": 500,
	}
	got := make(map[string]int, len(projectionRefreshDefinitions))
	for _, definition := range projectionRefreshDefinitions {
		got[definition.name] = definition.changedHeads
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projection refresh change counts = %#v, want %#v", got, want)
	}

	names := ProjectionRefreshScenarioNames()
	wantNames := []string{
		"projection-refresh-unchanged",
		"projection-refresh-one-changed",
		"projection-refresh-five-changed",
		"projection-refresh-fifty-changed",
		"projection-refresh-five-hundred-changed",
	}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("projection refresh scenario names = %#v, want %#v", names, wantNames)
	}

	registry := ScenarioNames()
	registryOrder := make([]string, 0, len(wantNames))
	for _, name := range registry {
		if _, family := want[name]; family {
			registryOrder = append(registryOrder, name)
		}
	}
	if !reflect.DeepEqual(registryOrder, wantNames) {
		t.Fatalf("registry family order = %#v, want %#v", registryOrder, wantNames)
	}
}

func TestRunProjectionRefreshScenariosRejectsFixtureWithTooFewMutableHeads(t *testing.T) {
	spec := RunSpec{
		WorkbookBinary: "workbook",
		Fixture: FixtureSpec{
			TotalTasks: 12, ActiveTasks: 10, TombstonedTasks: 2,
			OperationsPerTask: 3, ObjectFormat: "sha1",
		},
		Samples:        1,
		CommandTimeout: time.Minute,
	}
	_, _, err := runProjectionRefreshScenarios(
		context.Background(),
		spec,
		t.TempDir(),
		[]string{"projection-refresh-fifty-changed"},
		projectionRefreshDependencies{
			buildFixture: func(context.Context, string, FixtureSpec) (Fixture, error) {
				t.Fatal("fixture must not be built before the mutable-head requirement is checked")
				return Fixture{}, nil
			},
			runSetup:       func(context.Context, CommandSpec) CommandMeasurement { return CommandMeasurement{} },
			mutateHeads:    func(context.Context, string, core.ProjectConfig, []string, int) error { return nil },
			measureCommand: func(context.Context, CommandSpec) CommandMeasurement { return CommandMeasurement{} },
		},
	)
	if err == nil {
		t.Fatal("expected an actionable mutable-head requirement error")
	}
	for _, fragment := range []string{
		"projection-refresh-fifty-changed",
		"50 mutable active task heads",
		"10 active tasks",
		"--tasks",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error %q does not contain %q", err.Error(), fragment)
		}
	}
}

func TestRunProjectionRefreshScenariosRejectsInexactChangedHeadCardinality(t *testing.T) {
	fixture, spec := newProjectionRefreshTestFixture(t, "sha1")
	calls := 0
	_, _, err := runProjectionRefreshScenarios(
		context.Background(),
		spec,
		t.TempDir(),
		[]string{"projection-refresh-five-changed"},
		projectionRefreshDependencies{
			buildFixture: func(context.Context, string, FixtureSpec) (Fixture, error) {
				return fixture, nil
			},
			runSetup: func(context.Context, CommandSpec) CommandMeasurement {
				return CommandMeasurement{Sample: Sample{ExitCode: 0}}
			},
			mutateHeads: func(ctx context.Context, root string, config core.ProjectConfig, taskIDs []string, round int) error {
				calls++
				return mutateProjectionRefreshHeads(ctx, root, config, taskIDs[:len(taskIDs)-1], round)
			},
			measureCommand: func(context.Context, CommandSpec) CommandMeasurement {
				t.Fatal("measurement must not run after an inexact mutation")
				return CommandMeasurement{}
			},
		},
	)
	if calls != 1 {
		t.Fatalf("mutation calls = %d, want 1", calls)
	}
	if err == nil || !strings.Contains(err.Error(), "changed 4 task heads, want exactly 5") {
		t.Fatalf("cardinality error = %v, want exact changed-head rejection", err)
	}
}

func TestRunProjectionRefreshScenariosMutatesHeadsBeforeEveryTimedRefresh(t *testing.T) {
	fixture, spec := newProjectionRefreshTestFixture(t, "sha1")
	spec.Samples = 2
	// The stubbed product never runs, so stand in for the disposable cache the
	// measured refresh would have written.
	writeProjectionRefreshTestCache(t, fixture.Root)
	var order []string
	beforeMeasure := make([]map[string]string, 0, 2)
	var mutated map[string]string
	_, report, err := runProjectionRefreshScenarios(
		context.Background(),
		spec,
		t.TempDir(),
		[]string{"projection-refresh-five-changed"},
		projectionRefreshDependencies{
			buildFixture: func(context.Context, string, FixtureSpec) (Fixture, error) {
				return fixture, nil
			},
			runSetup: func(_ context.Context, command CommandSpec) CommandMeasurement {
				order = append(order, "setup:"+strings.Join(command.Args, " "))
				return CommandMeasurement{Sample: Sample{ExitCode: 0}}
			},
			mutateHeads: func(ctx context.Context, root string, config core.ProjectConfig, taskIDs []string, round int) error {
				order = append(order, fmt.Sprintf("mutate:%d", len(taskIDs)))
				before := projectionRefreshTestRefs(t, root)
				if err := mutateProjectionRefreshHeads(ctx, root, config, taskIDs, round); err != nil {
					return err
				}
				mutated = projectionRefreshTestRefs(t, root)
				if changed := projectionRefreshTestChanged(before, mutated); changed != len(taskIDs) {
					t.Fatalf("mutation changed %d refs, want %d", changed, len(taskIDs))
				}
				return nil
			},
			measureCommand: func(_ context.Context, command CommandSpec) CommandMeasurement {
				order = append(order, "measure:"+strings.Join(command.Args, " "))
				beforeMeasure = append(beforeMeasure, projectionRefreshTestRefs(t, command.Directory))
				return CommandMeasurement{
					Sample: Sample{ExitCode: 0, Duration: 5 * time.Millisecond, GitProcesses: 3},
					Stdout: projectionRefreshTestListJSON(spec.Fixture.ActiveTasks),
				}
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"setup:list --json",
		"mutate:5",
		"measure:list --json",
		"setup:list --json",
		"mutate:5",
		"measure:list --json",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("call order = %#v, want %#v", order, want)
	}
	for index, refs := range beforeMeasure {
		if !reflect.DeepEqual(refs, mutated) && index == len(beforeMeasure)-1 {
			t.Fatalf("refs at measurement %d = %#v, want the mutated heads", index+1, refs)
		}
	}
	if len(report.Points) != 1 || report.Points[0].ChangedTaskHeads != 5 || report.Points[0].Samples != 2 {
		t.Fatalf("report points = %#v, want one 5-changed point with 2 samples", report.Points)
	}
	if report.Samples != 2 {
		t.Fatalf("report samples = %d, want 2", report.Samples)
	}
}

func TestRunProjectionRefreshScenariosMeasureOnlyTheRefreshCommand(t *testing.T) {
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			if objectFormat == "sha256" && !supportsObjectFormat(t, objectFormat) {
				return
			}
			binary := buildWorkbookBinary(t)
			spec := RunSpec{
				WorkbookBinary: binary,
				Fixture: FixtureSpec{
					TotalTasks: 12, ActiveTasks: 10, TombstonedTasks: 2,
					OperationsPerTask: 3, ObjectFormat: objectFormat,
				},
				Samples:        2,
				CommandTimeout: time.Minute,
			}
			selected := []string{
				"projection-refresh-unchanged",
				"projection-refresh-one-changed",
				"projection-refresh-five-changed",
			}
			results, report, err := RunProjectionRefreshScenarios(
				context.Background(), spec, filepath.Join(t.TempDir(), "refresh"), selected,
			)
			if err != nil {
				t.Fatal(err)
			}

			gotNames := make([]string, len(results))
			for index, result := range results {
				gotNames[index] = result.Name
				if result.Surface != "repository" {
					t.Errorf("%s surface = %q, want repository", result.Name, result.Surface)
				}
				if result.Target != nil {
					t.Errorf("%s target = %#v, want no target", result.Name, result.Target)
				}
				if len(result.Samples) != 2 {
					t.Fatalf("%s samples = %d, want 2", result.Name, len(result.Samples))
				}
				for sampleIndex, sample := range result.Samples {
					if !sampleSucceeded(sample) {
						t.Fatalf("%s sample %d = %#v, want success", result.Name, sampleIndex+1, sample)
					}
					if sample.GitProcesses <= 0 || sample.GitProcesses > 20 {
						t.Errorf(
							"%s sample %d Git processes = %d, want only the measured refresh's processes",
							result.Name, sampleIndex+1, sample.GitProcesses,
						)
					}
				}
			}
			if !reflect.DeepEqual(gotNames, selected) {
				t.Fatalf("scenario names = %#v, want %#v", gotNames, selected)
			}

			if report.Format != ProjectionRefreshFormat || report.Version != ProjectionRefreshVersion {
				t.Fatalf("report envelope = %q v%d", report.Format, report.Version)
			}
			if report.Samples != 2 {
				t.Fatalf("report samples = %d, want 2", report.Samples)
			}
			if !reflect.DeepEqual(report.Fixture, spec.Fixture) {
				t.Fatalf("report fixture = %#v, want %#v", report.Fixture, spec.Fixture)
			}
			wantChanged := []int{0, 1, 5}
			if len(report.Points) != len(wantChanged) {
				t.Fatalf("report points = %#v, want %d points", report.Points, len(wantChanged))
			}
			for index, point := range report.Points {
				if point.Scenario != selected[index] || point.ChangedTaskHeads != wantChanged[index] {
					t.Fatalf("point %d = %#v, want %s with %d changed heads", index+1, point, selected[index], wantChanged[index])
				}
				if point.Samples != 2 {
					t.Errorf("%s samples = %d, want 2", point.Scenario, point.Samples)
				}
				if point.TaskRefs != spec.Fixture.TotalTasks {
					t.Errorf("%s task refs = %d, want %d", point.Scenario, point.TaskRefs, spec.Fixture.TotalTasks)
				}
				if point.ProjectedTaskRows != spec.Fixture.ActiveTasks {
					t.Errorf("%s projected rows = %d, want %d", point.Scenario, point.ProjectedTaskRows, spec.Fixture.ActiveTasks)
				}
				if point.ProjectionCacheBytes <= 0 {
					t.Errorf("%s projection cache bytes = %d, want positive", point.Scenario, point.ProjectionCacheBytes)
				}
				if point.RefEnumerationMedianMilliseconds <= 0 {
					t.Errorf("%s ref enumeration = %f ms, want positive", point.Scenario, point.RefEnumerationMedianMilliseconds)
				}
				if point.RefreshMedianMilliseconds <= 0 {
					t.Errorf("%s refresh median = %f ms, want positive", point.Scenario, point.RefreshMedianMilliseconds)
				}
			}
			if report.Slope.MaxChangedTaskHeads != 5 {
				t.Errorf("slope max changed heads = %d, want 5", report.Slope.MaxChangedTaskHeads)
			}
			if !strings.Contains(report.Slope.Description, "0 changed task heads") ||
				!strings.Contains(report.Slope.Description, "5 changed task heads") {
				t.Errorf("slope description = %q, want both measured endpoints", report.Slope.Description)
			}
		})
	}
}

func TestProjectionRefreshReportSerializesDeterministically(t *testing.T) {
	report := ProjectionRefreshReport{
		Format:  ProjectionRefreshFormat,
		Version: ProjectionRefreshVersion,
		Samples: 3,
		Fixture: FixtureSpec{
			TotalTasks: 525, ActiveTasks: 500, TombstonedTasks: 25,
			OperationsPerTask: 20, ObjectFormat: "sha256",
		},
		Points: []ProjectionRefreshPoint{{
			Scenario:                         "projection-refresh-unchanged",
			ChangedTaskHeads:                 0,
			Samples:                          3,
			TaskRefs:                         525,
			RefEnumerationMedianMilliseconds: 1.5,
			RefreshMedianMilliseconds:        10.25,
			RefreshP95Milliseconds:           11.5,
			RefreshMedianGitProcesses:        4,
			ProjectedTaskRows:                500,
			ProjectionCacheBytes:             1048576,
		}},
		Slope: ProjectionRefreshSlope{
			Description:                "measured description",
			BaselineMilliseconds:       10.25,
			MaxChangedTaskHeads:        0,
			MaxChangedMilliseconds:     10.25,
			MillisecondsPerChangedHead: 0,
		},
	}
	want := `{"format":"workbook.projection-refresh","version":1,"samples":3,` +
		`"fixture":{"totalTasks":525,"activeTasks":500,"tombstonedTasks":25,"operationsPerTask":20,"objectFormat":"sha256"},` +
		`"points":[{"scenario":"projection-refresh-unchanged","changedTaskHeads":0,"samples":3,"taskRefs":525,` +
		`"refEnumerationMedianMilliseconds":1.5,"refreshMedianMilliseconds":10.25,"refreshP95Milliseconds":11.5,` +
		`"refreshMedianGitProcesses":4,"projectedTaskRows":500,"projectionCacheBytes":1048576}],` +
		`"slope":{"description":"measured description","baselineMilliseconds":10.25,"maxChangedTaskHeads":0,` +
		`"maxChangedMilliseconds":10.25,"millisecondsPerChangedHead":0}}`

	for attempt := range 2 {
		encoded, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != want {
			t.Fatalf("attempt %d encoded report =\n%s\nwant\n%s", attempt+1, encoded, want)
		}
	}
}

func TestMeasureRepositoryHonorsRequestedSampleCount(t *testing.T) {
	binary := buildWorkbookBinary(t)
	fixture, err := BuildFixture(context.Background(), filepath.Join(t.TempDir(), "fixture"), FixtureSpec{
		TotalTasks: 10, ActiveTasks: 10,
		OperationsPerTask: 2,
		ObjectFormat:      "sha1",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, results, err := MeasureRepository(context.Background(), binary, fixture.Root, 3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"projection-rebuild", "sync-initial-local-bare", "sync-unchanged-local-bare"}
	got := make([]string, len(results))
	for index, result := range results {
		got[index] = result.Name
		if len(result.Samples) != 3 {
			t.Errorf("%s samples = %d, want 3", result.Name, len(result.Samples))
			continue
		}
		for sampleIndex, sample := range result.Samples {
			if !sampleSucceeded(sample) {
				t.Errorf("%s sample %d = %#v, want success", result.Name, sampleIndex+1, sample)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repository scenarios = %#v, want %#v", got, want)
	}
}

func newProjectionRefreshTestFixture(t *testing.T, objectFormat string) (Fixture, RunSpec) {
	t.Helper()
	spec := RunSpec{
		WorkbookBinary: "workbook",
		Fixture: FixtureSpec{
			TotalTasks: 12, ActiveTasks: 10, TombstonedTasks: 2,
			OperationsPerTask: 3, ObjectFormat: objectFormat,
		},
		Samples:        1,
		CommandTimeout: time.Minute,
	}
	fixture, err := BuildFixture(context.Background(), filepath.Join(t.TempDir(), "fixture"), spec.Fixture)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, spec
}

func writeProjectionRefreshTestCache(t *testing.T, root string) {
	t.Helper()
	directory := filepath.Join(root, ".git", "workbook")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "cache.sqlite"), []byte("projection"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func projectionRefreshTestRefs(t *testing.T, root string) map[string]string {
	t.Helper()
	return fixtureRefMap(t, root, "refs/workbook/tasks/")
}

func projectionRefreshTestChanged(before, after map[string]string) int {
	changed := 0
	for taskID, head := range after {
		if before[taskID] != head {
			changed++
		}
	}
	return changed
}

func projectionRefreshTestListJSON(rows int) []byte {
	tasks := make([]map[string]string, rows)
	for index := range tasks {
		tasks[index] = map[string]string{"id": fmt.Sprintf("WB-%04d", index)}
	}
	document := map[string]any{
		"format":  "workbook.result",
		"version": 1,
		"command": "list",
		"data":    tasks,
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return encoded
}

func TestRunProjectionRefreshScenariosRetainMeasuredProductFailures(t *testing.T) {
	fixture, spec := newProjectionRefreshTestFixture(t, "sha1")
	failed := Sample{Duration: 3 * time.Millisecond, ExitCode: 2, GitProcesses: 2, Error: "list failed"}
	results, report, err := runProjectionRefreshScenarios(
		context.Background(),
		spec,
		t.TempDir(),
		[]string{"projection-refresh-one-changed"},
		projectionRefreshDependencies{
			buildFixture: func(context.Context, string, FixtureSpec) (Fixture, error) {
				return fixture, nil
			},
			runSetup: func(context.Context, CommandSpec) CommandMeasurement {
				return CommandMeasurement{Sample: Sample{ExitCode: 0}}
			},
			mutateHeads: mutateProjectionRefreshHeads,
			measureCommand: func(context.Context, CommandSpec) CommandMeasurement {
				return CommandMeasurement{Sample: failed, Stdout: []byte("not json")}
			},
		},
	)
	if err != nil {
		t.Fatalf("measured product failure must be retained, not fatal: %v", err)
	}
	if len(results) != 1 || !reflect.DeepEqual(results[0].Samples, []Sample{failed}) {
		t.Fatalf("retained samples = %#v, want %#v", results, []Sample{failed})
	}
	if scenarioOutcome(results[0]) != "failed" {
		t.Fatalf("outcome = %q, want failed", scenarioOutcome(results[0]))
	}
	if len(report.Points) != 1 || report.Points[0].ChangedTaskHeads != 1 {
		t.Fatalf("report points = %#v, want the one-changed point", report.Points)
	}
}

func TestRunProjectionRefreshScenariosRejectUntrustworthyProjectionResult(t *testing.T) {
	fixture, spec := newProjectionRefreshTestFixture(t, "sha1")
	writeProjectionRefreshTestCache(t, fixture.Root)
	_, _, err := runProjectionRefreshScenarios(
		context.Background(),
		spec,
		t.TempDir(),
		[]string{"projection-refresh-one-changed"},
		projectionRefreshDependencies{
			buildFixture: func(context.Context, string, FixtureSpec) (Fixture, error) {
				return fixture, nil
			},
			runSetup: func(context.Context, CommandSpec) CommandMeasurement {
				return CommandMeasurement{Sample: Sample{ExitCode: 0}}
			},
			mutateHeads: mutateProjectionRefreshHeads,
			measureCommand: func(context.Context, CommandSpec) CommandMeasurement {
				return CommandMeasurement{
					Sample: Sample{ExitCode: 0, Duration: time.Millisecond},
					Stdout: projectionRefreshTestListJSON(spec.Fixture.ActiveTasks - 1),
				}
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "returned 9 task rows, want 10") {
		t.Fatalf("projection oracle error = %v, want fatal row-count rejection", err)
	}
}
