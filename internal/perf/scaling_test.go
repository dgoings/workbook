package perf

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Mutation witness: dropping a story point, reordering the matrix, or letting a
// caller mutate the shared slice would change the measured axes.
func TestDefaultScalingPointsEnumerateStoryMatrixInDeterministicOrder(t *testing.T) {
	want := []ScalingPointSpec{
		{ActiveTasks: 100, OperationsPerTask: 20},
		{ActiveTasks: 500, OperationsPerTask: 20},
		{ActiveTasks: 500, OperationsPerTask: 100},
		{ActiveTasks: 1000, OperationsPerTask: 20},
	}
	got := DefaultScalingPoints()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default scaling points = %#v, want %#v", got, want)
	}
	got[0].ActiveTasks = 7
	if next := DefaultScalingPoints(); !reflect.DeepEqual(next, want) {
		t.Fatalf("default scaling points after caller mutation = %#v, want %#v", next, want)
	}
}

func TestScalingPointNamesRecordBothAxes(t *testing.T) {
	tests := []struct {
		point ScalingPointSpec
		want  string
	}{
		{point: ScalingPointSpec{ActiveTasks: 100, OperationsPerTask: 20}, want: "active-100-depth-20"},
		{point: ScalingPointSpec{ActiveTasks: 500, OperationsPerTask: 100}, want: "active-500-depth-100"},
		{point: ScalingPointSpec{ActiveTasks: 1000, OperationsPerTask: 20}, want: "active-1000-depth-20"},
	}
	for _, test := range tests {
		if got := test.point.Name(); got != test.want {
			t.Errorf("%#v name = %q, want %q", test.point, got, test.want)
		}
	}
}

// Mutation witness: holding the tombstone population constant, or dropping it to
// zero, would change the representative fixture shape between matrix points and
// break the documented one-per-twenty ratio.
func TestScalingPointFixtureSpecScalesTombstonesWithActivePopulation(t *testing.T) {
	tests := []struct {
		point        ScalingPointSpec
		objectFormat string
		want         FixtureSpec
	}{
		{
			point:        ScalingPointSpec{ActiveTasks: 100, OperationsPerTask: 20},
			objectFormat: "sha1",
			want:         FixtureSpec{TotalTasks: 105, ActiveTasks: 100, TombstonedTasks: 5, OperationsPerTask: 20, ObjectFormat: "sha1"},
		},
		{
			point:        ScalingPointSpec{ActiveTasks: 500, OperationsPerTask: 20},
			objectFormat: "sha1",
			want:         FixtureSpec{TotalTasks: 525, ActiveTasks: 500, TombstonedTasks: 25, OperationsPerTask: 20, ObjectFormat: "sha1"},
		},
		{
			point:        ScalingPointSpec{ActiveTasks: 500, OperationsPerTask: 100},
			objectFormat: "sha256",
			want:         FixtureSpec{TotalTasks: 525, ActiveTasks: 500, TombstonedTasks: 25, OperationsPerTask: 100, ObjectFormat: "sha256"},
		},
		{
			point:        ScalingPointSpec{ActiveTasks: 1000, OperationsPerTask: 20},
			objectFormat: "sha256",
			want:         FixtureSpec{TotalTasks: 1050, ActiveTasks: 1000, TombstonedTasks: 50, OperationsPerTask: 20, ObjectFormat: "sha256"},
		},
		{
			point:        ScalingPointSpec{ActiveTasks: 10, OperationsPerTask: 4},
			objectFormat: "sha1",
			want:         FixtureSpec{TotalTasks: 11, ActiveTasks: 10, TombstonedTasks: 1, OperationsPerTask: 4, ObjectFormat: "sha1"},
		},
	}
	for _, test := range tests {
		got, err := test.point.FixtureSpec(test.objectFormat)
		if err != nil {
			t.Fatalf("%#v fixture spec: %v", test.point, err)
		}
		if got != test.want {
			t.Errorf("%#v fixture spec = %#v, want %#v", test.point, got, test.want)
		}
		if err := validateFixtureSpec(got); err != nil {
			t.Errorf("%#v produced an invalid fixture spec: %v", test.point, err)
		}
	}
}

