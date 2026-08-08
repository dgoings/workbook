package perf

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	// WatcherReportFormat and WatcherReportVersion version the descriptive
	// watcher block, which is not a scenario table and carries no budget.
	WatcherReportFormat  = "workbook.watcher-steady-state"
	WatcherReportVersion = 1

	// WatcherScenario is the registry selector for the steady-state family.
	WatcherScenario = "watch-steady-state"

	watcherIdleWindow   = "idle-control"
	watcherSteadyWindow = "steady-interval"

	// watcherSteadyInterval is the product's own default watcher interval, so
	// the steady window measures what a beta user actually leaves running
	// rather than a synthetic rate chosen to make the number look good.
	watcherSteadyInterval = 5 * time.Second

	// watcherIdleInterval is far longer than the observation window, so the
	// control window schedules no tick at all. It prices the watcher's fixed
	// costs: the opening synchronization, the shutdown synchronization, and
	// simply having the process alive.
	watcherIdleInterval = time.Hour

	// watcherObservationWindow is twelve steady intervals plus a half-interval
	// margin, so the scheduled tick count is unambiguous even when a tick runs
	// long. Both windows use the same wall-clock length, which is what lets the
	// control subtract cleanly.
	watcherObservationWindow = 62500 * time.Millisecond

	// watcherFetchCommand is the Git subcommand that defines one
	// synchronization. Repository.Sync runs exactly one fetch per call, so
	// counting it counts ticks without the harness assuming how many other Git
	// processes a tick spawns.
	watcherFetchCommand = "fetch"
)

// WatcherWindow records one bounded observation of a live `workbook sync
// --watch` process. Every field is descriptive; the family has no target.
type WatcherWindow struct {
	Name                 string  `json:"name"`
	IntervalMilliseconds int64   `json:"intervalMilliseconds"`
	ObservedMilliseconds float64 `json:"observedMilliseconds"`

	// Synchronizations counts the `git fetch` invocations recorded inside the
	// window, which is one per completed synchronization. It includes the
	// shutdown synchronization that follows the interrupt, because that work is
	// inside the observed process lifetime; the idle control counts the same
	// fixed synchronization and subtracts it.
	Synchronizations int `json:"synchronizations"`
	GitProcesses     int `json:"gitProcesses"`

	UserMilliseconds    float64 `json:"userMilliseconds"`
	SystemMilliseconds  float64 `json:"systemMilliseconds"`
	CPUMilliseconds     float64 `json:"cpuMilliseconds"`
	CPUPercentOfOneCore float64 `json:"cpuPercentOfOneCore"`

	// MaxResidentBytes is the process's whole-lifetime peak, so it also covers
	// the untimed startup synchronization that precedes the window.
	MaxResidentBytes   int64  `json:"maxResidentBytes"`
	MaxResidentRaw     int64  `json:"maxResidentRaw"`
	MaxResidentRawUnit string `json:"maxResidentRawUnit"`

	MinorPageFaults            int64 `json:"minorPageFaults"`
	MajorPageFaults            int64 `json:"majorPageFaults"`
	VoluntaryContextSwitches   int64 `json:"voluntaryContextSwitches"`
	InvoluntaryContextSwitches int64 `json:"involuntaryContextSwitches"`

	// RepositoryBytesDelta is the change in on-disk bytes under the repository
	// root across the window, sampled outside the CPU accounting. It answers
	// whether an idle daemon writes durable bytes every tick.
	RepositoryBytesDelta int64 `json:"repositoryBytesDelta"`
}

// WatcherTickCost is the marginal cost of one scheduled synchronization with
// nothing pending, derived by subtracting the idle control from the steady
// window.
type WatcherTickCost struct {
	Synchronizations                  int     `json:"synchronizations"`
	CPUMillisecondsPerSynchronization float64 `json:"cpuMillisecondsPerSynchronization"`
	GitProcessesPerSynchronization    float64 `json:"gitProcessesPerSynchronization"`
	RepositoryBytesPerSynchronization float64 `json:"repositoryBytesPerSynchronization"`
	MaxResidentByteDelta              int64   `json:"maxResidentByteDelta"`
	Description                       string  `json:"description"`
}

