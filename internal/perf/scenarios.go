package perf

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	coldCLITasksPerFixture  = 10
	warmHTTPTasksPerFixture = 10
	warmServerPrefix        = "Workbook board: http://"
)

// RunSpec configures one benchmark scenario run.
type RunSpec struct {
	WorkbookBinary string
	Fixture        FixtureSpec
	Samples        int
	CommandTimeout time.Duration
}

type scenarioDependencies struct {
	buildFixture      func(context.Context, string, FixtureSpec) (Fixture, error)
	prepareProjection func(context.Context, CommandSpec, int) error
	measureCommand    func(context.Context, CommandSpec) Sample
	cleanupFixture    func(string) error
}

type warmHTTPTasks struct {
	update      string
	sameBurst   string
	independent []string
}

type warmScenarioServer interface {
	prepareProjection(context.Context, int, time.Duration) error
	measureStatus(context.Context, string, string, time.Duration) (Sample, error)
	measureIndependentBurst(context.Context, []string, string, time.Duration) (Sample, error)
	measureSameTaskBurst(context.Context, string, int, time.Duration) (Sample, error)
	close(time.Duration) error
}

type warmHTTPDependencies struct {
	buildFixture   func(context.Context, string, FixtureSpec) (Fixture, error)
	startServer    func(context.Context, string, string, time.Duration) (warmScenarioServer, error)
	cleanupFixture func(string) error
}

type warmHTTPServer struct {
	baseURL   string
	tracePath string
	command   *exec.Cmd
	wait      <-chan error
	client    *http.Client
}

type countObjectsMetrics struct {
	count    int64
	size     int64
	inPack   int64
	sizePack int64
}

// RunColdCLI builds deterministic fixtures and measures cold CLI mutations
// against an acceptance-sized baseline isolated by scenario and sample.
func RunColdCLI(ctx context.Context, spec RunSpec, fixtureRoot string, selected []string) ([]ScenarioResult, error) {
	return runColdCLI(ctx, spec, fixtureRoot, selected, scenarioDependencies{
		buildFixture: func(ctx context.Context, root string, fixture FixtureSpec) (Fixture, error) {
			return buildFixtureWithinTimeout(ctx, root, fixture, spec.CommandTimeout)
		},
		prepareProjection: prepareProjection,
		measureCommand:    MeasureCommand,
		cleanupFixture:    os.RemoveAll,
	})
}

func buildFixtureWithinTimeout(ctx context.Context, root string, spec FixtureSpec, timeout time.Duration) (Fixture, error) {
	fixtureContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return BuildFixture(fixtureContext, root, spec)
}

func runColdCLI(ctx context.Context, spec RunSpec, fixtureRoot string, selected []string, dependencies scenarioDependencies) ([]ScenarioResult, error) {
	if spec.Samples < 1 {
		return nil, fmt.Errorf("samples must be positive")
	}
	if spec.CommandTimeout <= 0 {
		return nil, fmt.Errorf("command timeout must be positive")
	}
	if fixtureRoot == "" {
		return nil, fmt.Errorf("fixture root is required")
	}
	if dependencies.buildFixture == nil || dependencies.prepareProjection == nil || dependencies.measureCommand == nil {
		return nil, fmt.Errorf("cold CLI scenario dependencies are required")
	}
	cleanupFixture := dependencies.cleanupFixture
	if cleanupFixture == nil {
		cleanupFixture = os.RemoveAll
	}
	if err := os.MkdirAll(fixtureRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create fixture root: %w", err)
	}

	definitions, err := selectedColdCLIScenarios(selected)
	if err != nil {
		return nil, err
	}
	results := make([]ScenarioResult, len(definitions))
	for index, definition := range definitions {
		results[index] = coldCLIResult(definition.name, spec.Samples)
	}
	for sample := range spec.Samples {
		sampleRoot := filepath.Join(fixtureRoot, fmt.Sprintf("sample-%03d", sample+1))
		for index, definition := range definitions {
			root := filepath.Join(sampleRoot, definition.name)
			fixture, err := dependencies.buildFixture(ctx, root, spec.Fixture)
			if err != nil {
				primaryErr := fmt.Errorf("build %s fixture: %w", definition.name, err)
				return nil, withFixtureCleanupError(primaryErr, cleanupFixture(root), definition.name)
			}
			if err := dependencies.prepareProjection(ctx, CommandSpec{
				Binary: spec.WorkbookBinary, Args: []string{"rebuild", "--json"}, Directory: fixture.Root, Timeout: spec.CommandTimeout,
			}, spec.Fixture.TotalTasks); err != nil {
				primaryErr := fmt.Errorf("prepare %s projection: %w", definition.name, err)
				return nil, withFixtureCleanupError(primaryErr, cleanupFixture(root), definition.name)
			}
			measured, err := definition.measure(ctx, dependencies, spec, fixture, sample)
			cleanupErr := cleanupFixture(root)
			if err != nil {
				primaryErr := fmt.Errorf("measure %s: %w", definition.name, err)
				return nil, withFixtureCleanupError(primaryErr, cleanupErr, definition.name)
			}
			if cleanupErr != nil {
				return nil, withFixtureCleanupError(nil, cleanupErr, definition.name)
			}
			results[index].Samples[sample] = measured
		}
	}
	for index := range results {
		results[index].Summary = Summarize(results[index].Samples)
	}
	return results, nil
}

type coldScenarioDefinition struct {
	name    string
	measure func(context.Context, scenarioDependencies, RunSpec, Fixture, int) (Sample, error)
}