func TestScalingPointFixtureSpecRejectsUnmeasurablePoints(t *testing.T) {
	tests := []struct {
		name         string
		point        ScalingPointSpec
		objectFormat string
		want         string
	}{
		{
			name:         "too few active tasks for the small changed ref set",
			point:        ScalingPointSpec{ActiveTasks: 9, OperationsPerTask: 20},
			objectFormat: "sha1",
			want:         "at least 10 active tasks",
		},
		{
			name:         "history too shallow for a tombstone",
			point:        ScalingPointSpec{ActiveTasks: 100, OperationsPerTask: 1},
			objectFormat: "sha1",
			want:         "at least 2 operations per task",
		},
		{
			name:         "unsupported object format",
			point:        ScalingPointSpec{ActiveTasks: 100, OperationsPerTask: 20},
			objectFormat: "sha512",
			want:         "unsupported object format",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.point.FixtureSpec(test.objectFormat); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("fixture spec error = %v, want %q", err, test.want)
			}
		})
	}
}

// Mutation witness: dropping the list read path, either sync topology, or either
// validation path would leave a story-required scenario unmeasured; reordering
// would break the stable registry order the harness documents.
func TestScalingScenarioNamesCoverTheStoryMatrixInRegistryOrder(t *testing.T) {
	want := []string{
		"cli-create",
		"cli-depend",
		"cli-list",
		"cli-move",
		"cli-update",
		"sync-already-synchronized",
		"sync-small-changed-ref-set",
		"validate-full-history",
		"validate-cached-unchanged",
	}
	got := ScalingScenarioNames()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scaling scenario names = %#v, want %#v", got, want)
	}
	got[0] = "changed-by-caller"
	if next := ScalingScenarioNames(); !reflect.DeepEqual(next, want) {
		t.Fatalf("scaling scenario names after caller mutation = %#v, want %#v", next, want)
	}

	registry := ScenarioNames()
	index := make(map[string]int, len(registry))
	for position, name := range registry {
		index[name] = position
	}
	previous := -1
	for _, name := range want {
		position, registered := index[name]
		if !registered {
			t.Fatalf("scaling scenario %q is not in the harness registry", name)
		}
		if position <= previous {
			t.Fatalf("scaling scenario %q breaks registry order", name)
		}
		previous = position
	}
}

func scalingSample(milliseconds float64, gitProcesses int) Sample {
	return Sample{Duration: time.Duration(milliseconds * float64(time.Millisecond)), GitProcesses: gitProcesses}
}

func scalingTestPoint(active, depth int, scenario string, milliseconds float64, gitProcesses int) ScalingPoint {
	spec := ScalingPointSpec{ActiveTasks: active, OperationsPerTask: depth}
	fixture, err := spec.FixtureSpec("sha1")
	if err != nil {
		panic(err)
	}
	return ScalingPoint{
		Name:    spec.Name(),
		Spec:    spec,
		Fixture: fixture,
		Scenarios: []ScenarioResult{{
			Name:    scenario,
			Surface: "cold-cli",
			Samples: []Sample{scalingSample(milliseconds, gitProcesses)},
		}},
	}
}