// WatcherSteadyStateReport is the descriptive record of the daemon a beta user
// leaves running for hours.
type WatcherSteadyStateReport struct {
	Format             string          `json:"format"`
	Version            int             `json:"version"`
	Platform           string          `json:"platform"`
	MaxResidentRawUnit string          `json:"maxResidentRawUnit"`
	Fixture            FixtureSpec     `json:"fixture"`
	Observations       int             `json:"observations"`
	WindowMilliseconds int64           `json:"windowMilliseconds"`
	Idle               []WatcherWindow `json:"idle"`
	Steady             []WatcherWindow `json:"steady"`
	PerSynchronization WatcherTickCost `json:"perSynchronization"`
}

type watcherWindowSpec struct {
	Binary    string
	Directory string
	Name      string
	Interval  time.Duration
	Window    time.Duration
	Timeout   time.Duration
}

type watcherDependencies struct {
	buildFixture  func(context.Context, string, FixtureSpec) (Fixture, error)
	publishOrigin func(context.Context, time.Duration, string) error
	observeWindow func(context.Context, watcherWindowSpec) (WatcherWindow, error)
}

// RunWatcherSteadyState observes a live `workbook sync --watch` against an
// already-synchronized origin with nothing pending, which is the state a beta
// user's watcher spends nearly all of its life in.
func RunWatcherSteadyState(ctx context.Context, spec RunSpec, root string) (WatcherSteadyStateReport, error) {
	return runWatcherSteadyState(ctx, spec, root, watcherDependencies{
		buildFixture: func(ctx context.Context, root string, fixture FixtureSpec) (Fixture, error) {
			return buildFixtureWithinTimeout(ctx, root, fixture, spec.CommandTimeout)
		},
		publishOrigin: publishFixtureToLocalOrigin,
		observeWindow: observeWatcherWindow,
	})
}

