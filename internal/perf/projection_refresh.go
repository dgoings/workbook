package perf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

const (
	// ProjectionRefreshFormat names the machine-readable change-count block
	// embedded in a performance report.
	ProjectionRefreshFormat = "workbook.projection-refresh"
	// ProjectionRefreshVersion versions that block from its first release.
	ProjectionRefreshVersion = 1

	projectionRefreshSurface   = "repository"
	projectionRefreshCacheFile = "cache.sqlite"
)

// projectionRefreshMutationOrigin keeps deterministic setup operation IDs and
// commit timestamps well after the generated fixture history.
var projectionRefreshMutationOrigin = benchmarkOrigin.AddDate(2, 0, 0)

// ProjectionRefreshPoint records one measured change-count point of the
// projection refresh family.
type ProjectionRefreshPoint struct {
	// Scenario is the registered benchmark scenario name.
	Scenario string `json:"scenario"`
	// ChangedTaskHeads is the exact number of canonical task refs advanced
	// outside the timed refresh before every sample at this point.
	ChangedTaskHeads int `json:"changedTaskHeads"`
	// Samples is the number of measured refreshes at this point.
	Samples int `json:"samples"`
	// TaskRefs is the number of refs/workbook/tasks/* refs the refresh had to
	// consider.
	TaskRefs int `json:"taskRefs"`
	// RefEnumerationMedianMilliseconds is the median untimed harness cost of
	// enumerating every task ref and object name immediately before the
	// measured refresh.
	RefEnumerationMedianMilliseconds float64 `json:"refEnumerationMedianMilliseconds"`
	// RefreshMedianMilliseconds and RefreshP95Milliseconds summarize the timed
	// end-to-end refresh command.
	RefreshMedianMilliseconds float64 `json:"refreshMedianMilliseconds"`
	RefreshP95Milliseconds    float64 `json:"refreshP95Milliseconds"`
	// RefreshMedianGitProcesses counts Git process starts inside the measured
	// refresh only.
	RefreshMedianGitProcesses int `json:"refreshMedianGitProcesses"`
	// ProjectedTaskRows is the number of task rows the refreshed SQLite
	// projection returned.
	ProjectedTaskRows int `json:"projectedTaskRows"`
	// ProjectionCacheBytes is the size of the disposable SQLite projection
	// after the final measured refresh at this point.
	ProjectionCacheBytes int64 `json:"projectionCacheBytes"`
}

// ProjectionRefreshSlope describes, without judging, how measured refresh
// latency moved across the change-count points.
type ProjectionRefreshSlope struct {
	Description                string  `json:"description"`
	BaselineMilliseconds       float64 `json:"baselineMilliseconds"`
	MaxChangedTaskHeads        int     `json:"maxChangedTaskHeads"`
	MaxChangedMilliseconds     float64 `json:"maxChangedMilliseconds"`
	MillisecondsPerChangedHead float64 `json:"millisecondsPerChangedHead"`
}

// ProjectionRefreshReport is the versioned change-count block written next to
// the scenario results.
type ProjectionRefreshReport struct {
	Format  string                   `json:"format"`
	Version int                      `json:"version"`
	Samples int                      `json:"samples"`
	Fixture FixtureSpec              `json:"fixture"`
	Points  []ProjectionRefreshPoint `json:"points"`
	Slope   ProjectionRefreshSlope   `json:"slope"`
}

type projectionRefreshDefinition struct {
	name         string
	changedHeads int
}

var projectionRefreshDefinitions = []projectionRefreshDefinition{
	{name: "projection-refresh-unchanged", changedHeads: 0},
	{name: "projection-refresh-one-changed", changedHeads: 1},
	{name: "projection-refresh-five-changed", changedHeads: 5},
	{name: "projection-refresh-fifty-changed", changedHeads: 50},
	{name: "projection-refresh-five-hundred-changed", changedHeads: 500},
}

type projectionRefreshDependencies struct {
	buildFixture   func(context.Context, string, FixtureSpec) (Fixture, error)
	runSetup       func(context.Context, CommandSpec) CommandMeasurement
	mutateHeads    func(context.Context, string, core.ProjectConfig, []string, int) error
	measureCommand func(context.Context, CommandSpec) CommandMeasurement
}

