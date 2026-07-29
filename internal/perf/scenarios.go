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

const (
	coldCreate = iota
	coldDelete
	coldDepend
	coldFree
	coldMove
	coldRestore
	coldUpdate
	coldBurstIndependent
	coldBurstSameTask
)

// RunSpec configures one benchmark scenario run.
type RunSpec struct {
	WorkbookBinary string
	Fixture        FixtureSpec
	Samples        int
	CommandTimeout time.Duration
}

type coldCLITasks struct {
	update      string
	delete      string
	move        string
	moveAnchor  string
	dependent   string
	dependency  string
	sameBurst   string
	independent []string
}

type scenarioDependencies struct {
	buildFixture   func(context.Context, string, FixtureSpec) (Fixture, error)
	measureCommand func(context.Context, CommandSpec) Sample
}

type warmHTTPTasks struct {
	update      string
	sameBurst   string
	independent []string
}

type warmScenarioServer interface {
	measureStatus(context.Context, string, string, time.Duration) (Sample, error)
	measureIndependentBurst(context.Context, []string, string, time.Duration) (Sample, error)
	measureSameTaskBurst(context.Context, string, int, time.Duration) (Sample, error)
	close(time.Duration) error
}

type warmHTTPDependencies struct {
	buildFixture func(context.Context, string, FixtureSpec) (Fixture, error)
	startServer  func(context.Context, string, string, time.Duration) (warmScenarioServer, error)
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
func RunColdCLI(ctx context.Context, spec RunSpec, fixtureRoot string) ([]ScenarioResult, error) {
	return runColdCLI(ctx, spec, fixtureRoot, scenarioDependencies{
		buildFixture: func(ctx context.Context, root string, fixture FixtureSpec) (Fixture, error) {
			return buildFixtureWithinTimeout(ctx, root, fixture, spec.CommandTimeout)
		},
		measureCommand: MeasureCommand,
	})
}

func buildFixtureWithinTimeout(ctx context.Context, root string, spec FixtureSpec, timeout time.Duration) (Fixture, error) {
	fixtureContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return BuildFixture(fixtureContext, root, spec)
}

func runColdCLI(ctx context.Context, spec RunSpec, fixtureRoot string, dependencies scenarioDependencies) ([]ScenarioResult, error) {
	if spec.Samples < 1 {
		return nil, fmt.Errorf("samples must be positive")
	}
	if spec.CommandTimeout <= 0 {
		return nil, fmt.Errorf("command timeout must be positive")
	}
	if fixtureRoot == "" {
		return nil, fmt.Errorf("fixture root is required")
	}
	if err := os.MkdirAll(fixtureRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create fixture root: %w", err)
	}

	results := coldCLIResults(spec.Samples)
	for sample := range spec.Samples {
		sampleRoot := filepath.Join(fixtureRoot, fmt.Sprintf("sample-%03d", sample+1))

		createFixture, _, err := buildColdCLIFixture(ctx, dependencies, spec.Fixture, sampleRoot, "create")
		if err != nil {
			return nil, err
		}
		results[coldCreate].Samples[sample] = measureColdCLICommand(ctx, dependencies, spec, createFixture.Root, []string{
			"create", fmt.Sprintf("Benchmark created task %d", sample+1),
			"--status", "ready", "--priority", "high", "--json",
		})

		deleteFixture, deleteTasks, err := buildColdCLIFixture(ctx, dependencies, spec.Fixture, sampleRoot, "delete-restore")
		if err != nil {
			return nil, err
		}
		results[coldDelete].Samples[sample] = measureColdCLICommand(ctx, dependencies, spec, deleteFixture.Root, []string{
			"delete", deleteTasks.delete, "--json",
		})
		results[coldRestore].Samples[sample] = measureColdCLICommand(ctx, dependencies, spec, deleteFixture.Root, []string{
			"restore", deleteTasks.delete, "--json",
		})

		dependFixture, dependTasks, err := buildColdCLIFixture(ctx, dependencies, spec.Fixture, sampleRoot, "depend-free")
		if err != nil {
			return nil, err
		}
		results[coldDepend].Samples[sample] = measureColdCLICommand(ctx, dependencies, spec, dependFixture.Root, []string{
			"depend", dependTasks.dependent, dependTasks.dependency, "--json",
		})
		results[coldFree].Samples[sample] = measureColdCLICommand(ctx, dependencies, spec, dependFixture.Root, []string{
			"free", dependTasks.dependent, dependTasks.dependency, "--json",
		})

		moveFixture, moveTasks, err := buildColdCLIFixture(ctx, dependencies, spec.Fixture, sampleRoot, "move")
		if err != nil {
			return nil, err
		}
		results[coldMove].Samples[sample] = measureColdCLICommand(ctx, dependencies, spec, moveFixture.Root, []string{
			"move", moveTasks.move, "--before", moveTasks.moveAnchor, "--json",
		})

		updateFixture, updateTasks, err := buildColdCLIFixture(ctx, dependencies, spec.Fixture, sampleRoot, "update")
		if err != nil {
			return nil, err
		}
		results[coldUpdate].Samples[sample] = measureColdCLICommand(ctx, dependencies, spec, updateFixture.Root, []string{
			"update", updateTasks.update, "--status", "ready", "--json",
		})

		independentFixture, independentTasks, err := buildColdCLIFixture(ctx, dependencies, spec.Fixture, sampleRoot, "burst-independent")
		if err != nil {
			return nil, err
		}
		results[coldBurstIndependent].Samples[sample] = measureIndependentBurst(
			ctx, dependencies, spec, independentFixture.Root, independentTasks.independent,
		)

		sameFixture, sameTasks, err := buildColdCLIFixture(ctx, dependencies, spec.Fixture, sampleRoot, "burst-same-task")
		if err != nil {
			return nil, err
		}
		results[coldBurstSameTask].Samples[sample] = measureSameTaskBurst(
			ctx, dependencies, spec, sameFixture.Root, sameTasks.sameBurst,
		)
	}
	for index := range results {
		results[index].Summary = Summarize(results[index].Samples)
	}
	return results, nil
}

// RunWarmHTTP measures status mutations against independently warmed Workbook
// servers so each scenario sample starts from an exact-size fixture.
func RunWarmHTTP(ctx context.Context, spec RunSpec, fixtureRoot string) ([]ScenarioResult, error) {
	return runWarmHTTP(ctx, spec, fixtureRoot, warmHTTPDependencies{
		buildFixture: func(ctx context.Context, root string, fixture FixtureSpec) (Fixture, error) {
			return buildFixtureWithinTimeout(ctx, root, fixture, spec.CommandTimeout)
		},
		startServer: func(ctx context.Context, binary, root string, timeout time.Duration) (warmScenarioServer, error) {
			server, err := startWarmHTTPServer(ctx, binary, root, timeout)
			return server, err
		},
	})
}

func runWarmHTTP(ctx context.Context, spec RunSpec, fixtureRoot string, dependencies warmHTTPDependencies) ([]ScenarioResult, error) {
	if spec.Samples < 1 {
		return nil, fmt.Errorf("samples must be positive")
	}
	if spec.CommandTimeout <= 0 {
		return nil, fmt.Errorf("command timeout must be positive")
	}
	if fixtureRoot == "" {
		return nil, fmt.Errorf("fixture root is required")
	}
	if err := os.MkdirAll(fixtureRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create warm fixture root: %w", err)
	}

	results := warmHTTPResults(spec.Samples)
	for sample := range spec.Samples {
		sampleRoot := filepath.Join(fixtureRoot, fmt.Sprintf("sample-%03d", sample+1))
		for scenario := range results {
			name := results[scenario].Name
			root := filepath.Join(sampleRoot, name)
			fixture, err := dependencies.buildFixture(ctx, root, spec.Fixture)
			if err != nil {
				return nil, fmt.Errorf("build warm %s sample %d fixture: %w", name, sample+1, err)
			}
			tasks, err := allocateWarmHTTPTasks(fixture.ActiveTaskIDs)
			if err != nil {
				return nil, fmt.Errorf("allocate warm %s sample %d fixture: %w", name, sample+1, err)
			}
			server, err := dependencies.startServer(ctx, spec.WorkbookBinary, fixture.Root, spec.CommandTimeout)
			if err != nil {
				return nil, fmt.Errorf("start warm %s sample %d server: %w", name, sample+1, err)
			}

			var measured Sample
			switch scenario {
			case 0:
				measured, err = server.measureStatus(ctx, tasks.update, "ready", spec.CommandTimeout)
			case 1:
				measured, err = server.measureIndependentBurst(ctx, tasks.independent, "ready", spec.CommandTimeout)
			case 2:
				measured, err = server.measureSameTaskBurst(ctx, tasks.sameBurst, 0, spec.CommandTimeout)
			}
			closeErr := server.close(spec.CommandTimeout)
			if err != nil {
				return nil, fmt.Errorf("measure %s sample %d: %w", name, sample+1, err)
			}
			if closeErr != nil {
				return nil, fmt.Errorf("close warm %s sample %d server: %w", name, sample+1, closeErr)
			}
			results[scenario].Samples[sample] = measured
		}
	}
	for index := range results {
		results[index].Summary = Summarize(results[index].Samples)
	}
	return results, nil
}

// MeasureRepository records projection, ref, object, and local-bare-remote
// measurements against an existing Workbook fixture.
func MeasureRepository(ctx context.Context, workbookBinary, fixtureRoot string, commandTimeout time.Duration) (RepositoryMetrics, []ScenarioResult, error) {
	if workbookBinary == "" {
		return RepositoryMetrics{}, nil, fmt.Errorf("workbook binary is required")
	}
	if fixtureRoot == "" {
		return RepositoryMetrics{}, nil, fmt.Errorf("fixture root is required")
	}
	if commandTimeout <= 0 {
		return RepositoryMetrics{}, nil, fmt.Errorf("command timeout must be positive")
	}

	taskRefs, _, err := enumerateTaskRefs(ctx, commandTimeout, fixtureRoot)
	if err != nil {
		return RepositoryMetrics{}, nil, err
	}
	taskID, err := firstTaskID(taskRefs)
	if err != nil {
		return RepositoryMetrics{}, nil, err
	}

	results, err := measureProjectionScenarios(
		ctx,
		workbookBinary,
		fixtureRoot,
		taskID,
		commandTimeout,
		MeasureCommand,
	)
	if err != nil {
		return RepositoryMetrics{}, nil, err
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
	origin := filepath.Join(originRoot, "origin.git")
	if _, _, err := runRepositoryGit(ctx, commandTimeout, "", "init", "--bare", "--quiet", origin); err != nil {
		return RepositoryMetrics{}, nil, err
	}
	if _, _, err := runRepositoryGit(ctx, commandTimeout, fixtureRoot, "remote", "add", "origin", origin); err != nil {
		return RepositoryMetrics{}, nil, err
	}

	syncResults, err := measureLocalBareSync(
		ctx,
		workbookBinary,
		fixtureRoot,
		commandTimeout,
		MeasureCommand,
	)
	if err != nil {
		return RepositoryMetrics{}, nil, err
	}
	results = append(results[:3:3], syncResults...)

	for index := range results {
		results[index].Summary = Summarize(results[index].Samples)
	}
	return metrics, results, nil
}

func measureProjectionScenarios(
	ctx context.Context,
	workbookBinary string,
	fixtureRoot string,
	taskID string,
	commandTimeout time.Duration,
	measureCommand func(context.Context, CommandSpec) Sample,
) ([]ScenarioResult, error) {
	results := repositoryResults()[:3]
	results[0].Samples[0] = measureCommand(ctx, CommandSpec{
		Binary:    workbookBinary,
		Args:      []string{"rebuild", "--json"},
		Directory: fixtureRoot,
		Timeout:   commandTimeout,
	})
	results[1].Samples[0] = measureCommand(ctx, CommandSpec{
		Binary:    workbookBinary,
		Args:      []string{"list", "--json"},
		Directory: fixtureRoot,
		Timeout:   commandTimeout,
	})

	updateSample := measureCommand(ctx, CommandSpec{
		Binary:    workbookBinary,
		Args:      []string{"update", taskID, "--status", "ready", "--json"},
		Directory: fixtureRoot,
		Timeout:   commandTimeout,
	})
	if err := requireSuccessfulSample("prepare projection-refresh-one-changed", updateSample); err != nil {
		return nil, err
	}
	results[2].Samples[0] = measureCommand(ctx, CommandSpec{
		Binary:    workbookBinary,
		Args:      []string{"list", "--json"},
		Directory: fixtureRoot,
		Timeout:   commandTimeout,
	})

	for index := range results {
		results[index].Summary = Summarize(results[index].Samples)
	}
	return results, nil
}

func measureLocalBareSync(
	ctx context.Context,
	workbookBinary string,
	fixtureRoot string,
	commandTimeout time.Duration,
	measureCommand func(context.Context, CommandSpec) Sample,
) ([]ScenarioResult, error) {
	results := repositoryResults()[3:]
	command := CommandSpec{
		Binary:    workbookBinary,
		Args:      []string{"sync", "--json"},
		Directory: fixtureRoot,
		Timeout:   commandTimeout,
	}
	results[0].Samples[0] = measureCommand(ctx, command)
	if results[0].Samples[0].TimedOut {
		results[1].Samples[0] = Sample{
			ExitCode: -1,
			Error:    "not measured: initial sync timed out before remote completion",
		}
		for index := range results {
			results[index].Summary = Summarize(results[index].Samples)
		}
		return results, nil
	}
	if !sampleSucceeded(results[0].Samples[0]) {
		results[1].Samples[0] = Sample{
			ExitCode: -1,
			Error:    "not measured: initial sync failed before remote completion",
		}
		for index := range results {
			results[index].Summary = Summarize(results[index].Samples)
		}
		return results, nil
	}

	results[1].Samples[0] = measureCommand(ctx, command)
	for index := range results {
		results[index].Summary = Summarize(results[index].Samples)
	}
	return results, nil
}

func coldCLIResults(samples int) []ScenarioResult {
	names := []string{
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
	results := make([]ScenarioResult, len(names))
	for index, name := range names {
		results[index] = ScenarioResult{
			Name:    name,
			Surface: "cold-cli",
			Samples: make([]Sample, samples),
		}
	}
	return results
}

func warmHTTPResults(samples int) []ScenarioResult {
	names := []string{
		"api-update",
		"api-burst-independent-10",
		"api-burst-same-task-10",
	}
	results := make([]ScenarioResult, len(names))
	for index, name := range names {
		results[index] = ScenarioResult{
			Name:    name,
			Surface: "warm-http",
			Samples: make([]Sample, samples),
		}
	}
	return results
}

func repositoryResults() []ScenarioResult {
	names := []string{
		"projection-rebuild",
		"projection-refresh-unchanged",
		"projection-refresh-one-changed",
		"sync-initial-local-bare",
		"sync-unchanged-local-bare",
	}
	results := make([]ScenarioResult, len(names))
	for index, name := range names {
		results[index] = ScenarioResult{
			Name:    name,
			Surface: "repository",
			Samples: make([]Sample, 1),
		}
	}
	return results
}

func buildColdCLIFixture(
	ctx context.Context,
	dependencies scenarioDependencies,
	spec FixtureSpec,
	sampleRoot string,
	group string,
) (Fixture, coldCLITasks, error) {
	root := filepath.Join(sampleRoot, group)
	fixture, err := dependencies.buildFixture(ctx, root, spec)
	if err != nil {
		return Fixture{}, coldCLITasks{}, fmt.Errorf("build %s fixture: %w", group, err)
	}
	tasks, err := allocateColdCLITasks(fixture.ActiveTaskIDs)
	if err != nil {
		return Fixture{}, coldCLITasks{}, fmt.Errorf("allocate %s fixture: %w", group, err)
	}
	return fixture, tasks, nil
}

func allocateColdCLITasks(taskIDs []string) (coldCLITasks, error) {
	if len(taskIDs) < coldCLITasksPerFixture {
		return coldCLITasks{}, fmt.Errorf("fixture has %d tasks, need %d for cold CLI scenarios", len(taskIDs), coldCLITasksPerFixture)
	}
	return coldCLITasks{
		update:      taskIDs[0],
		delete:      taskIDs[1],
		moveAnchor:  taskIDs[2],
		move:        taskIDs[3],
		dependent:   taskIDs[4],
		dependency:  taskIDs[5],
		sameBurst:   taskIDs[6],
		independent: append([]string(nil), taskIDs[:10]...),
	}, nil
}

func allocateWarmHTTPTasks(taskIDs []string) (warmHTTPTasks, error) {
	if len(taskIDs) < warmHTTPTasksPerFixture {
		return warmHTTPTasks{}, fmt.Errorf("fixture has %d tasks, need %d for warm HTTP scenarios", len(taskIDs), warmHTTPTasksPerFixture)
	}
	return warmHTTPTasks{
		update:      taskIDs[0],
		sameBurst:   taskIDs[1],
		independent: append([]string(nil), taskIDs[:10]...),
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

func firstTaskID(refs []byte) (string, error) {
	firstLine, _, _ := bytes.Cut(refs, []byte{'\n'})
	ref, _, found := bytes.Cut(firstLine, []byte{0})
	if !found {
		return "", fmt.Errorf("task ref enumeration did not include an object name")
	}
	taskID := strings.TrimPrefix(string(ref), "refs/workbook/tasks/")
	if taskID == "" || taskID == string(ref) {
		return "", fmt.Errorf("task ref enumeration did not include a task ref")
	}
	return taskID, nil
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

func requireSuccessfulSample(name string, sample Sample) error {
	if sampleSucceeded(sample) {
		return nil
	}
	if sample.TimedOut {
		return fmt.Errorf("%s timed out: %s", name, sample.Error)
	}
	if sample.Error != "" {
		return fmt.Errorf("%s failed with exit code %d: %s", name, sample.ExitCode, sample.Error)
	}
	return fmt.Errorf("%s failed with exit code %d", name, sample.ExitCode)
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