// Mutation witness: computing one combined slope over every point would mix a
// five-fold task-count step with a five-fold history-depth step and report a
// single number the story explicitly asks to keep separate.
func TestComputeScalingSlopesSeparatesTaskCountFromHistoryDepth(t *testing.T) {
	points := []ScalingPoint{
		scalingTestPoint(1000, 20, "cli-update", 40, 12),
		scalingTestPoint(500, 100, "cli-update", 40, 6),
		scalingTestPoint(100, 20, "cli-update", 10, 3),
		scalingTestPoint(500, 20, "cli-update", 20, 6),
	}

	slopes := ComputeScalingSlopes(points)
	type segment struct {
		axis      string
		metric    string
		fromPoint string
		toPoint   string
		dimension float64
		value     float64
		slope     float64
		defined   bool
	}
	got := make([]segment, len(slopes))
	for index, slope := range slopes {
		got[index] = segment{
			axis:      slope.Axis,
			metric:    slope.Metric,
			fromPoint: slope.FromPoint,
			toPoint:   slope.ToPoint,
			dimension: slope.DimensionRatio,
			value:     slope.ValueRatio,
			slope:     slope.LogLogSlope,
			defined:   slope.Defined,
		}
	}
	logTwoOverLogFive := math.Log(2) / math.Log(5)
	want := []segment{
		{ScalingAxisTaskCount, ScalingMetricMedianMilliseconds, "active-100-depth-20", "active-500-depth-20", 5, 2, logTwoOverLogFive, true},
		{ScalingAxisTaskCount, ScalingMetricP95Milliseconds, "active-100-depth-20", "active-500-depth-20", 5, 2, logTwoOverLogFive, true},
		{ScalingAxisTaskCount, ScalingMetricP95GitProcesses, "active-100-depth-20", "active-500-depth-20", 5, 2, logTwoOverLogFive, true},
		{ScalingAxisTaskCount, ScalingMetricMedianMilliseconds, "active-500-depth-20", "active-1000-depth-20", 2, 2, 1, true},
		{ScalingAxisTaskCount, ScalingMetricP95Milliseconds, "active-500-depth-20", "active-1000-depth-20", 2, 2, 1, true},
		{ScalingAxisTaskCount, ScalingMetricP95GitProcesses, "active-500-depth-20", "active-1000-depth-20", 2, 2, 1, true},
		{ScalingAxisHistoryDepth, ScalingMetricMedianMilliseconds, "active-500-depth-20", "active-500-depth-100", 5, 2, logTwoOverLogFive, true},
		{ScalingAxisHistoryDepth, ScalingMetricP95Milliseconds, "active-500-depth-20", "active-500-depth-100", 5, 2, logTwoOverLogFive, true},
		{ScalingAxisHistoryDepth, ScalingMetricP95GitProcesses, "active-500-depth-20", "active-500-depth-100", 5, 1, 0, true},
	}
	if len(got) != len(want) {
		t.Fatalf("slope count = %d, want %d: %#v", len(got), len(want), got)
	}
	for index := range want {
		if got[index].axis != want[index].axis || got[index].metric != want[index].metric ||
			got[index].fromPoint != want[index].fromPoint || got[index].toPoint != want[index].toPoint ||
			got[index].defined != want[index].defined ||
			math.Abs(got[index].dimension-want[index].dimension) > 1e-9 ||
			math.Abs(got[index].value-want[index].value) > 1e-9 ||
			math.Abs(got[index].slope-want[index].slope) > 1e-9 {
			t.Fatalf("slope %d = %#v, want %#v", index, got[index], want[index])
		}
	}
	for _, slope := range slopes {
		if slope.Scenario != "cli-update" {
			t.Fatalf("slope scenario = %q, want cli-update", slope.Scenario)
		}
	}
}