// ProjectionRefreshScenarioNames returns the ordered projection refresh
// change-count family.
func ProjectionRefreshScenarioNames() []string {
	names := make([]string, len(projectionRefreshDefinitions))
	for index, definition := range projectionRefreshDefinitions {
		names[index] = definition.name
	}
	return names
}

// IsProjectionRefreshScenario reports whether a scenario name belongs to the
// change-count family.
func IsProjectionRefreshScenario(name string) bool {
	for _, definition := range projectionRefreshDefinitions {
		if definition.name == name {
			return true
		}
	}
	return false
}

// RequireProjectionRefreshFixture rejects a selection whose largest change-count
// point exceeds the fixture's mutable active task population, before any
// benchmark work begins. Unselected family members are ignored.
func RequireProjectionRefreshFixture(selected []string, fixture FixtureSpec) error {
	definitions, err := selectProjectionRefreshDefinitions(projectionRefreshSelection(selected))
	if err != nil {
		return err
	}
	return requireProjectionRefreshFixture(definitions, fixture)
}

func projectionRefreshSelection(selected []string) []string {
	family := make([]string, 0, len(selected))
	for _, name := range selected {
		if IsProjectionRefreshScenario(name) {
			family = append(family, name)
		}
	}
	return family
}

// RunProjectionRefreshScenarios measures how the disposable SQLite projection
// refreshes when a known number of canonical task heads changed since the last
// refresh. Fixture construction, projection settling, and the head mutations
// are deliberately outside every measured sample.
func RunProjectionRefreshScenarios(
	ctx context.Context,
	spec RunSpec,
	fixtureRoot string,
	selected []string,
) ([]ScenarioResult, ProjectionRefreshReport, error) {
	return runProjectionRefreshScenarios(ctx, spec, fixtureRoot, selected, projectionRefreshDependencies{
		buildFixture: func(ctx context.Context, root string, fixture FixtureSpec) (Fixture, error) {
			return buildFixtureWithinTimeout(ctx, root, fixture, spec.CommandTimeout)
		},
		runSetup:       runValidationSetupCommand,
		mutateHeads:    mutateProjectionRefreshHeads,
		measureCommand: MeasureCommandOutput,
	})
}

func runProjectionRefreshScenarios(
	ctx context.Context,
	spec RunSpec,
	fixtureRoot string,
	selected []string,
	dependencies projectionRefreshDependencies,
) ([]ScenarioResult, ProjectionRefreshReport, error) {
	if spec.WorkbookBinary == "" {
		return nil, ProjectionRefreshReport{}, fmt.Errorf("workbook binary is required")
	}
	if spec.Samples < 1 {
		return nil, ProjectionRefreshReport{}, fmt.Errorf("samples must be positive")
	}
	if spec.CommandTimeout <= 0 {
		return nil, ProjectionRefreshReport{}, fmt.Errorf("command timeout must be positive")
	}
	if fixtureRoot == "" {
		return nil, ProjectionRefreshReport{}, fmt.Errorf("fixture root is required")
	}
	if dependencies.buildFixture == nil || dependencies.runSetup == nil ||
		dependencies.mutateHeads == nil || dependencies.measureCommand == nil {
		return nil, ProjectionRefreshReport{}, fmt.Errorf("projection refresh scenario dependencies are required")
	}
	definitions, err := selectProjectionRefreshDefinitions(selected)
	if err != nil {
		return nil, ProjectionRefreshReport{}, err
	}
	if err := requireProjectionRefreshFixture(definitions, spec.Fixture); err != nil {
		return nil, ProjectionRefreshReport{}, err
	}
	if err := os.MkdirAll(fixtureRoot, 0o755); err != nil {
		return nil, ProjectionRefreshReport{}, fmt.Errorf("create projection refresh fixture root: %w", err)
	}

	report := ProjectionRefreshReport{
		Format:  ProjectionRefreshFormat,
		Version: ProjectionRefreshVersion,
		Samples: spec.Samples,
		Fixture: spec.Fixture,
		Points:  make([]ProjectionRefreshPoint, 0, len(definitions)),
	}
	results := make([]ScenarioResult, 0, len(definitions))
	for _, definition := range definitions {
		result, point, err := measureProjectionRefreshPoint(ctx, spec, fixtureRoot, definition, dependencies)
		if err != nil {
			return nil, ProjectionRefreshReport{}, err
		}
		results = append(results, result)
		report.Points = append(report.Points, point)
	}
	report.Slope = projectionRefreshSlope(report.Points, spec.Samples)
	return results, report, nil
}