var coldScenarioDefinitions = []coldScenarioDefinition{
	{name: "cli-create", measure: measureColdCreate},
	{name: "cli-delete", measure: measureColdDelete},
	{name: "cli-depend", measure: measureColdDepend},
	{name: "cli-free", measure: measureColdFree},
	{name: "cli-move", measure: measureColdMove},
	{name: "cli-restore", measure: measureColdRestore},
	{name: "cli-update", measure: measureColdUpdate},
	{name: "cli-burst-independent-10", measure: measureColdIndependentBurst},
	{name: "cli-burst-same-task-10", measure: measureColdSameTaskBurst},
}

func selectedColdCLIScenarios(selected []string) ([]coldScenarioDefinition, error) {
	wanted := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		wanted[name] = struct{}{}
	}
	definitions := make([]coldScenarioDefinition, 0, len(selected))
	for _, definition := range coldScenarioDefinitions {
		if _, ok := wanted[definition.name]; ok {
			definitions = append(definitions, definition)
			delete(wanted, definition.name)
		}
	}
	for name := range wanted {
		return nil, fmt.Errorf("unknown cold CLI scenario %q", name)
	}
	return definitions, nil
}

func measureColdCreate(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, sample int) (Sample, error) {
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{
		"create", fmt.Sprintf("Benchmark created task %d", sample+1), "--status", "ready", "--priority", "high", "--json",
	}), nil
}

func measureColdDelete(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	taskID, err := fixtureActiveTask(fixture, 1)
	if err != nil {
		return Sample{}, err
	}
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{"delete", taskID[0], "--json"}), nil
}

func measureColdDepend(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	taskIDs, err := fixtureActiveTask(fixture, 3)
	if err != nil {
		return Sample{}, err
	}
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{"depend", taskIDs[2], taskIDs[0], "--json"}), nil
}

func measureColdFree(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	if len(fixture.Dependencies) == 0 {
		return Sample{}, fmt.Errorf("fixture has no direct dependency")
	}
	pair := fixture.Dependencies[0]
	if !containsTaskID(fixture.ActiveTaskIDs, pair.Dependent) || !containsTaskID(fixture.ActiveTaskIDs, pair.Dependency) {
		return Sample{}, fmt.Errorf("fixture direct dependency must have active tasks")
	}
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{"free", pair.Dependent, pair.Dependency, "--json"}), nil
}

func measureColdMove(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	taskIDs, err := fixtureActiveTask(fixture, 2)
	if err != nil {
		return Sample{}, err
	}
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{"move", taskIDs[1], "--before", taskIDs[0], "--json"}), nil
}

func measureColdRestore(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	if len(fixture.TombstonedTaskIDs) == 0 {
		return Sample{}, fmt.Errorf("fixture has no tombstoned tasks")
	}
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{"restore", fixture.TombstonedTaskIDs[0], "--json"}), nil
}

func measureColdUpdate(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	taskID, err := fixtureActiveTask(fixture, 1)
	if err != nil {
		return Sample{}, err
	}
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{"update", taskID[0], "--status", "ready", "--json"}), nil
}

func measureColdIndependentBurst(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	taskIDs, err := fixtureActiveTask(fixture, coldCLITasksPerFixture)
	if err != nil {
		return Sample{}, err
	}
	return measureIndependentBurst(ctx, dependencies, spec, fixture.Root, taskIDs), nil
}

func measureColdSameTaskBurst(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	taskID, err := fixtureActiveTask(fixture, 1)
	if err != nil {
		return Sample{}, err
	}
	return measureSameTaskBurst(ctx, dependencies, spec, fixture.Root, taskID[0]), nil
}

func fixtureActiveTask(fixture Fixture, count int) ([]string, error) {
	if len(fixture.ActiveTaskIDs) < count {
		return nil, fmt.Errorf("fixture has %d active tasks, need %d", len(fixture.ActiveTaskIDs), count)
	}
	return fixture.ActiveTaskIDs[:count], nil
}

func containsTaskID(taskIDs []string, taskID string) bool {
	for _, candidate := range taskIDs {
		if candidate == taskID {
			return true
		}
	}
	return false
}

// RunWarmHTTP measures status mutations against independently warmed Workbook
// servers so each scenario sample starts from an exact-size fixture.
func RunWarmHTTP(ctx context.Context, spec RunSpec, fixtureRoot string, selected []string) ([]ScenarioResult, error) {
	return runWarmHTTP(ctx, spec, fixtureRoot, selected, warmHTTPDependencies{
		buildFixture: func(ctx context.Context, root string, fixture FixtureSpec) (Fixture, error) {
			return buildFixtureWithinTimeout(ctx, root, fixture, spec.CommandTimeout)
		},
		startServer: func(ctx context.Context, binary, root string, timeout time.Duration) (warmScenarioServer, error) {
			server, err := startWarmHTTPServer(ctx, binary, root, timeout)
			return server, err
		},
		cleanupFixture: os.RemoveAll,
	})
}

