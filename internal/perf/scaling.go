package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// activeTasksPerTombstone keeps the representative tombstone population
// proportional to the active population. The default acceptance fixture is 25
// tombstoned tasks in 500, so every scaling point uses the same one-in-twenty
// ratio and each matrix axis varies exactly one dimension.
const activeTasksPerTombstone = 20

// scalingMinimumActiveTasks is the smallest active population every scaling
// scenario can measure. The small-changed-ref-set sync topology needs ten
// active tasks; every other selected scenario needs fewer.
const scalingMinimumActiveTasks = 10

// ScalingPointSpec names one measured fixture point on the scaling matrix. It
// records the story's dimensions directly: active tasks and history depth.
type ScalingPointSpec struct {
	ActiveTasks       int `json:"activeTasks"`
	OperationsPerTask int `json:"operationsPerTask"`
}

var defaultScalingPoints = []ScalingPointSpec{
	{ActiveTasks: 100, OperationsPerTask: 20},
	{ActiveTasks: 500, OperationsPerTask: 20},
	{ActiveTasks: 500, OperationsPerTask: 100},
	{ActiveTasks: 1000, OperationsPerTask: 20},
}

var scalingScenarioRegistry = []string{
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

// DefaultScalingPoints returns the story matrix ordered by ascending active
// task count and then ascending history depth.
func DefaultScalingPoints() []ScalingPointSpec {
	return append([]ScalingPointSpec(nil), defaultScalingPoints...)
}

// ScalingScenarioNames returns the measured scaling scenarios in the harness's
// stable registry order.
func ScalingScenarioNames() []string {
	return append([]string(nil), scalingScenarioRegistry...)
}

// Name returns the point's stable identifier, which records both axes.
func (point ScalingPointSpec) Name() string {
	return fmt.Sprintf("active-%d-depth-%d", point.ActiveTasks, point.OperationsPerTask)
}

// FixtureSpec maps a matrix point onto the realized representative fixture
// shape. Tombstoned tasks scale with the active population and the total ref
// count is their sum, so a reader never has to infer the measured shape.
func (point ScalingPointSpec) FixtureSpec(objectFormat string) (FixtureSpec, error) {
	if point.ActiveTasks < scalingMinimumActiveTasks {
		return FixtureSpec{}, fmt.Errorf("scaling point %s requires at least %d active tasks", point.Name(), scalingMinimumActiveTasks)
	}
	if point.OperationsPerTask < 2 {
		return FixtureSpec{}, fmt.Errorf("scaling point %s requires at least 2 operations per task", point.Name())
	}
	if objectFormat != "sha1" && objectFormat != "sha256" {
		return FixtureSpec{}, fmt.Errorf("unsupported object format %q", objectFormat)
	}
	tombstoned := point.ActiveTasks / activeTasksPerTombstone
	if tombstoned < 1 {
		tombstoned = 1
	}
	return FixtureSpec{
		TotalTasks:        point.ActiveTasks + tombstoned,
		ActiveTasks:       point.ActiveTasks,
		TombstonedTasks:   tombstoned,
		OperationsPerTask: point.OperationsPerTask,
		ObjectFormat:      objectFormat,
	}, nil
}

// Scaling axes. Task-count slopes hold history depth constant; history-depth
// slopes hold the active task population constant.
const (
	ScalingAxisTaskCount    = "task-count"
	ScalingAxisHistoryDepth = "history-depth"
)

// Scaling metrics. Each is read from the measured samples of one scenario at
// one point.
const (
	ScalingMetricMedianMilliseconds = "medianMilliseconds"
	ScalingMetricP95Milliseconds    = "p95Milliseconds"
	ScalingMetricP95GitProcesses    = "p95GitProcesses"
)

var scalingMetrics = []string{
	ScalingMetricMedianMilliseconds,
	ScalingMetricP95Milliseconds,
	ScalingMetricP95GitProcesses,
}

// ScalingPoint records one measured matrix point: the requested dimensions, the
// realized representative fixture shape, and every measured scenario.
type ScalingPoint struct {
	Name      string           `json:"name"`
	Spec      ScalingPointSpec `json:"spec"`
	Fixture   FixtureSpec      `json:"fixture"`
	Scenarios []ScenarioResult `json:"scenarios"`
}

// ScalingSlope describes one measured scenario metric across one segment of one
// axis. It is descriptive evidence: there is no pass or fail classification and
// no threshold.
type ScalingSlope struct {
	Axis           string  `json:"axis"`
	Scenario       string  `json:"scenario"`
	Metric         string  `json:"metric"`
	FromPoint      string  `json:"fromPoint"`
	ToPoint        string  `json:"toPoint"`
	FromDimension  int     `json:"fromDimension"`
	ToDimension    int     `json:"toDimension"`
	FromValue      float64 `json:"fromValue"`
	ToValue        float64 `json:"toValue"`
	DimensionRatio float64 `json:"dimensionRatio"`
	ValueRatio     float64 `json:"valueRatio"`
	LogLogSlope    float64 `json:"logLogSlope"`
	Defined        bool    `json:"defined"`
	Note           string  `json:"note,omitempty"`
}

// ComputeScalingSlopes derives descriptive log-log slopes from measured points.
// Task-count segments compare points that share a history depth; history-depth
// segments compare points that share an active task population. Segments are
// consecutive along each axis, so a slope always names the two points it came
// from.
func ComputeScalingSlopes(points []ScalingPoint) []ScalingSlope {
	slopes := make([]ScalingSlope, 0)
	slopes = append(slopes, axisSlopes(
		points,
		ScalingAxisTaskCount,
		func(point ScalingPoint) int { return point.Spec.OperationsPerTask },
		func(point ScalingPoint) int { return point.Spec.ActiveTasks },
	)...)
	slopes = append(slopes, axisSlopes(
		points,
		ScalingAxisHistoryDepth,
		func(point ScalingPoint) int { return point.Spec.ActiveTasks },
		func(point ScalingPoint) int { return point.Spec.OperationsPerTask },
	)...)
	return slopes
}

// axisSlopes groups points by the axis's held-constant dimension and emits one
// slope record per consecutive pair, scenario, and metric.
func axisSlopes(points []ScalingPoint, axis string, groupKey, dimension func(ScalingPoint) int) []ScalingSlope {
	grouped := make(map[int][]ScalingPoint)
	for _, point := range points {
		grouped[groupKey(point)] = append(grouped[groupKey(point)], point)
	}
	keys := make([]int, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Ints(keys)

	var slopes []ScalingSlope
	for _, key := range keys {
		group := grouped[key]
		sort.SliceStable(group, func(i, j int) bool { return dimension(group[i]) < dimension(group[j]) })
		for index := 1; index < len(group); index++ {
			from, to := group[index-1], group[index]
			for _, scenario := range commonScenarioNames(from, to) {
				fromSummary := Summarize(scenarioSamples(from, scenario))
				toSummary := Summarize(scenarioSamples(to, scenario))
				for _, metric := range scalingMetrics {
					slopes = append(slopes, newScalingSlope(
						axis, scenario, metric,
						from, to,
						dimension(from), dimension(to),
						scalingMetricValue(fromSummary, metric),
						scalingMetricValue(toSummary, metric),
					))
				}
			}
		}
	}
	return slopes
}

func newScalingSlope(axis, scenario, metric string, from, to ScalingPoint, fromDimension, toDimension int, fromValue, toValue float64) ScalingSlope {
	slope := ScalingSlope{
		Axis:          axis,
		Scenario:      scenario,
		Metric:        metric,
		FromPoint:     from.Name,
		ToPoint:       to.Name,
		FromDimension: fromDimension,
		ToDimension:   toDimension,
		FromValue:     fromValue,
		ToValue:       toValue,
	}
	if fromDimension > 0 && toDimension > 0 {
		slope.DimensionRatio = float64(toDimension) / float64(fromDimension)
	}
	if fromValue > 0 {
		slope.ValueRatio = toValue / fromValue
	}
	switch {
	case fromDimension <= 0 || toDimension <= 0:
		slope.Note = "nonpositive dimension"
	case fromDimension == toDimension:
		slope.Note = "identical dimensions"
	case fromValue <= 0 || toValue <= 0:
		slope.Note = "nonpositive measured value"
	default:
		slope.Defined = true
		slope.LogLogSlope = math.Log(slope.ValueRatio) / math.Log(slope.DimensionRatio)
	}
	if !slope.Defined {
		slope.LogLogSlope = 0
	}
	return slope
}

// commonScenarioNames returns the scenarios measured at both points, in the
// order the earlier point recorded them.
func commonScenarioNames(from, to ScalingPoint) []string {
	present := make(map[string]struct{}, len(to.Scenarios))
	for _, scenario := range to.Scenarios {
		present[scenario.Name] = struct{}{}
	}
	names := make([]string, 0, len(from.Scenarios))
	for _, scenario := range from.Scenarios {
		if _, shared := present[scenario.Name]; shared {
			names = append(names, scenario.Name)
		}
	}
	return names
}

func scenarioSamples(point ScalingPoint, name string) []Sample {
	for _, scenario := range point.Scenarios {
		if scenario.Name == name {
			return scenario.Samples
		}
	}
	return nil
}

func scalingMetricValue(summary Summary, metric string) float64 {
	switch metric {
	case ScalingMetricMedianMilliseconds:
		return summary.MedianMilliseconds
	case ScalingMetricP95Milliseconds:
		return summary.P95Milliseconds
	case ScalingMetricP95GitProcesses:
		return float64(summary.P95GitProcesses)
	default:
		return 0
	}
}

const (
	ScalingReportFormat  = "workbook.performance-scaling-report"
	ScalingReportVersion = 1
)

// ScalingReport is the machine-readable evidence for one scaling matrix run.
// It is a separate versioned format from the single-fixture performance report
// because it carries several fixture points and descriptive slopes rather than
// one fixture and one target classification.
type ScalingReport struct {
	Format       string         `json:"format"`
	Version      int            `json:"version"`
	Phase        string         `json:"phase"`
	GeneratedAt  time.Time      `json:"generatedAt"`
	Environment  Environment    `json:"environment"`
	ObjectFormat string         `json:"objectFormat"`
	Samples      int            `json:"samples"`
	Points       []ScalingPoint `json:"points"`
	Slopes       []ScalingSlope `json:"slopes"`
}

func (report ScalingReport) WriteJSON(w io.Writer) error {
	return json.NewEncoder(w).Encode(report.normalized())
}

func (report ScalingReport) WriteMarkdown(w io.Writer) error {
	report = report.normalized()
	sections := []func(io.Writer, ScalingReport) error{
		writeScalingHeader,
		writeScalingPointTable,
		writeScalingMeasurements,
		writeScalingSlopeTable,
	}
	for _, section := range sections {
		if err := section(w, report); err != nil {
			return err
		}
	}
	return nil
}

func writeScalingHeader(w io.Writer, report ScalingReport) error {
	_, err := fmt.Fprintf(
		w,
		"# Workbook performance scaling report\n\nPhase: %s\n\nObject format: %s\n\nSamples per scenario: %d\n",
		report.Phase, report.ObjectFormat, report.Samples,
	)
	return err
}

func writeScalingPointTable(w io.Writer, report ScalingReport) error {
	if _, err := fmt.Fprintln(w, "\n## Matrix points"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| Point | Active tasks | Tombstoned tasks | Total tasks | History depth | Object format |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| --- | ---: | ---: | ---: | ---: | --- |"); err != nil {
		return err
	}
	for _, point := range report.Points {
		if _, err := fmt.Fprintf(
			w,
			"| %s | %d | %d | %d | %d | %s |\n",
			point.Name,
			point.Fixture.ActiveTasks,
			point.Fixture.TombstonedTasks,
			point.Fixture.TotalTasks,
			point.Fixture.OperationsPerTask,
			point.Fixture.ObjectFormat,
		); err != nil {
			return err
		}
	}
	return nil
}

func writeScalingMeasurements(w io.Writer, report ScalingReport) error {
	if _, err := fmt.Fprintln(w, "\n## Measurements"); err != nil {
		return err
	}
	for _, point := range report.Points {
		if _, err := fmt.Fprintf(w, "\n### %s\n\n", point.Name); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, "| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |"); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, "| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |"); err != nil {
			return err
		}
		for _, scenario := range point.Scenarios {
			if _, err := fmt.Fprintf(
				w,
				"| %s | %s | %d | %d | %.2f | %.2f | %.2f | %d | %s |\n",
				scenario.Name,
				scenario.Surface,
				scenario.Summary.Completed,
				scenario.Summary.TimedOut,
				scenario.Summary.MinMilliseconds,
				scenario.Summary.MedianMilliseconds,
				scenario.Summary.P95Milliseconds,
				scenario.Summary.P95GitProcesses,
				scenario.Outcome,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeScalingSlopeTable(w io.Writer, report ScalingReport) error {
	if _, err := fmt.Fprintln(w, "\n## Observed slopes"); err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, "\nEach row is a descriptive log-log slope between two consecutive measured points on one axis. A slope carries no budget and no classification.\n\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| Axis | Scenario | Metric | From | To | Dimension ratio | Value ratio | Log-log slope | Note |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| --- | --- | --- | --- | --- | ---: | ---: | ---: | --- |"); err != nil {
		return err
	}
	for _, slope := range report.Slopes {
		logLog := "-"
		note := slope.Note
		if slope.Defined {
			logLog = fmt.Sprintf("%.4f", slope.LogLogSlope)
		}
		if note == "" {
			note = "-"
		}
		if _, err := fmt.Fprintf(
			w,
			"| %s | %s | %s | %s | %s | %.4f | %.4f | %s | %s |\n",
			slope.Axis, slope.Scenario, slope.Metric, slope.FromPoint, slope.ToPoint,
			slope.DimensionRatio, slope.ValueRatio, logLog, note,
		); err != nil {
			return err
		}
	}
	return nil
}

// normalized orders points and scenarios deterministically, recomputes every
// summary from the retained samples, strips single-fixture duration budgets
// from scaling evidence, and recomputes the slopes from the measured points.
func (report ScalingReport) normalized() ScalingReport {
	points := append([]ScalingPoint(nil), report.Points...)
	for index := range points {
		point := &points[index]
		point.Name = point.Spec.Name()
		scenarios := append([]ScenarioResult(nil), point.Scenarios...)
		for scenarioIndex := range scenarios {
			scenario := &scenarios[scenarioIndex]
			scenario.Samples = append([]Sample(nil), scenario.Samples...)
			for sampleIndex := range scenario.Samples {
				scenario.Samples[sampleIndex].Milliseconds = durationMilliseconds(scenario.Samples[sampleIndex])
			}
			scenario.Target = nil
			scenario.Summary = Summarize(scenario.Samples)
			scenario.Outcome = scenarioOutcome(*scenario)
		}
		sort.SliceStable(scenarios, func(i, j int) bool {
			left, right := scalingScenarioPosition(scenarios[i].Name), scalingScenarioPosition(scenarios[j].Name)
			if left != right {
				return left < right
			}
			return scenarios[i].Name < scenarios[j].Name
		})
		point.Scenarios = scenarios
	}
	sort.SliceStable(points, func(i, j int) bool {
		if points[i].Spec.ActiveTasks != points[j].Spec.ActiveTasks {
			return points[i].Spec.ActiveTasks < points[j].Spec.ActiveTasks
		}
		return points[i].Spec.OperationsPerTask < points[j].Spec.OperationsPerTask
	})
	report.Points = points
	report.Slopes = ComputeScalingSlopes(points)
	return report
}

func scalingScenarioPosition(name string) int {
	for index, scenario := range scalingScenarioRegistry {
		if scenario == name {
			return index
		}
	}
	return len(scalingScenarioRegistry)
}

// ScalingSpec configures one scaling matrix run.
type ScalingSpec struct {
	WorkbookBinary string
	ObjectFormat   string
	Samples        int
	CommandTimeout time.Duration
	Points         []ScalingPointSpec
}

type scalingScenarioRunner func(context.Context, RunSpec, string, []string) ([]ScenarioResult, error)

type scalingDependencies struct {
	runCold       scalingScenarioRunner
	runRemote     scalingScenarioRunner
	runValidation scalingScenarioRunner
}

type scalingFamily struct {
	name      string
	scenarios []string
	run       func(scalingDependencies) scalingScenarioRunner
}

var scalingFamilies = []scalingFamily{
	{
		name:      "cold",
		scenarios: []string{"cli-create", "cli-depend", "cli-list", "cli-move", "cli-update"},
		run:       func(dependencies scalingDependencies) scalingScenarioRunner { return dependencies.runCold },
	},
	{
		name:      "remote",
		scenarios: []string{"sync-already-synchronized", "sync-small-changed-ref-set"},
		run:       func(dependencies scalingDependencies) scalingScenarioRunner { return dependencies.runRemote },
	},
	{
		name:      "validation",
		scenarios: []string{"validate-full-history", "validate-cached-unchanged"},
		run:       func(dependencies scalingDependencies) scalingScenarioRunner { return dependencies.runValidation },
	},
}

// RunScalingMatrix measures every matrix point with the existing cold CLI,
// remote synchronization, and history validation runners, so fixture
// construction, projection seeding, and cache seeding stay outside every timed
// command.
func RunScalingMatrix(ctx context.Context, spec ScalingSpec, fixtureRoot string) ([]ScalingPoint, error) {
	return runScalingMatrix(ctx, spec, fixtureRoot, scalingDependencies{
		runCold:       RunColdCLI,
		runRemote:     RunRemoteScenarios,
		runValidation: RunValidationScenarios,
	})
}

func runScalingMatrix(ctx context.Context, spec ScalingSpec, fixtureRoot string, dependencies scalingDependencies) ([]ScalingPoint, error) {
	fixtures, err := validateScalingSpec(spec, fixtureRoot, dependencies)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(fixtureRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create scaling fixture root: %w", err)
	}

	points := make([]ScalingPoint, 0, len(spec.Points))
	for index, pointSpec := range orderedScalingPoints(spec.Points) {
		fixture := fixtures[pointSpec]
		runSpec := RunSpec{
			WorkbookBinary: spec.WorkbookBinary,
			Fixture:        fixture,
			Samples:        spec.Samples,
			CommandTimeout: spec.CommandTimeout,
		}
		var measured []ScenarioResult
		for _, family := range scalingFamilies {
			root := filepath.Join(fixtureRoot, fmt.Sprintf("point-%02d-%s", index+1, pointSpec.Name()), family.name)
			results, err := family.run(dependencies)(ctx, runSpec, root, append([]string(nil), family.scenarios...))
			if err != nil {
				return nil, fmt.Errorf("measure %s %s scenarios: %w", pointSpec.Name(), family.name, err)
			}
			measured = append(measured, results...)
		}
		if err := requireCompleteScalingCoverage(pointSpec, measured); err != nil {
			return nil, err
		}
		points = append(points, ScalingPoint{
			Name:      pointSpec.Name(),
			Spec:      pointSpec,
			Fixture:   fixture,
			Scenarios: measured,
		})
	}
	return points, nil
}

func validateScalingSpec(spec ScalingSpec, fixtureRoot string, dependencies scalingDependencies) (map[ScalingPointSpec]FixtureSpec, error) {
	if spec.WorkbookBinary == "" {
		return nil, fmt.Errorf("workbook binary is required")
	}
	if len(spec.Points) == 0 {
		return nil, fmt.Errorf("at least one scaling point is required")
	}
	if spec.Samples < 1 {
		return nil, fmt.Errorf("samples must be positive")
	}
	if spec.CommandTimeout <= 0 {
		return nil, fmt.Errorf("command timeout must be positive")
	}
	if fixtureRoot == "" {
		return nil, fmt.Errorf("fixture root is required")
	}
	if dependencies.runCold == nil || dependencies.runRemote == nil || dependencies.runValidation == nil {
		return nil, fmt.Errorf("scaling scenario dependencies are required")
	}
	fixtures := make(map[ScalingPointSpec]FixtureSpec, len(spec.Points))
	for _, point := range spec.Points {
		if _, duplicate := fixtures[point]; duplicate {
			return nil, fmt.Errorf("duplicate scaling point %s", point.Name())
		}
		fixture, err := point.FixtureSpec(spec.ObjectFormat)
		if err != nil {
			return nil, err
		}
		fixtures[point] = fixture
	}
	return fixtures, nil
}

func orderedScalingPoints(points []ScalingPointSpec) []ScalingPointSpec {
	ordered := append([]ScalingPointSpec(nil), points...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].ActiveTasks != ordered[j].ActiveTasks {
			return ordered[i].ActiveTasks < ordered[j].ActiveTasks
		}
		return ordered[i].OperationsPerTask < ordered[j].OperationsPerTask
	})
	return ordered
}

func requireCompleteScalingCoverage(point ScalingPointSpec, measured []ScenarioResult) error {
	reported := make(map[string]struct{}, len(measured))
	for _, result := range measured {
		reported[result.Name] = struct{}{}
	}
	for _, name := range scalingScenarioRegistry {
		if _, found := reported[name]; !found {
			return fmt.Errorf("scaling point %s is missing a %s measurement", point.Name(), name)
		}
	}
	return nil
}