func measureProjectionRefreshPoint(
	ctx context.Context,
	spec RunSpec,
	fixtureRoot string,
	definition projectionRefreshDefinition,
	dependencies projectionRefreshDependencies,
) (ScenarioResult, ProjectionRefreshPoint, error) {
	result := ScenarioResult{
		Name:    definition.name,
		Surface: projectionRefreshSurface,
		Samples: make([]Sample, spec.Samples),
	}
	point := ProjectionRefreshPoint{
		Scenario:         definition.name,
		ChangedTaskHeads: definition.changedHeads,
		Samples:          spec.Samples,
	}
	enumerationMilliseconds := make([]float64, 0, spec.Samples)
	for sample := range spec.Samples {
		root := filepath.Join(fixtureRoot, definition.name, fmt.Sprintf("sample-%03d", sample+1))
		fixture, err := dependencies.buildFixture(ctx, root, spec.Fixture)
		if err != nil {
			return ScenarioResult{}, ProjectionRefreshPoint{}, fmt.Errorf("build %s sample %d fixture: %w", definition.name, sample+1, err)
		}
		measured, samplePoint, err := measureProjectionRefreshSample(ctx, spec, definition, fixture, sample, dependencies)
		cleanupErr := os.RemoveAll(root)
		if err != nil {
			primaryErr := fmt.Errorf("%s sample %d: %w", definition.name, sample+1, err)
			return ScenarioResult{}, ProjectionRefreshPoint{}, withFixtureCleanupError(primaryErr, cleanupErr, definition.name)
		}
		if cleanupErr != nil {
			return ScenarioResult{}, ProjectionRefreshPoint{}, withFixtureCleanupError(nil, cleanupErr, definition.name)
		}
		result.Samples[sample] = measured
		point.TaskRefs = samplePoint.taskRefs
		point.ProjectedTaskRows = samplePoint.projectedTaskRows
		point.ProjectionCacheBytes = samplePoint.projectionCacheBytes
		enumerationMilliseconds = append(enumerationMilliseconds, samplePoint.refEnumerationMilliseconds)
	}
	result.Summary = Summarize(result.Samples)
	point.RefreshMedianMilliseconds = result.Summary.MedianMilliseconds
	point.RefreshP95Milliseconds = result.Summary.P95Milliseconds
	point.RefreshMedianGitProcesses = medianGitProcesses(result.Samples)
	sort.Float64s(enumerationMilliseconds)
	if len(enumerationMilliseconds) != 0 {
		point.RefEnumerationMedianMilliseconds = median(enumerationMilliseconds)
	}
	return result, point, nil
}

type projectionRefreshSampleMetrics struct {
	taskRefs                   int
	projectedTaskRows          int
	projectionCacheBytes       int64
	refEnumerationMilliseconds float64
}