// Mutation witness: dividing without guarding a zero baseline, an identical
// dimension, or a scenario missing from one point would publish infinities,
// NaNs, or a slope for a comparison that was never measured.
func TestComputeScalingSlopesHandlesDegenerateInputs(t *testing.T) {
	t.Run("single point has no segment", func(t *testing.T) {
		if slopes := ComputeScalingSlopes([]ScalingPoint{scalingTestPoint(100, 20, "cli-update", 10, 3)}); len(slopes) != 0 {
			t.Fatalf("single-point slopes = %#v, want none", slopes)
		}
	})

	t.Run("no points", func(t *testing.T) {
		if slopes := ComputeScalingSlopes(nil); len(slopes) != 0 {
			t.Fatalf("empty slopes = %#v, want none", slopes)
		}
	})

	t.Run("scenario measured at only one point", func(t *testing.T) {
		points := []ScalingPoint{
			scalingTestPoint(100, 20, "cli-update", 10, 3),
			scalingTestPoint(500, 20, "cli-list", 20, 6),
		}
		if slopes := ComputeScalingSlopes(points); len(slopes) != 0 {
			t.Fatalf("disjoint-scenario slopes = %#v, want none", slopes)
		}
	})

	t.Run("zero baseline value is undefined", func(t *testing.T) {
		points := []ScalingPoint{
			scalingTestPoint(100, 20, "cli-update", 0, 0),
			scalingTestPoint(500, 20, "cli-update", 20, 6),
		}
		slopes := ComputeScalingSlopes(points)
		if len(slopes) != 3 {
			t.Fatalf("slope count = %d, want 3", len(slopes))
		}
		for _, slope := range slopes {
			if slope.Defined || slope.LogLogSlope != 0 || slope.ValueRatio != 0 {
				t.Fatalf("zero-baseline slope = %#v, want undefined with zero ratio and slope", slope)
			}
			if !strings.Contains(slope.Note, "nonpositive") {
				t.Fatalf("zero-baseline note = %q, want a nonpositive-value explanation", slope.Note)
			}
			if slope.DimensionRatio != 5 {
				t.Fatalf("zero-baseline dimension ratio = %v, want 5", slope.DimensionRatio)
			}
		}
	})

	t.Run("identical dimensions are undefined", func(t *testing.T) {
		points := []ScalingPoint{
			scalingTestPoint(500, 20, "cli-update", 10, 3),
			scalingTestPoint(500, 20, "cli-update", 20, 6),
		}
		slopes := ComputeScalingSlopes(points)
		if len(slopes) != 6 {
			t.Fatalf("slope count = %d, want 6 undefined segments", len(slopes))
		}
		for _, slope := range slopes {
			if slope.Defined || slope.LogLogSlope != 0 {
				t.Fatalf("identical-dimension slope = %#v, want undefined", slope)
			}
			if !strings.Contains(slope.Note, "identical") {
				t.Fatalf("identical-dimension note = %q, want an identical-dimension explanation", slope.Note)
			}
		}
	})

	t.Run("zero measured value at the second point stays undefined", func(t *testing.T) {
		points := []ScalingPoint{
			scalingTestPoint(100, 20, "cli-update", 10, 3),
			scalingTestPoint(500, 20, "cli-update", 0, 0),
		}
		for _, slope := range ComputeScalingSlopes(points) {
			if slope.Defined {
				t.Fatalf("zero-result slope = %#v, want undefined", slope)
			}
		}
	})
}

func scalingReportFixture() ScalingReport {
	point := func(active, depth int, results ...ScenarioResult) ScalingPoint {
		spec := ScalingPointSpec{ActiveTasks: active, OperationsPerTask: depth}
		fixture, err := spec.FixtureSpec("sha256")
		if err != nil {
			panic(err)
		}
		return ScalingPoint{Name: spec.Name(), Spec: spec, Fixture: fixture, Scenarios: results}
	}
	result := func(name string, milliseconds float64, gitProcesses int) ScenarioResult {
		return ScenarioResult{
			Name:    name,
			Surface: "cold-cli",
			Samples: []Sample{scalingSample(milliseconds, gitProcesses)},
		}
	}
	return ScalingReport{
		Format:       ScalingReportFormat,
		Version:      ScalingReportVersion,
		GeneratedAt:  time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC),
		Environment:  Environment{OS: "darwin", Arch: "arm64", GitVersion: "git version 2.51.0", GoVersion: "go version go1.25.0", WorkbookVersion: "0.1.0", WorkbookCommit: "abc123", WorkbookBinarySHA256: "deadbeef"},
		ObjectFormat: "sha256",
		Samples:      1,
		Points: []ScalingPoint{
			point(1000, 20, result("cli-update", 40, 12), result("cli-create", 41, 13)),
			point(100, 20, result("cli-create", 11, 3), result("cli-update", 10, 3)),
			point(500, 20, result("cli-update", 20, 6), result("cli-create", 21, 7)),
		},
	}
}