func runWarmHTTP(ctx context.Context, spec RunSpec, fixtureRoot string, selected []string, dependencies warmHTTPDependencies) ([]ScenarioResult, error) {
	if spec.Samples < 1 {
		return nil, fmt.Errorf("samples must be positive")
	}
	if spec.CommandTimeout <= 0 {
		return nil, fmt.Errorf("command timeout must be positive")
	}
	if fixtureRoot == "" {
		return nil, fmt.Errorf("fixture root is required")
	}
	if dependencies.buildFixture == nil || dependencies.startServer == nil {
		return nil, fmt.Errorf("warm HTTP scenario dependencies are required")
	}
	cleanupFixture := dependencies.cleanupFixture
	if cleanupFixture == nil {
		cleanupFixture = os.RemoveAll
	}
	if err := os.MkdirAll(fixtureRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create warm fixture root: %w", err)
	}

	definitions, err := selectedWarmHTTPScenarios(selected)
	if err != nil {
		return nil, err
	}
	results := make([]ScenarioResult, len(definitions))
	for index, definition := range definitions {
		results[index] = warmHTTPResult(definition.name, spec.Samples)
	}
	for sample := range spec.Samples {
		sampleRoot := filepath.Join(fixtureRoot, fmt.Sprintf("sample-%03d", sample+1))
		for index, definition := range definitions {
			name := definition.name
			root := filepath.Join(sampleRoot, name)
			fixture, err := dependencies.buildFixture(ctx, root, spec.Fixture)
			if err != nil {
				primaryErr := fmt.Errorf("build warm %s sample %d fixture: %w", name, sample+1, err)
				return nil, withFixtureCleanupError(primaryErr, cleanupFixture(root), name)
			}
			server, err := dependencies.startServer(ctx, spec.WorkbookBinary, fixture.Root, spec.CommandTimeout)
			if err != nil {
				primaryErr := fmt.Errorf("start warm %s sample %d server: %w", name, sample+1, err)
				return nil, withFixtureCleanupError(primaryErr, cleanupFixture(root), name)
			}
			if err := server.prepareProjection(ctx, spec.Fixture.ActiveTasks, spec.CommandTimeout); err != nil {
				closeErr := server.close(spec.CommandTimeout)
				cleanupErr := cleanupFixture(root)
				primaryErr := fmt.Errorf("prepare %s sample %d: %w", name, sample+1, err)
				if closeErr != nil {
					primaryErr = fmt.Errorf("%w (close warm server: %v)", primaryErr, closeErr)
				}
				return nil, withFixtureCleanupError(primaryErr, cleanupErr, name)
			}
			measured, measureErr := definition.measure(ctx, server, fixture, spec.CommandTimeout)
			closeErr := server.close(spec.CommandTimeout)
			cleanupErr := cleanupFixture(root)
			if measureErr != nil {
				primaryErr := fmt.Errorf("measure %s sample %d: %w", name, sample+1, measureErr)
				if closeErr != nil {
					primaryErr = fmt.Errorf("%w (close warm server: %v)", primaryErr, closeErr)
				}
				return nil, withFixtureCleanupError(primaryErr, cleanupErr, name)
			}
			if closeErr != nil {
				primaryErr := fmt.Errorf("close warm %s sample %d server: %w", name, sample+1, closeErr)
				return nil, withFixtureCleanupError(primaryErr, cleanupErr, name)
			}
			if cleanupErr != nil {
				return nil, withFixtureCleanupError(nil, cleanupErr, name)
			}
			results[index].Samples[sample] = measured
		}
	}
	for index := range results {
		results[index].Summary = Summarize(results[index].Samples)
	}
	return results, nil
}

func withFixtureCleanupError(primaryErr, cleanupErr error, scenario string) error {
	if cleanupErr == nil {
		return primaryErr
	}
	if primaryErr == nil {
		return fmt.Errorf("cleanup %s fixture: %w", scenario, cleanupErr)
	}
	return fmt.Errorf("%w (cleanup %s fixture: %v)", primaryErr, scenario, cleanupErr)
}

type warmHTTPScenarioDefinition struct {
	name    string
	measure func(context.Context, warmScenarioServer, Fixture, time.Duration) (Sample, error)
}

var warmHTTPScenarioDefinitions = []warmHTTPScenarioDefinition{
	{name: "api-update", measure: measureWarmUpdate},
	{name: "api-burst-independent-10", measure: measureWarmIndependentBurst},
	{name: "api-burst-same-task-10", measure: measureWarmSameTaskBurst},
}

func selectedWarmHTTPScenarios(selected []string) ([]warmHTTPScenarioDefinition, error) {
	wanted := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		wanted[name] = struct{}{}
	}
	definitions := make([]warmHTTPScenarioDefinition, 0, len(selected))
	for _, definition := range warmHTTPScenarioDefinitions {
		if _, ok := wanted[definition.name]; ok {
			definitions = append(definitions, definition)
			delete(wanted, definition.name)
		}
	}
	for name := range wanted {
		return nil, fmt.Errorf("unknown warm HTTP scenario %q", name)
	}
	return definitions, nil
}

func measureWarmUpdate(ctx context.Context, server warmScenarioServer, fixture Fixture, timeout time.Duration) (Sample, error) {
	taskIDs, err := fixtureActiveTask(fixture, 1)
	if err != nil {
		return Sample{}, err
	}
	return server.measureStatus(ctx, taskIDs[0], "ready", timeout)
}

func measureWarmIndependentBurst(ctx context.Context, server warmScenarioServer, fixture Fixture, timeout time.Duration) (Sample, error) {
	taskIDs, err := fixtureActiveTask(fixture, warmHTTPTasksPerFixture)
	if err != nil {
		return Sample{}, err
	}
	return server.measureIndependentBurst(ctx, taskIDs, "ready", timeout)
}

func measureWarmSameTaskBurst(ctx context.Context, server warmScenarioServer, fixture Fixture, timeout time.Duration) (Sample, error) {
	taskIDs, err := fixtureActiveTask(fixture, 2)
	if err != nil {
		return Sample{}, err
	}
	return server.measureSameTaskBurst(ctx, taskIDs[1], 0, timeout)
}

// MeasureRepository records projection, ref, object, and local-bare-remote
// measurements against an existing Workbook fixture. Every repository scenario
// records the requested number of samples.
func MeasureRepository(ctx context.Context, workbookBinary, fixtureRoot string, samples int, commandTimeout time.Duration) (RepositoryMetrics, []ScenarioResult, error) {
	return measureRepository(ctx, workbookBinary, fixtureRoot, samples, commandTimeout, true)
}

// MeasurePackedRepositorySync records ref, object, and local-bare-remote
// measurements without running projection scenarios that mutate the fixture.
func MeasurePackedRepositorySync(ctx context.Context, workbookBinary, fixtureRoot string, samples int, commandTimeout time.Duration) (RepositoryMetrics, []ScenarioResult, error) {
	return measureRepository(ctx, workbookBinary, fixtureRoot, samples, commandTimeout, false)
}