func measureProjectionRefreshSample(
	ctx context.Context,
	spec RunSpec,
	definition projectionRefreshDefinition,
	fixture Fixture,
	sample int,
	dependencies projectionRefreshDependencies,
) (Sample, projectionRefreshSampleMetrics, error) {
	refresh := CommandSpec{
		Binary:    spec.WorkbookBinary,
		Args:      []string{"list", "--json"},
		Directory: fixture.Root,
		Timeout:   spec.CommandTimeout,
	}
	if err := requireSuccessfulSetup(definition.name+" settle", dependencies.runSetup(ctx, refresh)); err != nil {
		return Sample{}, projectionRefreshSampleMetrics{}, err
	}

	before, _, err := enumerateTaskRefs(ctx, spec.CommandTimeout, fixture.Root)
	if err != nil {
		return Sample{}, projectionRefreshSampleMetrics{}, err
	}
	if len(fixture.ActiveTaskIDs) < definition.changedHeads {
		return Sample{}, projectionRefreshSampleMetrics{}, fmt.Errorf(
			"fixture has %d active tasks, need %d mutable active task heads",
			len(fixture.ActiveTaskIDs), definition.changedHeads,
		)
	}
	intended := append([]string(nil), fixture.ActiveTaskIDs[:definition.changedHeads]...)
	if err := dependencies.mutateHeads(ctx, fixture.Root, fixture.Config, intended, sample); err != nil {
		return Sample{}, projectionRefreshSampleMetrics{}, fmt.Errorf("mutate task heads: %w", err)
	}

	after, enumeration, err := enumerateTaskRefs(ctx, spec.CommandTimeout, fixture.Root)
	if err != nil {
		return Sample{}, projectionRefreshSampleMetrics{}, err
	}
	changed, err := changedTaskHeads(before, after)
	if err != nil {
		return Sample{}, projectionRefreshSampleMetrics{}, err
	}
	if len(changed) != definition.changedHeads {
		return Sample{}, projectionRefreshSampleMetrics{}, fmt.Errorf(
			"setup changed %d task heads, want exactly %d",
			len(changed), definition.changedHeads,
		)
	}
	for _, taskID := range intended {
		if _, mutated := changed[taskID]; !mutated {
			return Sample{}, projectionRefreshSampleMetrics{}, fmt.Errorf("setup did not change intended task head %q", taskID)
		}
	}

	measurement := dependencies.measureCommand(ctx, refresh)
	metrics := projectionRefreshSampleMetrics{
		taskRefs:                   countTaskRefs(after),
		refEnumerationMilliseconds: durationAsMilliseconds(enumeration),
	}
	// A timed-out or failed product command is retained evidence, exactly like
	// the other repository scenarios. Only an untrustworthy successful result
	// is fatal.
	if !sampleSucceeded(measurement.Sample) {
		return measurement.Sample, metrics, nil
	}
	rows, err := projectedTaskRows(measurement, spec.Fixture.ActiveTasks)
	if err != nil {
		return Sample{}, projectionRefreshSampleMetrics{}, err
	}
	cacheBytes, err := projectionCacheBytes(ctx, spec.CommandTimeout, fixture.Root)
	if err != nil {
		return Sample{}, projectionRefreshSampleMetrics{}, err
	}
	metrics.projectedTaskRows = rows
	metrics.projectionCacheBytes = cacheBytes
	return measurement.Sample, metrics, nil
}

func selectProjectionRefreshDefinitions(selected []string) ([]projectionRefreshDefinition, error) {
	requested := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		if _, duplicate := requested[name]; duplicate {
			return nil, fmt.Errorf("duplicate projection refresh scenario %q", name)
		}
		requested[name] = struct{}{}
	}
	definitions := make([]projectionRefreshDefinition, 0, len(selected))
	for _, definition := range projectionRefreshDefinitions {
		if _, wanted := requested[definition.name]; wanted {
			definitions = append(definitions, definition)
			delete(requested, definition.name)
		}
	}
	if len(requested) != 0 {
		for _, name := range selected {
			if _, unknown := requested[name]; unknown {
				return nil, fmt.Errorf("unknown projection refresh scenario %q", name)
			}
		}
	}
	return definitions, nil
}

// requireProjectionRefreshFixture rejects a fixture that cannot supply the
// exact requested changed-head cardinality. Only active tasks are mutable,
// because a tombstoned task's history has already ended.
func requireProjectionRefreshFixture(definitions []projectionRefreshDefinition, fixture FixtureSpec) error {
	for _, definition := range definitions {
		if definition.changedHeads <= fixture.ActiveTasks {
			continue
		}
		return fmt.Errorf(
			"%s requires %d mutable active task heads, but the fixture has %d active tasks; "+
				"re-run with a larger fixture, for example --tasks %d --tombstones %d",
			definition.name,
			definition.changedHeads,
			fixture.ActiveTasks,
			definition.changedHeads+fixture.TombstonedTasks,
			fixture.TombstonedTasks,
		)
	}
	return nil
}

