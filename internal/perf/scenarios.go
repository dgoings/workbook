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
	watcherReadyPrefix      = "Workbook sync watcher:"
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
	measureCommand    func(context.Context, CommandSpec) CommandMeasurement
	cleanupFixture    func(string) error
}

type warmHTTPTasks struct {
	update      string
	sameBurst   string
	independent []string
}

type warmScenarioServer interface {
	prepareProjection(context.Context, int, time.Duration) error
	measureTaskList(context.Context, int, time.Duration) (Sample, error)
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
		measureCommand:    MeasureCommandOutput,
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
	{name: "cli-list", measure: measureColdList},
	{name: "cli-move", measure: measureColdMove},
	{name: "cli-next", measure: measureColdNext},
	{name: "cli-restore", measure: measureColdRestore},
	{name: "cli-show", measure: measureColdShow},
	{name: "cli-update", measure: measureColdUpdate},
	{name: "cli-update-autosync", measure: measureColdUpdateAutoSync},
	{name: "cli-update-watched", measure: measureColdUpdateWatched},
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
		"create", fmt.Sprintf("Benchmark created task %d", sample+1), "--status", "ready", "--priority", "high", "--no-sync", "--json",
	}), nil
}

func measureColdDelete(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	taskID, err := fixtureActiveTask(fixture, 1)
	if err != nil {
		return Sample{}, err
	}
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{"delete", taskID[0], "--no-sync", "--json"}), nil
}

func measureColdDepend(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	taskIDs, err := fixtureActiveTask(fixture, 3)
	if err != nil {
		return Sample{}, err
	}
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{"depend", taskIDs[2], taskIDs[0], "--no-sync", "--json"}), nil
}

func measureColdFree(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	if len(fixture.Dependencies) == 0 {
		return Sample{}, fmt.Errorf("fixture has no direct dependency")
	}
	pair := fixture.Dependencies[0]
	if !containsTaskID(fixture.ActiveTaskIDs, pair.Dependent) || !containsTaskID(fixture.ActiveTaskIDs, pair.Dependency) {
		return Sample{}, fmt.Errorf("fixture direct dependency must have active tasks")
	}
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{"free", pair.Dependent, pair.Dependency, "--no-sync", "--json"}), nil
}

func measureColdMove(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	taskIDs, err := fixtureActiveTask(fixture, 2)
	if err != nil {
		return Sample{}, err
	}
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{"move", taskIDs[1], "--before", taskIDs[0], "--no-sync", "--json"}), nil
}

func measureColdRestore(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	if len(fixture.TombstonedTaskIDs) == 0 {
		return Sample{}, fmt.Errorf("fixture has no tombstoned tasks")
	}
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{"restore", fixture.TombstonedTaskIDs[0], "--no-sync", "--json"}), nil
}

// measureColdNext measures the command an agent runs to acquire work.
//
// It deliberately leaves automatic synchronization enabled. `workbook next`
// fetches before answering so two agents cannot claim the same task, and that
// fetch is the point of the scenario: measuring it with --no-sync would report a
// local read that no agent ever performs. The origin is published as setup so
// the sample covers the steady-state fetch rather than an initial publication,
// exactly as cli-update-autosync does for the mutation path.
//
// The setup order matters and each step earns its place:
//
//   - `next` only ever selects a task whose status is `ready`, and the fixture's
//     deterministic generator never leaves one there. Without the first step the
//     board holds nothing to acquire, and the scenario would report the agent's
//     acquire step while measuring a search that always comes up empty.
//   - Origin is published after that mutation, so the measured fetch meets an
//     already-synchronized remote and reconciles nothing. Publishing first would
//     leave the local task ahead and price a replay this scenario does not claim
//     to measure.
//   - The mutation leaves the projection one head stale, and refreshing a single
//     changed head is tens of milliseconds at acceptance size. Re-settling it
//     untimed keeps the sample comparable with every other cold scenario, whose
//     projection is current when its command starts.
//
// The measured command's own answer is then checked. A successful setup does not
// prove the board held work when the timed command ran, and `next` reports an
// empty search by exiting 0 with a null result rather than by failing.
func measureColdNext(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	taskIDs, err := fixtureActiveTask(fixture, 1)
	if err != nil {
		return Sample{}, err
	}
	// This sample is deliberately discarded: the mutation is setup, not the
	// measurement. Its exit code is still checked, because a board with nothing
	// acquirable would answer fast and mean nothing.
	setup := measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{
		"update", taskIDs[0], "--status", "ready", "--no-sync", "--json",
	})
	if setup.ExitCode != 0 {
		return Sample{}, fmt.Errorf(
			"prepare an acquirable task: exit code %d: %s", setup.ExitCode, setup.Error,
		)
	}
	if err := publishFixtureToLocalOrigin(ctx, spec.CommandTimeout, fixture.Root); err != nil {
		return Sample{}, err
	}
	if err := dependencies.prepareProjection(ctx, CommandSpec{
		Binary:    spec.WorkbookBinary,
		Args:      []string{"rebuild", "--json"},
		Directory: fixture.Root,
		Timeout:   spec.CommandTimeout,
	}, spec.Fixture.TotalTasks); err != nil {
		return Sample{}, fmt.Errorf("re-settle the projection after preparing an acquirable task: %w", err)
	}
	measured := measureColdCLIOutput(ctx, dependencies, spec, fixture.Root, []string{"next", "--json"})
	if err := verifyAcquiredTask(measured); err != nil {
		return Sample{}, err
	}
	return measured.Sample, nil
}