// runWatcherSteadyState builds one fixture and reuses it for every observation.
// Nothing in this family mutates the repository — the point is a daemon with no
// work to do — so a fresh fixture per observation would only add minutes of
// untimed setup without changing what is measured.
func runWatcherSteadyState(ctx context.Context, spec RunSpec, root string, dependencies watcherDependencies) (WatcherSteadyStateReport, error) {
	if spec.WorkbookBinary == "" {
		return WatcherSteadyStateReport{}, fmt.Errorf("workbook binary is required")
	}
	if spec.Samples < 1 {
		return WatcherSteadyStateReport{}, fmt.Errorf("samples must be positive")
	}
	if spec.CommandTimeout <= 0 {
		return WatcherSteadyStateReport{}, fmt.Errorf("command timeout must be positive")
	}
	if root == "" {
		return WatcherSteadyStateReport{}, fmt.Errorf("fixture root is required")
	}
	if dependencies.buildFixture == nil || dependencies.publishOrigin == nil || dependencies.observeWindow == nil {
		return WatcherSteadyStateReport{}, fmt.Errorf("watcher scenario dependencies are required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return WatcherSteadyStateReport{}, fmt.Errorf("create watcher fixture root: %w", err)
	}

	fixture, err := dependencies.buildFixture(ctx, filepath.Join(root, "watcher"), spec.Fixture)
	if err != nil {
		return WatcherSteadyStateReport{}, fmt.Errorf("build watcher fixture: %w", err)
	}
	if err := dependencies.publishOrigin(ctx, spec.CommandTimeout, fixture.Root); err != nil {
		return WatcherSteadyStateReport{}, fmt.Errorf("publish watcher fixture: %w", err)
	}

	report := WatcherSteadyStateReport{
		Format:             WatcherReportFormat,
		Version:            WatcherReportVersion,
		Platform:           runtime.GOOS + "/" + runtime.GOARCH,
		MaxResidentRawUnit: MaxResidentUnitForOS(runtime.GOOS),
		Fixture:            spec.Fixture,
		Observations:       spec.Samples,
		WindowMilliseconds: watcherObservationWindow.Milliseconds(),
	}
	// The control runs first in every observation so a steady window is never
	// the first thing a cold page cache sees.
	windows := []struct {
		name     string
		interval time.Duration
	}{
		{name: watcherIdleWindow, interval: watcherIdleInterval},
		{name: watcherSteadyWindow, interval: watcherSteadyInterval},
	}
	for range spec.Samples {
		for _, window := range windows {
			observed, err := dependencies.observeWindow(ctx, watcherWindowSpec{
				Binary:    spec.WorkbookBinary,
				Directory: fixture.Root,
				Name:      window.name,
				Interval:  window.interval,
				Window:    watcherObservationWindow,
				Timeout:   spec.CommandTimeout,
			})
			if err != nil {
				return WatcherSteadyStateReport{}, fmt.Errorf("observe %s watcher window: %w", window.name, err)
			}
			if window.name == watcherIdleWindow {
				report.Idle = append(report.Idle, observed)
				continue
			}
			report.Steady = append(report.Steady, observed)
		}
	}

	cost, err := watcherTickCost(report.Idle, report.Steady)
	if err != nil {
		return WatcherSteadyStateReport{}, err
	}
	report.PerSynchronization = cost
	return report, nil
}

// watcherTickCost subtracts the idle control from the steady window. Both
// windows pay for process startup, the opening synchronization, and the
// shutdown synchronization, so the difference is the marginal cost of the
// scheduled ticks and nothing else.
func watcherTickCost(idle, steady []WatcherWindow) (WatcherTickCost, error) {
	if len(idle) == 0 || len(steady) == 0 {
		return WatcherTickCost{}, fmt.Errorf("watcher tick cost needs both an idle control and a steady window")
	}
	idleSyncs := medianInt(watcherSynchronizations(idle))
	steadySyncs := medianInt(watcherSynchronizations(steady))
	marginal := steadySyncs - idleSyncs
	if marginal < 1 {
		return WatcherTickCost{}, fmt.Errorf(
			"steady window recorded %d synchronizations against %d in the idle control: no scheduled synchronization was observed",
			steadySyncs, idleSyncs,
		)
	}

	idleCPU := medianFloat(watcherFloats(idle, func(w WatcherWindow) float64 { return w.CPUMilliseconds }))
	steadyCPU := medianFloat(watcherFloats(steady, func(w WatcherWindow) float64 { return w.CPUMilliseconds }))
	idleProcesses := medianFloat(watcherFloats(idle, func(w WatcherWindow) float64 { return float64(w.GitProcesses) }))
	steadyProcesses := medianFloat(watcherFloats(steady, func(w WatcherWindow) float64 { return float64(w.GitProcesses) }))
	idleBytes := medianFloat(watcherFloats(idle, func(w WatcherWindow) float64 { return float64(w.RepositoryBytesDelta) }))
	steadyBytes := medianFloat(watcherFloats(steady, func(w WatcherWindow) float64 { return float64(w.RepositoryBytesDelta) }))
	idleResident := medianInt(watcherResident(idle))
	steadyResident := medianInt(watcherResident(steady))

	divisor := float64(marginal)
	return WatcherTickCost{
		Synchronizations:                  marginal,
		CPUMillisecondsPerSynchronization: (steadyCPU - idleCPU) / divisor,
		GitProcessesPerSynchronization:    (steadyProcesses - idleProcesses) / divisor,
		RepositoryBytesPerSynchronization: (steadyBytes - idleBytes) / divisor,
		MaxResidentByteDelta:              int64(steadyResident - idleResident),
		Description: fmt.Sprintf(
			"%d additional synchronizations over a %s window at a %s interval, against an idle control at a %s interval",
			marginal, watcherObservationWindow, watcherSteadyInterval, watcherIdleInterval,
		),
	}, nil
}

func watcherSynchronizations(windows []WatcherWindow) []int {
	values := make([]int, len(windows))
	for index, window := range windows {
		values[index] = window.Synchronizations
	}
	return values
}

func watcherResident(windows []WatcherWindow) []int {
	values := make([]int, len(windows))
	for index, window := range windows {
		values[index] = int(window.MaxResidentBytes)
	}
	return values
}

func watcherFloats(windows []WatcherWindow, read func(WatcherWindow) float64) []float64 {
	values := make([]float64, len(windows))
	for index, window := range windows {
		values[index] = read(window)
	}
	return values
}

func medianInt(values []int) int {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}

func medianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return median(sorted)
}