// Mutation witness: serializing the points in call order, keeping per-point
// scenarios in measurement order, or trusting a caller-supplied slope list would
// make two identical measurements produce different reports.
func TestScalingReportSerializesDeterministicallyRegardlessOfInputOrder(t *testing.T) {
	report := scalingReportFixture()
	shuffled := scalingReportFixture()
	shuffled.Points = []ScalingPoint{shuffled.Points[1], shuffled.Points[0], shuffled.Points[2]}
	shuffled.Points[0].Scenarios = []ScenarioResult{shuffled.Points[0].Scenarios[1], shuffled.Points[0].Scenarios[0]}
	shuffled.Slopes = []ScalingSlope{{Axis: "invented", Scenario: "cli-update", Metric: "made-up"}}

	var first, second bytes.Buffer
	if err := report.WriteJSON(&first); err != nil {
		t.Fatal(err)
	}
	if err := shuffled.WriteJSON(&second); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatalf("JSON reports differ:\n%s\n%s", first.String(), second.String())
	}

	var decoded ScalingReport
	if err := json.Unmarshal(first.Bytes(), &decoded); err != nil {
		t.Fatalf("decode scaling report: %v", err)
	}
	if decoded.Format != ScalingReportFormat || decoded.Version != ScalingReportVersion {
		t.Fatalf("report identity = %q v%d, want %q v%d", decoded.Format, decoded.Version, ScalingReportFormat, ScalingReportVersion)
	}
	gotPoints := make([]string, len(decoded.Points))
	for index, point := range decoded.Points {
		gotPoints[index] = point.Name
	}
	if want := []string{"active-100-depth-20", "active-500-depth-20", "active-1000-depth-20"}; !reflect.DeepEqual(gotPoints, want) {
		t.Fatalf("point order = %#v, want %#v", gotPoints, want)
	}
	for _, point := range decoded.Points {
		gotScenarios := make([]string, len(point.Scenarios))
		for index, scenario := range point.Scenarios {
			gotScenarios[index] = scenario.Name
			if scenario.Outcome != "completed" {
				t.Fatalf("%s scenario %s outcome = %q, want completed", point.Name, scenario.Name, scenario.Outcome)
			}
		}
		if want := []string{"cli-create", "cli-update"}; !reflect.DeepEqual(gotScenarios, want) {
			t.Fatalf("%s scenario order = %#v, want %#v", point.Name, gotScenarios, want)
		}
		if point.Fixture.ObjectFormat != "sha256" || point.Fixture.TotalTasks != point.Spec.ActiveTasks+point.Fixture.TombstonedTasks {
			t.Fatalf("%s fixture = %#v, want the realized representative shape", point.Name, point.Fixture)
		}
	}
	if len(decoded.Slopes) == 0 {
		t.Fatal("scaling report has no slopes")
	}
	for _, slope := range decoded.Slopes {
		if slope.Axis != ScalingAxisTaskCount {
			t.Fatalf("slope axis = %q, want the recomputed task-count axis", slope.Axis)
		}
	}

	var markdownFirst, markdownSecond bytes.Buffer
	if err := report.WriteMarkdown(&markdownFirst); err != nil {
		t.Fatal(err)
	}
	if err := shuffled.WriteMarkdown(&markdownSecond); err != nil {
		t.Fatal(err)
	}
	if markdownFirst.String() != markdownSecond.String() {
		t.Fatalf("Markdown reports differ:\n%s\n%s", markdownFirst.String(), markdownSecond.String())
	}
	markdown := markdownFirst.String()
	for _, fragment := range []string{
		"active-100-depth-20",
		"Total tasks",
		"Tombstoned",
		"sha256",
		ScalingAxisTaskCount,
		ScalingMetricMedianMilliseconds,
	} {
		if !strings.Contains(markdown, fragment) {
			t.Fatalf("Markdown report is missing %q:\n%s", fragment, markdown)
		}
	}
	for _, forbidden := range []string{"pass", "miss", "Target"} {
		if strings.Contains(markdown, forbidden) {
			t.Fatalf("Markdown report contains threshold language %q:\n%s", forbidden, markdown)
		}
	}
}

type scalingRunnerCall struct {
	family    string
	root      string
	scenarios []string
	spec      RunSpec
}