// verifyAcquiredTask refuses a cli-next sample that acquired nothing.
//
// `workbook next` answers an empty board by exiting 0 and writing a null result,
// so the exit code proves only that the search ran. Selection also requires
// every dependency to be done, which the fixture's chain satisfies for exactly
// one task today; a change to the generator, to which task the scenario makes
// ready, or to the eligibility rules would silently turn the sample into a timed
// whole-board scan published as the agent's acquire latency.
//
// A sample that timed out or exited non-zero is left alone. It produced no
// answer to inspect, and the harness records those as `timeout` and `failed`
// outcomes rather than discarding a run's collected evidence.
func verifyAcquiredTask(measurement CommandMeasurement) error {
	if measurement.Sample.TimedOut || measurement.Sample.ExitCode != 0 {
		return nil
	}
	var envelope remoteResultEnvelope
	if err := json.Unmarshal(measurement.Stdout, &envelope); err != nil {
		return fmt.Errorf("decode next result: %w", err)
	}
	if envelope.Format != workbookResultFormat || envelope.Version != workbookJSONVersion || envelope.Command != "next" {
		return fmt.Errorf(
			"next result = %q v%d command %q, want %q v%d command next",
			envelope.Format, envelope.Version, envelope.Command, workbookResultFormat, workbookJSONVersion,
		)
	}
	var acquired struct {
		ID string `json:"id"`
	}
	if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, &acquired); err != nil {
			return fmt.Errorf("decode next data: %w", err)
		}
	}
	if acquired.ID == "" {
		return fmt.Errorf("next acquired no task: the board held nothing eligible when the measured command ran")
	}
	return nil
}

// measureColdShow measures the command an agent runs to read a task's context.
// It reads one task through a read-only service and synchronizes nothing, so it
// belongs to the local class and needs no origin.
func measureColdShow(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	taskID, err := fixtureActiveTask(fixture, 1)
	if err != nil {
		return Sample{}, err
	}
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{"show", taskID[0], "--json"}), nil
}

func measureColdUpdate(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	taskID, err := fixtureActiveTask(fixture, 1)
	if err != nil {
		return Sample{}, err
	}
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{"update", taskID[0], "--status", "ready", "--no-sync", "--json"}), nil
}

// measureColdUpdateAutoSync measures a mutation with automatic synchronization
// left enabled, against a local bare origin that already holds the project's
// task refs.
//
// The already-synchronized topology is the steady state a team works in, and it
// is the shape the fetch-then-targeted-push sequence has to stay affordable in.
// Its sibling cli-update measures the same mutation with --no-sync, so the two
// budgets separate local mutation cost from synchronization cost instead of
// letting a local regression hide inside network variance.
//
// Creating the origin and publishing the starting refs is setup, outside the
// measured sample.
func measureColdUpdateAutoSync(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	taskID, err := fixtureActiveTask(fixture, 1)
	if err != nil {
		return Sample{}, err
	}
	if err := publishFixtureToLocalOrigin(ctx, spec.CommandTimeout, fixture.Root); err != nil {
		return Sample{}, err
	}
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{
		"update", taskID[0], "--status", "ready", "--json",
	}), nil
}