// observeWatcherWindow starts one watcher, lets it run for a bounded window,
// interrupts it, and reports the kernel's account of what it cost.
//
// Everything before the window opens is setup: binding the socket, printing the
// readiness line, and completing the opening synchronization. The Trace2 cursor
// opens at the same moment the clock starts, so the counted Git work belongs to
// the window and to the shutdown synchronization that follows it.
func observeWatcherWindow(ctx context.Context, spec watcherWindowSpec) (WatcherWindow, error) {
	if spec.Window <= 0 {
		return WatcherWindow{}, fmt.Errorf("watcher observation window must be positive")
	}
	traceFile, err := os.CreateTemp("", "workbook-watcher-git-trace-*.json")
	if err != nil {
		return WatcherWindow{}, fmt.Errorf("create watcher Trace2 event file: %w", err)
	}
	tracePath := traceFile.Name()
	if err := traceFile.Close(); err != nil {
		os.Remove(tracePath)
		return WatcherWindow{}, fmt.Errorf("close watcher Trace2 event file: %w", err)
	}
	defer os.Remove(tracePath)

	command := exec.Command(spec.Binary, "sync", "--watch", "--interval", spec.Interval.String())
	command.Dir = spec.Directory
	command.Env = append(os.Environ(), "GIT_TRACE2_EVENT="+tracePath)
	command.Stdout = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := command.StderrPipe()
	if err != nil {
		return WatcherWindow{}, fmt.Errorf("open watcher stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return WatcherWindow{}, fmt.Errorf("start watcher: %w", err)
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

	startupTimer := time.NewTimer(spec.Timeout)
	defer startupTimer.Stop()
	select {
	case <-ready:
	case err := <-wait:
		return WatcherWindow{}, fmt.Errorf("watcher exited before readiness: %w", err)
	case <-ctx.Done():
		watcher.stop()
		return WatcherWindow{}, ctx.Err()
	case <-startupTimer.C:
		watcher.stop()
		return WatcherWindow{}, fmt.Errorf("watcher did not report readiness within %s", spec.Timeout)
	}
	if err := waitForWatcherSync(ctx, spec.Binary, spec.Directory, spec.Timeout); err != nil {
		watcher.stop()
		return WatcherWindow{}, err
	}

	cursor, err := OpenTraceCursor(tracePath)
	if err != nil {
		watcher.stop()
		return WatcherWindow{}, err
	}
	bytesBefore, err := directoryBytes(spec.Directory)
	if err != nil {
		watcher.stop()
		return WatcherWindow{}, err
	}

	startedAt := time.Now()
	windowTimer := time.NewTimer(spec.Window)
	defer windowTimer.Stop()
	select {
	case <-windowTimer.C:
	case err := <-wait:
		return WatcherWindow{}, fmt.Errorf("watcher exited during the observation window: %w", err)
	case <-ctx.Done():
		watcher.stop()
		return WatcherWindow{}, ctx.Err()
	}
	observed := time.Since(startedAt)

	// A window is only evidence about a watcher that was still trustworthy when
	// it closed. This probe is the one status request inside the window, and
	// both windows pay for exactly one.
	if err := waitForWatcherSync(ctx, spec.Binary, spec.Directory, spec.Timeout); err != nil {
		watcher.stop()
		return WatcherWindow{}, fmt.Errorf("watcher was not synchronized when the window closed: %w", err)
	}

	if err := interruptWarmServer(command); err != nil {
		watcher.stop()
		return WatcherWindow{}, fmt.Errorf("interrupt watcher: %w", err)
	}
	shutdownTimer := time.NewTimer(spec.Timeout)
	defer shutdownTimer.Stop()
	select {
	case err := <-wait:
		if err != nil {
			return WatcherWindow{}, fmt.Errorf("watcher did not exit cleanly: %w", err)
		}
	case <-shutdownTimer.C:
		terminateWarmServer(command)
		<-wait
		return WatcherWindow{}, fmt.Errorf("watcher did not exit within %s", spec.Timeout)
	}

	bytesAfter, err := directoryBytes(spec.Directory)
	if err != nil {
		return WatcherWindow{}, err
	}
	counts, err := cursor.CountNew()
	if err != nil {
		return WatcherWindow{}, err
	}

	window := WatcherWindow{
		Name:                 spec.Name,
		IntervalMilliseconds: spec.Interval.Milliseconds(),
		ObservedMilliseconds: durationAsMilliseconds(observed),
		Synchronizations:     counts.Commands[watcherFetchCommand],
		GitProcesses:         counts.GitProcesses,
		MaxResidentRawUnit:   MaxResidentUnitForOS(runtime.GOOS),
		RepositoryBytesDelta: bytesAfter - bytesBefore,
	}
	state := command.ProcessState
	if state == nil {
		return WatcherWindow{}, fmt.Errorf("watcher reported no process state")
	}
	window.UserMilliseconds = durationAsMilliseconds(state.UserTime())
	window.SystemMilliseconds = durationAsMilliseconds(state.SystemTime())
	window.CPUMilliseconds = window.UserMilliseconds + window.SystemMilliseconds
	if observed > 0 {
		window.CPUPercentOfOneCore = 100 * window.CPUMilliseconds / durationAsMilliseconds(observed)
	}
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || usage == nil {
		return WatcherWindow{}, fmt.Errorf("watcher reported no resource usage")
	}
	window.MaxResidentRaw = int64(usage.Maxrss)
	window.MaxResidentBytes = maxResidentBytes(window.MaxResidentRaw, window.MaxResidentRawUnit)
	window.MinorPageFaults = int64(usage.Minflt)
	window.MajorPageFaults = int64(usage.Majflt)
	window.VoluntaryContextSwitches = int64(usage.Nvcsw)
	window.InvoluntaryContextSwitches = int64(usage.Nivcsw)
	return window, nil
}

// writeWatcherMarkdown renders the descriptive watcher section. Runs that did
// not observe a watcher write nothing.
func writeWatcherMarkdown(w io.Writer, report *WatcherSteadyStateReport) error {
	if report == nil {
		return nil
	}
	if _, err := fmt.Fprintf(w, "\n## Sync watcher steady state\n\nPlatform: %s. Peak resident unit: %s. Observations: %d of %s each.\n\n",
		report.Platform, report.MaxResidentRawUnit, report.Observations, watcherObservationWindow); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| Window | Interval (ms) | Observed (ms) | Synchronizations | Git processes | CPU (ms) | CPU (% of one core) | Peak resident (bytes) | Major faults | Repository bytes delta |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |"); err != nil {
		return err
	}
	for _, window := range append(append([]WatcherWindow(nil), report.Idle...), report.Steady...) {
		if _, err := fmt.Fprintf(w, "| %s | %d | %.2f | %d | %d | %.2f | %.2f | %d | %d | %d |\n",
			window.Name, window.IntervalMilliseconds, window.ObservedMilliseconds, window.Synchronizations,
			window.GitProcesses, window.CPUMilliseconds, window.CPUPercentOfOneCore,
			window.MaxResidentBytes, window.MajorPageFaults, window.RepositoryBytesDelta); err != nil {
			return err
		}
	}
	cost := report.PerSynchronization
	_, err := fmt.Fprintf(w,
		"\nPer synchronization with nothing pending: %.2f ms CPU, %.2f Git processes, %.0f repository bytes. "+
			"Peak resident set moved %d bytes between the control and the steady window. %s.\n",
		cost.CPUMillisecondsPerSynchronization, cost.GitProcessesPerSynchronization,
		cost.RepositoryBytesPerSynchronization, cost.MaxResidentByteDelta, cost.Description)
	return err
}