func measureRepository(ctx context.Context, workbookBinary, fixtureRoot string, samples int, commandTimeout time.Duration, includeProjection bool) (RepositoryMetrics, []ScenarioResult, error) {
	if workbookBinary == "" {
		return RepositoryMetrics{}, nil, fmt.Errorf("workbook binary is required")
	}
	if fixtureRoot == "" {
		return RepositoryMetrics{}, nil, fmt.Errorf("fixture root is required")
	}
	if samples < 1 {
		return RepositoryMetrics{}, nil, fmt.Errorf("samples must be positive")
	}
	if commandTimeout <= 0 {
		return RepositoryMetrics{}, nil, fmt.Errorf("command timeout must be positive")
	}

	var results []ScenarioResult
	if includeProjection {
		var err error
		results, err = measureProjectionScenarios(
			ctx,
			workbookBinary,
			fixtureRoot,
			samples,
			commandTimeout,
			MeasureCommand,
		)
		if err != nil {
			return RepositoryMetrics{}, nil, err
		}
	}

	looseRefs, looseRefDuration, err := enumerateTaskRefs(ctx, commandTimeout, fixtureRoot)
	if err != nil {
		return RepositoryMetrics{}, nil, err
	}
	beforeObjectsOutput, _, err := runRepositoryGit(ctx, commandTimeout, fixtureRoot, "count-objects", "-v")
	if err != nil {
		return RepositoryMetrics{}, nil, err
	}

	if _, _, err := runRepositoryGit(ctx, commandTimeout, fixtureRoot, "pack-refs", "--all"); err != nil {
		return RepositoryMetrics{}, nil, err
	}
	packedRefs, packedRefDuration, err := enumerateTaskRefs(ctx, commandTimeout, fixtureRoot)
	if err != nil {
		return RepositoryMetrics{}, nil, err
	}
	if !bytes.Equal(looseRefs, packedRefs) {
		return RepositoryMetrics{}, nil, fmt.Errorf("task ref enumeration changed after packing refs")
	}

	if _, _, err := runRepositoryGit(ctx, commandTimeout, fixtureRoot, "gc"); err != nil {
		return RepositoryMetrics{}, nil, err
	}
	afterObjectsOutput, _, err := runRepositoryGit(ctx, commandTimeout, fixtureRoot, "count-objects", "-v")
	if err != nil {
		return RepositoryMetrics{}, nil, err
	}
	metrics, err := repositoryMetricsFromCounts(
		looseRefDuration,
		packedRefDuration,
		beforeObjectsOutput,
		afterObjectsOutput,
	)
	if err != nil {
		return RepositoryMetrics{}, nil, err
	}

	originRoot, err := os.MkdirTemp("", "workbook-benchmark-origin-")
	if err != nil {
		return RepositoryMetrics{}, nil, fmt.Errorf("create local bare remote root: %w", err)
	}
	defer os.RemoveAll(originRoot)
	syncResults, err := measureLocalBareSyncAgainstNewOrigin(
		ctx,
		workbookBinary,
		fixtureRoot,
		originRoot,
		samples,
		commandTimeout,
		MeasureCommand,
	)
	if err != nil {
		return RepositoryMetrics{}, nil, err
	}
	results = append(results, syncResults...)

	for index := range results {
		results[index].Summary = Summarize(results[index].Samples)
	}
	return metrics, results, nil
}

func measureProjectionScenarios(
	ctx context.Context,
	workbookBinary string,
	fixtureRoot string,
	samples int,
	commandTimeout time.Duration,
	measureCommand func(context.Context, CommandSpec) Sample,
) ([]ScenarioResult, error) {
	results := repositoryResults(samples)[:1]
	for sample := range samples {
		results[0].Samples[sample] = measureCommand(ctx, CommandSpec{
			Binary:    workbookBinary,
			Args:      []string{"rebuild", "--json"},
			Directory: fixtureRoot,
			Timeout:   commandTimeout,
		})
	}
	for index := range results {
		results[index].Summary = Summarize(results[index].Samples)
	}
	return results, nil
}

// measureLocalBareSyncAgainstNewOrigin gives every sample its own empty bare
// origin and clears the fetched tracking namespace, so each sample measures the
// same initial-publication and already-synchronized topology. That setup is
// plain Git work outside both measured samples.
func measureLocalBareSyncAgainstNewOrigin(
	ctx context.Context,
	workbookBinary string,
	fixtureRoot string,
	originRoot string,
	samples int,
	commandTimeout time.Duration,
	measureCommand func(context.Context, CommandSpec) Sample,
) ([]ScenarioResult, error) {
	output, _, err := runRepositoryGit(
		ctx, commandTimeout, fixtureRoot, "rev-parse", "--show-object-format",
	)
	if err != nil {
		return nil, err
	}
	objectFormat := strings.TrimSuffix(string(output), "\n")
	if objectFormat == "" || strings.ContainsAny(objectFormat, "\r\n\t ") {
		return nil, fmt.Errorf("Git returned invalid repository object format %q", objectFormat)
	}
	prepareSample := func(ctx context.Context, sample int) error {
		origin := filepath.Join(originRoot, fmt.Sprintf("origin-%03d.git", sample+1))
		if _, _, err := runRepositoryGit(
			ctx, commandTimeout, "", "init", "--bare", "--quiet",
			"--object-format="+objectFormat, origin,
		); err != nil {
			return err
		}
		remoteCommand := "add"
		if sample > 0 {
			remoteCommand = "set-url"
		}
		if _, _, err := runRepositoryGit(
			ctx, commandTimeout, fixtureRoot, "remote", remoteCommand, "origin", origin,
		); err != nil {
			return err
		}
		return deleteTrackingTaskRefs(ctx, commandTimeout, fixtureRoot)
	}
	return measureLocalBareSync(
		ctx, workbookBinary, fixtureRoot, samples, commandTimeout, measureCommand, prepareSample,
	)
}