// mutateProjectionRefreshHeads advances exactly the supplied canonical task
// refs with one deterministic, valid appended operation each. It writes Git
// objects directly so the disposable projection is left stale for precisely
// those tasks and no measured product command runs during setup.
func mutateProjectionRefreshHeads(
	ctx context.Context,
	root string,
	config core.ProjectConfig,
	taskIDs []string,
	round int,
) error {
	ids := newFixtureIDs()
	ids.nextAt = projectionRefreshMutationOrigin.Add(time.Duration(round+1) * time.Hour)
	for index, taskID := range taskIDs {
		ref := taskRefName(taskID)
		head, err := fixtureRefObjectID(ctx, root, ref)
		if err != nil {
			return err
		}
		parent, err := readFixtureCommit(ctx, root, head)
		if err != nil {
			return err
		}
		commit, err := appendFixtureOperation(
			ctx, root, config, parent, taskID, parent.Pack.HistoryGeneration,
			index, int(parent.Pack.LogicalClock)+1, ids,
		)
		if err != nil {
			return err
		}
		if err := updateFixtureRef(ctx, root, ref, commit.Head, head); err != nil {
			return err
		}
	}
	return nil
}

func changedTaskHeads(before, after []byte) (map[string]string, error) {
	previous, err := parseTaskRefHeads(before)
	if err != nil {
		return nil, err
	}
	current, err := parseTaskRefHeads(after)
	if err != nil {
		return nil, err
	}
	if len(previous) != len(current) {
		return nil, fmt.Errorf("task ref population changed from %d to %d during setup", len(previous), len(current))
	}
	changed := make(map[string]string)
	for taskID, head := range current {
		existing, known := previous[taskID]
		if !known {
			return nil, fmt.Errorf("task ref %q appeared during setup", taskID)
		}
		if existing != head {
			changed[taskID] = head
		}
	}
	return changed, nil
}

func parseTaskRefHeads(refs []byte) (map[string]string, error) {
	heads := make(map[string]string)
	for _, line := range bytes.Split(refs, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		ref, objectName, found := bytes.Cut(line, []byte{0})
		if !found {
			return nil, fmt.Errorf("task ref enumeration line %q has no object name", line)
		}
		taskID := strings.TrimPrefix(string(ref), "refs/workbook/tasks/")
		if taskID == "" || taskID == string(ref) {
			return nil, fmt.Errorf("task ref enumeration line %q is not a task ref", line)
		}
		heads[taskID] = string(objectName)
	}
	return heads, nil
}

func countTaskRefs(refs []byte) int {
	heads, err := parseTaskRefHeads(refs)
	if err != nil {
		return 0
	}
	return len(heads)
}