func recordingScalingDependencies(calls *[]scalingRunnerCall) scalingDependencies {
	runner := func(family string) func(context.Context, RunSpec, string, []string) ([]ScenarioResult, error) {
		return func(_ context.Context, spec RunSpec, root string, selected []string) ([]ScenarioResult, error) {
			*calls = append(*calls, scalingRunnerCall{family: family, root: root, scenarios: append([]string(nil), selected...), spec: spec})
			results := make([]ScenarioResult, len(selected))
			for index, name := range selected {
				results[index] = ScenarioResult{Name: name, Surface: family, Samples: []Sample{scalingSample(float64(index+1), index+1)}}
			}
			return results, nil
		}
	}
	return scalingDependencies{
		runCold:       runner("cold-cli"),
		runRemote:     runner("remote-sync"),
		runValidation: runner("history-validation"),
	}
}

// Mutation witness: measuring the matrix inside this runner instead of
// delegating to the existing cold, remote, and validation runners would move
// fixture construction, projection seeding, and cache seeding inside the timed
// commands.
func TestRunScalingMatrixDelegatesEveryPointToTheExistingScenarioRunners(t *testing.T) {
	var calls []scalingRunnerCall
	spec := ScalingSpec{
		WorkbookBinary: "workbook",
		ObjectFormat:   "sha256",
		Samples:        3,
		CommandTimeout: 7 * time.Second,
		Points: []ScalingPointSpec{
			{ActiveTasks: 500, OperationsPerTask: 100},
			{ActiveTasks: 100, OperationsPerTask: 20},
		},
	}
	fixtureRoot := t.TempDir()

	points, err := runScalingMatrix(context.Background(), spec, fixtureRoot, recordingScalingDependencies(&calls))
	if err != nil {
		t.Fatal(err)
	}

	gotPoints := make([]string, len(points))
	for index, point := range points {
		gotPoints[index] = point.Name
	}
	if want := []string{"active-100-depth-20", "active-500-depth-100"}; !reflect.DeepEqual(gotPoints, want) {
		t.Fatalf("measured point order = %#v, want %#v", gotPoints, want)
	}
	for _, point := range points {
		wantFixture, err := point.Spec.FixtureSpec("sha256")
		if err != nil {
			t.Fatal(err)
		}
		if point.Fixture != wantFixture {
			t.Fatalf("%s realized fixture = %#v, want %#v", point.Name, point.Fixture, wantFixture)
		}
		gotScenarios := make([]string, len(point.Scenarios))
		for index, scenario := range point.Scenarios {
			gotScenarios[index] = scenario.Name
		}
		if !reflect.DeepEqual(gotScenarios, ScalingScenarioNames()) {
			t.Fatalf("%s scenarios = %#v, want %#v", point.Name, gotScenarios, ScalingScenarioNames())
		}
	}

	wantCalls := []struct {
		family    string
		scenarios []string
		active    int
		depth     int
	}{
		{"cold-cli", []string{"cli-create", "cli-depend", "cli-list", "cli-move", "cli-update"}, 100, 20},
		{"remote-sync", []string{"sync-already-synchronized", "sync-small-changed-ref-set"}, 100, 20},
		{"history-validation", []string{"validate-full-history", "validate-cached-unchanged"}, 100, 20},
		{"cold-cli", []string{"cli-create", "cli-depend", "cli-list", "cli-move", "cli-update"}, 500, 100},
		{"remote-sync", []string{"sync-already-synchronized", "sync-small-changed-ref-set"}, 500, 100},
		{"history-validation", []string{"validate-full-history", "validate-cached-unchanged"}, 500, 100},
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("runner calls = %d, want %d: %#v", len(calls), len(wantCalls), calls)
	}
	roots := make(map[string]struct{}, len(calls))
	for index, want := range wantCalls {
		call := calls[index]
		if call.family != want.family || !reflect.DeepEqual(call.scenarios, want.scenarios) {
			t.Fatalf("call %d = %s %#v, want %s %#v", index, call.family, call.scenarios, want.family, want.scenarios)
		}
		if call.spec.WorkbookBinary != "workbook" || call.spec.Samples != 3 || call.spec.CommandTimeout != 7*time.Second {
			t.Fatalf("call %d run spec = %#v, want the matrix binary, samples, and timeout", index, call.spec)
		}
		if call.spec.Fixture.ActiveTasks != want.active || call.spec.Fixture.OperationsPerTask != want.depth || call.spec.Fixture.ObjectFormat != "sha256" {
			t.Fatalf("call %d fixture = %#v, want %d active tasks at depth %d in sha256", index, call.spec.Fixture, want.active, want.depth)
		}
		if !strings.HasPrefix(call.root, fixtureRoot) {
			t.Fatalf("call %d root = %q, want a path under %q", index, call.root, fixtureRoot)
		}
		if _, duplicate := roots[call.root]; duplicate {
			t.Fatalf("call %d reuses fixture root %q", index, call.root)
		}
		roots[call.root] = struct{}{}
	}
}