// deleteTrackingTaskRefs removes every fetched tracking ref so the next sample
// starts from an unpublished repository.
func deleteTrackingTaskRefs(ctx context.Context, commandTimeout time.Duration, fixtureRoot string) error {
	output, _, err := runRepositoryGit(
		ctx, commandTimeout, fixtureRoot, "for-each-ref", "--format=delete %(refname)", "refs/workbook/remotes/",
	)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(output)) == 0 {
		return nil
	}
	if _, err := runFixtureGitOutput(ctx, fixtureRoot, output, "update-ref", "--stdin"); err != nil {
		return fmt.Errorf("clear fetched tracking refs: %w", err)
	}
	return nil
}

func measureLocalBareSync(
	ctx context.Context,
	workbookBinary string,
	fixtureRoot string,
	samples int,
	commandTimeout time.Duration,
	measureCommand func(context.Context, CommandSpec) Sample,
	prepareSample func(context.Context, int) error,
) ([]ScenarioResult, error) {
	results := repositoryResults(samples)[1:]
	command := CommandSpec{
		Binary:    workbookBinary,
		Args:      []string{"sync", "--json"},
		Directory: fixtureRoot,
		Timeout:   commandTimeout,
	}
	for sample := range samples {
		if prepareSample != nil {
			if err := prepareSample(ctx, sample); err != nil {
				return nil, err
			}
		}
		results[0].Samples[sample] = measureCommand(ctx, command)
		if results[0].Samples[sample].TimedOut {
			results[1].Samples[sample] = Sample{
				ExitCode: -1,
				Error:    "not measured: initial sync timed out before remote completion",
			}
			continue
		}
		if !sampleSucceeded(results[0].Samples[sample]) {
			results[1].Samples[sample] = Sample{
				ExitCode: -1,
				Error:    "not measured: initial sync failed before remote completion",
			}
			continue
		}
		results[1].Samples[sample] = measureCommand(ctx, command)
	}
	for index := range results {
		results[index].Summary = Summarize(results[index].Samples)
	}
	return results, nil
}

var coldSingleTarget = ScenarioTarget{
	DurationStatistic:  DurationP95,
	DurationComparison: DurationAtMost,
	MaxMilliseconds:    200,
}

var warmUpdateTarget = ScenarioTarget{
	DurationStatistic:  DurationP95,
	DurationComparison: DurationAtMost,
	MaxMilliseconds:    100,
}

var burstTarget = ScenarioTarget{
	DurationStatistic:  DurationEverySample,
	DurationComparison: DurationLessThan,
	MaxMilliseconds:    1000,
}

func coldCLIResults(samples int) []ScenarioResult {
	results := make([]ScenarioResult, len(coldScenarioDefinitions))
	for index, definition := range coldScenarioDefinitions {
		results[index] = coldCLIResult(definition.name, samples)
	}
	return results
}

func coldCLIResult(name string, samples int) ScenarioResult {
	target := &coldSingleTarget
	if name == "cli-burst-independent-10" || name == "cli-burst-same-task-10" {
		target = &burstTarget
	}
	return ScenarioResult{Name: name, Surface: "cold-cli", Target: target, Samples: make([]Sample, samples)}
}

func warmHTTPResult(name string, samples int) ScenarioResult {
	target := &warmUpdateTarget
	if name == "api-burst-independent-10" || name == "api-burst-same-task-10" {
		target = &burstTarget
	}
	return ScenarioResult{
		Name:    name,
		Surface: "warm-http",
		Target:  target,
		Samples: make([]Sample, samples),
	}
}

func warmHTTPResults(samples int) []ScenarioResult {
	results := make([]ScenarioResult, len(warmHTTPScenarioDefinitions))
	for index, definition := range warmHTTPScenarioDefinitions {
		results[index] = warmHTTPResult(definition.name, samples)
	}
	return results
}

func repositoryResults(samples int) []ScenarioResult {
	names := []string{
		"projection-rebuild",
		"sync-initial-local-bare",
		"sync-unchanged-local-bare",
	}
	results := make([]ScenarioResult, len(names))
	for index, name := range names {
		results[index] = ScenarioResult{
			Name:    name,
			Surface: "repository",
			Samples: make([]Sample, samples),
		}
	}
	return results
}

func allocateWarmHTTPTasks(taskIDs []string) (warmHTTPTasks, error) {
	if len(taskIDs) < warmHTTPTasksPerFixture {
		return warmHTTPTasks{}, fmt.Errorf("fixture has %d tasks, need %d for warm HTTP scenarios", len(taskIDs), warmHTTPTasksPerFixture)
	}
	return warmHTTPTasks{
		update:      taskIDs[0],
		sameBurst:   taskIDs[1],
		independent: append([]string(nil), taskIDs[:warmHTTPTasksPerFixture]...),
	}, nil
}

func measureColdCLICommand(
	ctx context.Context,
	dependencies scenarioDependencies,
	spec RunSpec,
	directory string,
	args []string,
) Sample {
	return dependencies.measureCommand(ctx, CommandSpec{
		Binary:    spec.WorkbookBinary,
		Args:      args,
		Directory: directory,
		Timeout:   spec.CommandTimeout,
	})
}

type rebuildProjectionResult struct {
	TaskCount int `json:"taskCount"`
}