// measureColdUpdateWatched measures a mutation with a sync watcher running.
//
// It is held to the local budget rather than the auto-sync one on purpose: the
// claim this scenario exists to test is that a watched mutation is a local
// mutation, and measuring it against the network budget would prove nothing.
//
// The watcher's interval is an hour so it never synchronizes on its own, which
// leaves the sample measuring the probe and the nudge rather than the watcher's
// Git work. Trace2 attribution is already correct, because the harness passes
// GIT_TRACE2_EVENT only to the measured command.
func measureColdUpdateWatched(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	taskID, err := fixtureActiveTask(fixture, 1)
	if err != nil {
		return Sample{}, err
	}
	if err := publishFixtureToLocalOrigin(ctx, spec.CommandTimeout, fixture.Root); err != nil {
		return Sample{}, err
	}
	watcher, err := startSyncWatcher(ctx, spec.WorkbookBinary, fixture.Root, spec.CommandTimeout)
	if err != nil {
		return Sample{}, err
	}
	defer watcher.stop()
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{
		"update", taskID[0], "--status", "ready", "--json",
	}), nil
}

type syncWatcher struct {
	command *exec.Cmd
	wait    <-chan error
}

// startSyncWatcher runs `workbook sync --watch` and returns once it is not just
// listening but trustworthy, since a mutation correctly refuses to defer to a
// watcher that has not yet completed a synchronization.
func startSyncWatcher(ctx context.Context, binary, directory string, timeout time.Duration) (*syncWatcher, error) {
	command := exec.Command(binary, "sync", "--watch", "--interval", "1h")
	command.Dir = directory
	// The watcher's own Git processes must not be attributed to the measured
	// command, so it runs without a Trace2 event file of its own.
	command.Env = append(os.Environ(), "GIT_TRACE2_EVENT=")
	command.Stdout = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open sync watcher stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start sync watcher: %w", err)
	}

	ready := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		reported := false
		for scanner.Scan() {
			if !reported && strings.HasPrefix(scanner.Text(), watcherReadyPrefix) {
				ready <- struct{}{}
				reported = true
			}
		}
	}()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	watcher := &syncWatcher{command: command, wait: wait}

	startupTimer := time.NewTimer(timeout)
	defer startupTimer.Stop()
	select {
	case <-ready:
	case err := <-wait:
		return nil, fmt.Errorf("sync watcher exited before readiness: %w", err)
	case <-ctx.Done():
		watcher.stop()
		return nil, ctx.Err()
	case <-startupTimer.C:
		watcher.stop()
		return nil, fmt.Errorf("sync watcher did not report readiness within %s", timeout)
	}

	if err := waitForWatcherSync(ctx, binary, directory, timeout); err != nil {
		watcher.stop()
		return nil, err
	}
	return watcher, nil
}