// Mutation witness: silently dropping a scenario a runner failed to return
// would publish an incomplete matrix that still looks like complete evidence.
func TestRunScalingMatrixRejectsIncompleteScenarioCoverage(t *testing.T) {
	dependencies := recordingScalingDependencies(&[]scalingRunnerCall{})
	dependencies.runValidation = func(_ context.Context, _ RunSpec, _ string, selected []string) ([]ScenarioResult, error) {
		return []ScenarioResult{{Name: selected[0], Surface: "history-validation", Samples: []Sample{scalingSample(1, 1)}}}, nil
	}
	spec := ScalingSpec{
		WorkbookBinary: "workbook",
		ObjectFormat:   "sha1",
		Samples:        1,
		CommandTimeout: time.Second,
		Points:         []ScalingPointSpec{{ActiveTasks: 100, OperationsPerTask: 20}},
	}

	_, err := runScalingMatrix(context.Background(), spec, t.TempDir(), dependencies)
	if err == nil || !strings.Contains(err.Error(), "validate-cached-unchanged") {
		t.Fatalf("incomplete coverage error = %v, want a missing validate-cached-unchanged report", err)
	}
}

func TestRunScalingMatrixRejectsUnmeasurableSpecs(t *testing.T) {
	base := ScalingSpec{
		WorkbookBinary: "workbook",
		ObjectFormat:   "sha1",
		Samples:        1,
		CommandTimeout: time.Second,
		Points:         []ScalingPointSpec{{ActiveTasks: 100, OperationsPerTask: 20}},
	}
	tests := []struct {
		name   string
		change func(*ScalingSpec)
		want   string
	}{
		{"missing binary", func(spec *ScalingSpec) { spec.WorkbookBinary = "" }, "workbook binary is required"},
		{"no points", func(spec *ScalingSpec) { spec.Points = nil }, "at least one scaling point is required"},
		{"samples below one", func(spec *ScalingSpec) { spec.Samples = 0 }, "samples must be positive"},
		{"nonpositive timeout", func(spec *ScalingSpec) { spec.CommandTimeout = 0 }, "command timeout must be positive"},
		{"unsupported object format", func(spec *ScalingSpec) { spec.ObjectFormat = "sha512" }, "unsupported object format"},
		{"unmeasurable point", func(spec *ScalingSpec) { spec.Points[0].ActiveTasks = 9 }, "at least 10 active tasks"},
		{
			"duplicate point",
			func(spec *ScalingSpec) {
				spec.Points = append(spec.Points, ScalingPointSpec{ActiveTasks: 100, OperationsPerTask: 20})
			},
			"duplicate scaling point",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := base
			spec.Points = append([]ScalingPointSpec(nil), base.Points...)
			test.change(&spec)
			var calls []scalingRunnerCall
			_, err := runScalingMatrix(context.Background(), spec, t.TempDir(), recordingScalingDependencies(&calls))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("runScalingMatrix error = %v, want %q", err, test.want)
			}
			if len(calls) != 0 {
				t.Fatalf("rejected spec still ran %d measurements", len(calls))
			}
		})
	}
}