// prepareProjection rebuilds the disposable SQLite projection outside the
// measured mutation and validates the versioned rebuild result.
func prepareProjection(ctx context.Context, spec CommandSpec, totalTasks int) error {
	measurement := MeasureCommandOutput(ctx, spec)
	if measurement.Sample.ExitCode != 0 || measurement.Sample.Error != "" {
		return fmt.Errorf("rebuild failed with exit code %d: %s", measurement.Sample.ExitCode, measurement.Sample.Error)
	}
	var envelope remoteResultEnvelope
	if err := json.Unmarshal(measurement.Stdout, &envelope); err != nil {
		return fmt.Errorf("decode rebuild result: %w", err)
	}
	if envelope.Format != workbookResultFormat {
		return fmt.Errorf("rebuild result format = %q, want %q", envelope.Format, workbookResultFormat)
	}
	if envelope.Version != workbookJSONVersion {
		return fmt.Errorf("rebuild result version = %d, want %d", envelope.Version, workbookJSONVersion)
	}
	if envelope.Command != "rebuild" {
		return fmt.Errorf("rebuild result command = %q, want rebuild", envelope.Command)
	}
	var result rebuildProjectionResult
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		return fmt.Errorf("decode rebuild data: %w", err)
	}
	if result.TaskCount != totalTasks {
		return fmt.Errorf("rebuild task count = %d, want %d", result.TaskCount, totalTasks)
	}
	return nil
}

func measureSameTaskBurst(
	ctx context.Context,
	dependencies scenarioDependencies,
	spec RunSpec,
	directory string,
	taskID string,
) Sample {
	startedAt := time.Now()
	members := make([]Sample, 10)
	for command := range members {
		status := "ready"
		if command%2 == 1 {
			status = "in-progress"
		}
		members[command] = measureColdCLICommand(ctx, dependencies, spec, directory, []string{
			"update", taskID, "--status", status, "--json",
		})
	}
	return aggregateBurst(time.Since(startedAt), members)
}

func measureIndependentBurst(
	ctx context.Context,
	dependencies scenarioDependencies,
	spec RunSpec,
	directory string,
	taskIDs []string,
) Sample {
	start := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(len(taskIDs))
	done.Add(len(taskIDs))
	members := make([]Sample, len(taskIDs))
	for index, taskID := range taskIDs {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			members[index] = measureColdCLICommand(ctx, dependencies, spec, directory, []string{
				"update", taskID, "--status", "ready", "--json",
			})
		}()
	}
	ready.Wait()
	startedAt := time.Now()
	close(start)
	done.Wait()
	return aggregateBurst(time.Since(startedAt), members)
}

func startWarmHTTPServer(ctx context.Context, binary, directory string, timeout time.Duration) (*warmHTTPServer, error) {
	traceFile, err := os.CreateTemp("", "workbook-server-git-trace-*.json")
	if err != nil {
		return nil, fmt.Errorf("create server Trace2 event file: %w", err)
	}
	tracePath := traceFile.Name()
	if err := traceFile.Close(); err != nil {
		os.Remove(tracePath)
		return nil, fmt.Errorf("close server Trace2 event file: %w", err)
	}

	command := exec.Command(binary, "serve", "--addr", "127.0.0.1:0")
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TRACE2_EVENT="+tracePath)
	command.Stdout = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := command.StderrPipe()
	if err != nil {
		os.Remove(tracePath)
		return nil, fmt.Errorf("open server stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		os.Remove(tracePath)
		return nil, fmt.Errorf("start warm HTTP server: %w", err)
	}

	ready := make(chan string, 1)
	scanDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		reported := false
		for scanner.Scan() {
			line := scanner.Text()
			if !reported && strings.HasPrefix(line, warmServerPrefix) {
				ready <- strings.TrimSpace(strings.TrimPrefix(line, warmServerPrefix))
				reported = true
			}
		}
		scanDone <- scanner.Err()
	}()
	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()

	startupTimer := time.NewTimer(timeout)
	defer startupTimer.Stop()
	var address string
	select {
	case address = <-ready:
	case err := <-scanDone:
		terminateWarmServer(command)
		<-wait
		os.Remove(tracePath)
		if err != nil {
			return nil, fmt.Errorf("read warm HTTP server stderr: %w", err)
		}
		return nil, fmt.Errorf("warm HTTP server stderr closed before %q", warmServerPrefix)
	case err := <-wait:
		os.Remove(tracePath)
		if err == nil {
			return nil, fmt.Errorf("warm HTTP server exited before readiness")
		}
		return nil, fmt.Errorf("warm HTTP server exited before readiness: %w", err)
	case <-ctx.Done():
		terminateWarmServer(command)
		<-wait
		os.Remove(tracePath)
		return nil, ctx.Err()
	case <-startupTimer.C:
		terminateWarmServer(command)
		<-wait
		os.Remove(tracePath)
		return nil, fmt.Errorf("warm HTTP server did not report readiness within %s", timeout)
	}

	baseURL := "http://" + address
	client := &http.Client{}
	if err := waitForWarmHealth(ctx, client, baseURL, timeout); err != nil {
		terminateWarmServer(command)
		<-wait
		os.Remove(tracePath)
		return nil, err
	}
	return &warmHTTPServer{
		baseURL:   baseURL,
		tracePath: tracePath,
		command:   command,
		wait:      wait,
		client:    client,
	}, nil
}