func waitForWatcherSync(ctx context.Context, binary, directory string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		command := exec.Command(binary, "sync", "--status", "--json")
		command.Dir = directory
		output, err := command.Output()
		if err == nil {
			var envelope struct {
				Data struct {
					Running    bool `json:"running"`
					LastSyncOK bool `json:"lastSyncOk"`
				} `json:"data"`
			}
			if json.Unmarshal(output, &envelope) == nil && envelope.Data.Running && envelope.Data.LastSyncOK {
				return nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("sync watcher did not complete a synchronization within %s", timeout)
}

func (w *syncWatcher) stop() {
	terminateWarmServer(w.command)
	<-w.wait
}

// publishFixtureToLocalOrigin gives the fixture an origin holding its current
// task refs. The bare repository lives inside the fixture so the harness's
// existing per-sample cleanup reclaims it.
func publishFixtureToLocalOrigin(ctx context.Context, timeout time.Duration, fixtureRoot string) error {
	output, _, err := runRepositoryGit(ctx, timeout, fixtureRoot, "rev-parse", "--show-object-format")
	if err != nil {
		return err
	}
	objectFormat := strings.TrimSuffix(string(output), "\n")
	if objectFormat == "" || strings.ContainsAny(objectFormat, "\r\n\t ") {
		return fmt.Errorf("Git returned invalid repository object format %q", objectFormat)
	}

	origin := filepath.Join(fixtureRoot, "benchmark-origin.git")
	if _, _, err := runRepositoryGit(
		ctx, timeout, "", "init", "--bare", "--quiet", "--object-format="+objectFormat, origin,
	); err != nil {
		return err
	}
	if _, _, err := runRepositoryGit(ctx, timeout, fixtureRoot, "remote", "add", "origin", origin); err != nil {
		return err
	}

	// Git rejects a wildcard refspec that matches nothing, so a fixture with no
	// task refs publishes nothing rather than failing the scenario.
	refs, _, err := runRepositoryGit(
		ctx, timeout, fixtureRoot, "for-each-ref", "--format=%(refname)", "refs/workbook/tasks/",
	)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(refs)) == 0 {
		return nil
	}
	if _, _, err := runRepositoryGit(
		ctx, timeout, fixtureRoot, "push", "--quiet", "origin",
		"refs/workbook/tasks/*:refs/workbook/tasks/*",
	); err != nil {
		return err
	}
	return nil
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
	{name: "api-tasks", measure: measureWarmTaskList},
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

// measureWarmTaskList measures the board's read side: a GET of the whole task
// collection against an already-warmed server holding the populated fixture.
// The untimed preparation load has already verified the same population, so this
// sample measures a warm read rather than a first read that also opens the
// projection.
func measureWarmTaskList(ctx context.Context, server warmScenarioServer, fixture Fixture, timeout time.Duration) (Sample, error) {
	return server.measureTaskList(ctx, len(fixture.ActiveTaskIDs), timeout)
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
// origin, so each sample measures the same initial-publication and
// already-synchronized topology. That setup is plain Git work outside both
// measured samples.
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

// deleteTrackingTaskRefs clears any fetched tracking ref so a sample's
// unpublished starting topology does not depend on the measured product still
// pruning stale tracking refs during its own fetch. It is normally a no-op.
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

// coldAutoSyncTarget budgets a synchronized mutation. Two connections to origin
// dominate it, and a connection costs roughly the same whatever it carries, so
// the budget is a network allowance rather than a scaled local budget.
var coldAutoSyncTarget = ScenarioTarget{
	DurationStatistic:  DurationP95,
	DurationComparison: DurationAtMost,
	MaxMilliseconds:    1000,
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
	switch name {
	case "cli-burst-independent-10", "cli-burst-same-task-10":
		target = &burstTarget
	case "cli-update-autosync", "cli-next":
		// `next` fetches before answering, so it is priced in the
		// synchronized class rather than held to a local budget it cannot
		// meet by design.
		target = &coldAutoSyncTarget
	case "cli-list":
		// The read path has no approved duration budget, so it is reported
		// descriptively rather than classified against an invented threshold.
		target = nil
	}
	return ScenarioResult{Name: name, Surface: "cold-cli", Target: target, Samples: make([]Sample, samples)}
}

func warmHTTPResult(name string, samples int) ScenarioResult {
	target := &warmUpdateTarget
	switch name {
	case "api-burst-independent-10", "api-burst-same-task-10":
		target = &burstTarget
	case "api-tasks":
		// The 100 ms warm budget was approved for a single mutation. The read
		// path has no approved budget, so it is reported descriptively rather
		// than classified against an invented threshold.
		target = nil
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
	return measureColdCLIOutput(ctx, dependencies, spec, directory, args).Sample
}

// measureColdCLIOutput keeps the measured command's streams, which a scenario
// needs when the exit code alone does not prove the measured work happened.
func measureColdCLIOutput(
	ctx context.Context,
	dependencies scenarioDependencies,
	spec RunSpec,
	directory string,
	args []string,
) CommandMeasurement {
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
			"update", taskID, "--status", status, "--no-sync", "--json",
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
				"update", taskID, "--status", "ready", "--no-sync", "--json",
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
	_, err := server.loadTaskList(ctx, activeTasks, timeout)
	return err
}

// taskListRequestError reports a GET /api/tasks that reached its own timeout or
// answered with a non-OK status. Both are outcomes the report can carry, so a
// caller that is measuring translates one into a sample; a caller that is only
// preparing a fixture treats it as the fatal error it already was.
type taskListRequestError struct {
	Duration   time.Duration
	StatusCode int
	TimedOut   bool
	Message    string
}

func (err *taskListRequestError) Error() string { return err.Message }

// sample renders the failure the way performStatus renders its own: a timeout
// keeps the harness's -1 exit code, and a server error keeps the HTTP status.
func (err *taskListRequestError) sample() Sample {
	exitCode := err.StatusCode
	if err.TimedOut {
		exitCode = -1
	}
	return Sample{
		Duration: err.Duration,
		ExitCode: exitCode,
		TimedOut: err.TimedOut,
		Error:    err.Message,
	}
}

// measureTaskList times one warm GET of the board's task collection and holds it
// to the same population oracle the untimed preparation load uses. A read that
// answered with an empty board would be fast and worthless, so the count is
// checked before the sample is reported.
//
// A read that timed out or that the server refused is reported as a sample
// instead, exactly as measureStatus reports one. Those are the outcomes
// `timeout` and `failed` exist for, and aborting the run on sample 7 of 20 would
// discard every measurement already collected and write no report at all.
func (server *warmHTTPServer) measureTaskList(ctx context.Context, activeTasks int, timeout time.Duration) (Sample, error) {
	cursor, err := OpenTraceCursor(server.tracePath)
	if err != nil {
		return Sample{}, err
	}
	sample, err := server.performTaskList(ctx, activeTasks, timeout)
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

// performTaskList runs the measured read and keeps the failures the report is
// built to carry. Anything else — a malformed answer, the wrong envelope, an
// unpopulated board, a cancelled caller — stays fatal, because none of those is
// a measurement of the board's read cost.
func (server *warmHTTPServer) performTaskList(ctx context.Context, activeTasks int, timeout time.Duration) (Sample, error) {
	duration, err := server.loadTaskList(ctx, activeTasks, timeout)
	if err != nil {
		var requestErr *taskListRequestError
		if errors.As(err, &requestErr) {
			return requestErr.sample(), nil
		}
		return Sample{}, err
	}
	return Sample{Duration: duration, ExitCode: 0}, nil
}

// loadTaskList performs one GET /api/tasks, verifies the versioned document and
// its active-task population, and returns the elapsed request time. A request
// that reached its own timeout or that the server refused is reported as a
// *taskListRequestError so a measuring caller can classify it.
func (server *warmHTTPServer) loadTaskList(ctx context.Context, activeTasks int, timeout time.Duration) (time.Duration, error) {
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, server.baseURL+"/api/tasks", nil)
	if err != nil {
		return 0, fmt.Errorf("build task list request: %w", err)
	}
	startedAt := time.Now()
	response, err := server.client.Do(request)
	if err != nil {
		duration := time.Since(startedAt)
		message := fmt.Sprintf("send task list request: %v", err)
		// A cancelled caller is the harness shutting down rather than the
		// command reaching its own deadline, so it stays fatal.
		if ctx.Err() == nil && errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			return 0, &taskListRequestError{Duration: duration, TimedOut: true, Message: message}
		}
		return 0, fmt.Errorf("send task list request: %w", err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	duration := time.Since(startedAt)
	if readErr != nil {
		if ctx.Err() == nil && errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			return 0, &taskListRequestError{
				Duration: duration,
				TimedOut: true,
				Message:  fmt.Sprintf("read task list response: %v", readErr),
			}
		}
		return 0, fmt.Errorf("read task list response: %w", readErr)
	}
	if closeErr != nil {
		return 0, fmt.Errorf("close task list response: %w", closeErr)
	}
	if response.StatusCode != http.StatusOK {
		evidence := conciseHTTPBody(body)
		message := fmt.Sprintf("HTTP %d %s", response.StatusCode, http.StatusText(response.StatusCode))
		if evidence != "" {
			message += ": " + evidence
		}
		return 0, &taskListRequestError{
			Duration:   duration,
			StatusCode: response.StatusCode,
			Message:    message,
		}
	}
	var document struct {
		Format  string            `json:"format"`
		Version int               `json:"version"`
		Tasks   []json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return 0, fmt.Errorf("decode task list response: %w", err)
	}
	if document.Format != "workbook.tasks" || document.Version != 1 {
		return 0, fmt.Errorf(
			"task list response = %q v%d, want workbook.tasks v1",
			document.Format, document.Version,
		)
	}
	if len(document.Tasks) != activeTasks {
		return 0, fmt.Errorf("task list response task count = %d, want %d", len(document.Tasks), activeTasks)
	}
	return duration, nil
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