func projectedTaskRows(measurement CommandMeasurement, activeTasks int) (int, error) {
	var envelope remoteResultEnvelope
	if err := json.Unmarshal(measurement.Stdout, &envelope); err != nil {
		return 0, fmt.Errorf("decode refresh result: %w", err)
	}
	if envelope.Format != workbookResultFormat || envelope.Version != workbookJSONVersion || envelope.Command != "list" {
		return 0, fmt.Errorf(
			"refresh result = %q v%d command %q, want %s v%d command list",
			envelope.Format, envelope.Version, envelope.Command, workbookResultFormat, workbookJSONVersion,
		)
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(envelope.Data, &rows); err != nil {
		return 0, fmt.Errorf("decode refresh task rows: %w", err)
	}
	if len(rows) != activeTasks {
		return 0, fmt.Errorf("refreshed projection returned %d task rows, want %d", len(rows), activeTasks)
	}
	return len(rows), nil
}

func projectionCacheBytes(ctx context.Context, timeout time.Duration, root string) (int64, error) {
	output, _, err := runRepositoryGit(ctx, timeout, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return 0, err
	}
	commonDir := strings.TrimSpace(string(output))
	if commonDir == "" {
		return 0, fmt.Errorf("Git returned an empty common directory")
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}
	info, err := os.Stat(filepath.Join(commonDir, "workbook", projectionRefreshCacheFile))
	if err != nil {
		return 0, fmt.Errorf("stat projection cache: %w", err)
	}
	return info.Size(), nil
}

func medianGitProcesses(samples []Sample) int {
	if len(samples) == 0 {
		return 0
	}
	counts := make([]int, len(samples))
	for index, sample := range samples {
		counts[index] = sample.GitProcesses
	}
	sort.Ints(counts)
	middle := len(counts) / 2
	if len(counts)%2 == 1 {
		return counts[middle]
	}
	return (counts[middle-1] + counts[middle]) / 2
}

func projectionRefreshSlope(points []ProjectionRefreshPoint, samples int) ProjectionRefreshSlope {
	if len(points) == 0 {
		return ProjectionRefreshSlope{Description: "No projection refresh change-count point was measured."}
	}
	baseline, maximum := points[0], points[0]
	for _, point := range points {
		if point.ChangedTaskHeads < baseline.ChangedTaskHeads {
			baseline = point
		}
		if point.ChangedTaskHeads > maximum.ChangedTaskHeads {
			maximum = point
		}
	}
	slope := ProjectionRefreshSlope{
		BaselineMilliseconds:   baseline.RefreshMedianMilliseconds,
		MaxChangedTaskHeads:    maximum.ChangedTaskHeads,
		MaxChangedMilliseconds: maximum.RefreshMedianMilliseconds,
	}
	span := maximum.ChangedTaskHeads - baseline.ChangedTaskHeads
	if span > 0 {
		slope.MillisecondsPerChangedHead =
			(maximum.RefreshMedianMilliseconds - baseline.RefreshMedianMilliseconds) / float64(span)
	}
	measured := make([]string, len(points))
	for index, point := range points {
		measured[index] = fmt.Sprintf("%d changed task heads at %.2f ms", point.ChangedTaskHeads, point.RefreshMedianMilliseconds)
	}
	slope.Description = fmt.Sprintf(
		"Median refresh latency was %.2f ms at %d changed task heads and %.2f ms at %d changed task heads "+
			"across %d sample(s) per point, an average of %.4f ms per additional changed task head. "+
			"Measured points: %s. These values describe the measured samples; this family has no pass threshold.",
		baseline.RefreshMedianMilliseconds, baseline.ChangedTaskHeads,
		maximum.RefreshMedianMilliseconds, maximum.ChangedTaskHeads,
		samples, slope.MillisecondsPerChangedHead, strings.Join(measured, "; "),
	)
	return slope
}

func (r ProjectionRefreshReport) writeMarkdown(w io.Writer) error {
	if _, err := fmt.Fprintln(w, "\n## Projection refresh change-count family"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(
		w,
		"Fixture: %d total tasks (%d active, %d tombstoned), %d operations per task, %s object format; %d sample(s) per point.\n\n",
		r.Fixture.TotalTasks, r.Fixture.ActiveTasks, r.Fixture.TombstonedTasks,
		r.Fixture.OperationsPerTask, r.Fixture.ObjectFormat, r.Samples,
	); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| Scenario | Changed task heads | Samples | Task refs | Ref enumeration median (ms) | Refresh median (ms) | Refresh p95 (ms) | Refresh median Git processes | Projected task rows | Projection cache (bytes) |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |"); err != nil {
		return err
	}
	for _, point := range r.Points {
		if _, err := fmt.Fprintf(
			w, "| %s | %d | %d | %d | %.2f | %.2f | %.2f | %d | %d | %d |\n",
			point.Scenario, point.ChangedTaskHeads, point.Samples, point.TaskRefs,
			point.RefEnumerationMedianMilliseconds, point.RefreshMedianMilliseconds,
			point.RefreshP95Milliseconds, point.RefreshMedianGitProcesses,
			point.ProjectedTaskRows, point.ProjectionCacheBytes,
		); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "\nSlope: %s\n", r.Slope.Description)
	return err
}