func waitForWarmHealth(ctx context.Context, client *http.Client, baseURL string, timeout time.Duration) error {
	healthContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		request, err := http.NewRequestWithContext(healthContext, http.MethodGet, baseURL+"/healthz", nil)
		if err != nil {
			return fmt.Errorf("build warm HTTP health request: %w", err)
		}
		response, requestErr := client.Do(request)
		if requestErr == nil {
			_, readErr := io.Copy(io.Discard, response.Body)
			closeErr := response.Body.Close()
			if readErr != nil {
				return fmt.Errorf("read warm HTTP health response: %w", readErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close warm HTTP health response: %w", closeErr)
			}
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}

		select {
		case <-healthContext.Done():
			return fmt.Errorf("warm HTTP health check: %w", healthContext.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (server *warmHTTPServer) prepareProjection(ctx context.Context, activeTasks int, timeout time.Duration) error {
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, server.baseURL+"/api/tasks", nil)
	if err != nil {
		return fmt.Errorf("build task list request: %w", err)
	}
	response, err := server.client.Do(request)
	if err != nil {
		return fmt.Errorf("send task list request: %w", err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read task list response: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close task list response: %w", closeErr)
	}
	if response.StatusCode != http.StatusOK {
		evidence := conciseHTTPBody(body)
		message := fmt.Sprintf("HTTP %d %s", response.StatusCode, http.StatusText(response.StatusCode))
		if evidence != "" {
			message += ": " + evidence
		}
		return errors.New(message)
	}
	var document struct {
		Format  string            `json:"format"`
		Version int               `json:"version"`
		Tasks   []json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return fmt.Errorf("decode task list response: %w", err)
	}
	if document.Format != "workbook.tasks" || document.Version != 1 {
		return fmt.Errorf(
			"task list response = %q v%d, want workbook.tasks v1",
			document.Format, document.Version,
		)
	}
	if len(document.Tasks) != activeTasks {
		return fmt.Errorf("task list response task count = %d, want %d", len(document.Tasks), activeTasks)
	}
	return nil
}

func (server *warmHTTPServer) measureStatus(ctx context.Context, taskID, status string, timeout time.Duration) (Sample, error) {
	cursor, err := OpenTraceCursor(server.tracePath)
	if err != nil {
		return Sample{}, err
	}
	sample, err := server.performStatus(ctx, taskID, status, timeout)
	if err != nil {
		return Sample{}, err
	}
	gitProcesses, err := cursor.CountNewGitProcesses()
	if err != nil {
		return Sample{}, err
	}
	sample.GitProcesses = gitProcesses
	return sample, nil
}

func (server *warmHTTPServer) performStatus(ctx context.Context, taskID, status string, timeout time.Duration) (Sample, error) {
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestContext,
		http.MethodPatch,
		server.baseURL+"/api/tasks/"+url.PathEscape(taskID)+"/status",
		strings.NewReader(`{"status":"`+status+`"}`),
	)
	if err != nil {
		return Sample{}, fmt.Errorf("build status request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	startedAt := time.Now()
	response, err := server.client.Do(request)
	if err != nil {
		duration := time.Since(startedAt)
		if ctx.Err() == nil && errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			return Sample{
				Duration: duration,
				ExitCode: -1,
				TimedOut: true,
				Error:    fmt.Sprintf("send status request: %v", err),
			}, nil
		}
		return Sample{}, fmt.Errorf("send status request: %w", err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	duration := time.Since(startedAt)
	if readErr != nil {
		if ctx.Err() == nil && errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			return Sample{
				Duration: duration,
				ExitCode: -1,
				TimedOut: true,
				Error:    fmt.Sprintf("read status response: %v", readErr),
			}, nil
		}
		return Sample{}, fmt.Errorf("read status response: %w", readErr)
	}
	if closeErr != nil {
		return Sample{}, fmt.Errorf("close status response: %w", closeErr)
	}
	if response.StatusCode != http.StatusOK {
		evidence := conciseHTTPBody(body)
		message := fmt.Sprintf("HTTP %d %s", response.StatusCode, http.StatusText(response.StatusCode))
		if evidence != "" {
			message += ": " + evidence
		}
		return Sample{
			Duration: duration,
			ExitCode: response.StatusCode,
			Error:    message,
		}, nil
	}
	var document struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
		Task    struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"task"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return Sample{}, fmt.Errorf("decode status response: %w", err)
	}
	if document.Format != "workbook.task-mutation" || document.Version != 1 ||
		document.Task.ID != taskID || document.Task.Status != status {
		return Sample{}, fmt.Errorf(
			"status response = %q v%d task %q status %q, want workbook.task-mutation v1 task %q status %q",
			document.Format, document.Version, document.Task.ID, document.Task.Status, taskID, status,
		)
	}
	return Sample{
		Duration: duration,
		ExitCode: 0,
	}, nil
}

func (server *warmHTTPServer) measureSameTaskBurst(ctx context.Context, taskID string, statusOffset int, timeout time.Duration) (Sample, error) {
	startedAt := time.Now()
	members := make([]Sample, 0, 10)
	for command := range 10 {
		sample, err := server.measureStatus(ctx, taskID, alternatingStatus(command+statusOffset), timeout)
		if err != nil {
			return Sample{}, fmt.Errorf("request %d: %w", command+1, err)
		}
		members = append(members, sample)
		if !sampleSucceeded(sample) {
			break
		}
	}
	return aggregateBurst(time.Since(startedAt), members), nil
}

func conciseHTTPBody(body []byte) string {
	const maxEvidenceRunes = 256
	evidence := []rune(strings.Join(strings.Fields(string(body)), " "))
	if len(evidence) <= maxEvidenceRunes {
		return string(evidence)
	}
	return string(evidence[:maxEvidenceRunes]) + "..."
}

func (server *warmHTTPServer) measureIndependentBurst(ctx context.Context, taskIDs []string, status string, timeout time.Duration) (Sample, error) {
	start := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(len(taskIDs))
	done.Add(len(taskIDs))
	members := make([]Sample, len(taskIDs))
	errorsByRequest := make([]error, len(taskIDs))
	for index, taskID := range taskIDs {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			members[index], errorsByRequest[index] = server.performStatus(ctx, taskID, status, timeout)
		}()
	}
	ready.Wait()
	cursor, err := OpenTraceCursor(server.tracePath)
	if err != nil {
		return Sample{}, err
	}
	startedAt := time.Now()
	close(start)
	done.Wait()
	for index, err := range errorsByRequest {
		if err != nil {
			return Sample{}, fmt.Errorf("request %d: %w", index+1, err)
		}
	}
	gitProcesses, err := cursor.CountNewGitProcesses()
	if err != nil {
		return Sample{}, err
	}
	aggregate := aggregateBurst(time.Since(startedAt), members)
	aggregate.GitProcesses = gitProcesses
	return aggregate, nil
}

func (server *warmHTTPServer) close(timeout time.Duration) error {
	if err := interruptWarmServer(server.command); err != nil {
		return fmt.Errorf("interrupt warm HTTP server: %w", err)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-server.wait:
		os.Remove(server.tracePath)
		if err != nil {
			return fmt.Errorf("warm HTTP server did not exit cleanly: %w", err)
		}
		return nil
	case <-timer.C:
		terminateWarmServer(server.command)
		<-server.wait
		os.Remove(server.tracePath)
		return fmt.Errorf("warm HTTP server did not exit within %s", timeout)
	}
}

func alternatingStatus(index int) string {
	if index%2 == 0 {
		return "ready"
	}
	return "in-progress"
}

func terminateWarmServer(command *exec.Cmd) {
	_ = signalWarmServer(command, syscall.SIGKILL)
}

func interruptWarmServer(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	err := command.Process.Signal(os.Interrupt)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func signalWarmServer(command *exec.Cmd, signal syscall.Signal) error {
	if command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func enumerateTaskRefs(ctx context.Context, timeout time.Duration, directory string) ([]byte, time.Duration, error) {
	return runRepositoryGit(ctx, timeout, directory, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/workbook/tasks/")
}

func runRepositoryGit(ctx context.Context, timeout time.Duration, directory string, args ...string) ([]byte, time.Duration, error) {
	commandArgs := append([]string(nil), args...)
	if directory != "" {
		commandArgs = append([]string{"-C", directory}, commandArgs...)
	}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, "git", commandArgs...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	command.WaitDelay = commandWaitDelay
	startedAt := time.Now()
	output, err := command.CombinedOutput()
	duration := time.Since(startedAt)
	if commandContext.Err() == context.DeadlineExceeded {
		return nil, duration, fmt.Errorf("git %s timed out after %s", strings.Join(commandArgs, " "), timeout)
	}
	if err != nil {
		return nil, duration, fmt.Errorf("git %s: %w: %s", strings.Join(commandArgs, " "), err, strings.TrimSpace(string(output)))
	}
	return output, duration, nil
}

func parseCountObjects(output []byte) (countObjectsMetrics, error) {
	values := make(map[string]int64)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found {
			return countObjectsMetrics{}, fmt.Errorf("invalid count-objects line %q", scanner.Text())
		}
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || parsed < 0 {
			return countObjectsMetrics{}, fmt.Errorf("invalid count-objects %s value %q", key, strings.TrimSpace(value))
		}
		values[key] = parsed
	}
	if err := scanner.Err(); err != nil {
		return countObjectsMetrics{}, err
	}
	required := []string{"count", "size", "in-pack", "size-pack"}
	for _, key := range required {
		if _, found := values[key]; !found {
			return countObjectsMetrics{}, fmt.Errorf("count-objects output missing %q", key)
		}
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	if values["size"] > maxInt64/1024 || values["size-pack"] > maxInt64/1024 {
		return countObjectsMetrics{}, fmt.Errorf("count-objects KiB value overflows bytes")
	}
	return countObjectsMetrics{
		count:    values["count"],
		size:     values["size"],
		inPack:   values["in-pack"],
		sizePack: values["size-pack"],
	}, nil
}

func repositoryMetricsFromCounts(
	looseRefDuration time.Duration,
	packedRefDuration time.Duration,
	beforeOutput []byte,
	afterOutput []byte,
) (RepositoryMetrics, error) {
	before, err := parseCountObjects(beforeOutput)
	if err != nil {
		return RepositoryMetrics{}, fmt.Errorf("parse loose object metrics: %w", err)
	}
	after, err := parseCountObjects(afterOutput)
	if err != nil {
		return RepositoryMetrics{}, fmt.Errorf("parse packed object metrics: %w", err)
	}
	return RepositoryMetrics{
		LooseRefEnumerationMilliseconds:  durationAsMilliseconds(looseRefDuration),
		PackedRefEnumerationMilliseconds: durationAsMilliseconds(packedRefDuration),
		LooseObjects:                     before.count,
		LooseObjectBytes:                 before.size * 1024,
		PackedObjects:                    after.inPack,
		PackBytes:                        after.sizePack * 1024,
	}, nil
}

func sampleSucceeded(sample Sample) bool {
	return sample.ExitCode == 0 && !sample.TimedOut && sample.Error == ""
}

func durationAsMilliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}

func aggregateBurst(duration time.Duration, members []Sample) Sample {
	aggregate := Sample{Duration: duration, ExitCode: 0}
	var failures []string
	for index, member := range members {
		aggregate.GitProcesses += member.GitProcesses
		if member.ExitCode == 0 && !member.TimedOut && member.Error == "" {
			continue
		}
		if aggregate.ExitCode == 0 {
			aggregate.ExitCode = member.ExitCode
			if aggregate.ExitCode == 0 {
				aggregate.ExitCode = -1
			}
		}
		aggregate.TimedOut = aggregate.TimedOut || member.TimedOut
		detail := member.Error
		if member.TimedOut {
			detail = "timed out"
			if member.Error != "" {
				detail += ": " + member.Error
			}
		} else if detail == "" {
			detail = fmt.Sprintf("exit code %d", member.ExitCode)
		}
		failures = append(failures, fmt.Sprintf("command %d: %s", index+1, detail))
	}
	aggregate.Error = strings.Join(failures, "; ")
	return aggregate
}
