package perf

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func appendTraceLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, line := range lines {
		if _, err := file.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRunWatcherSteadyStateObservesAnIdleControlBesideTheRealisticInterval pins
// the shape of the measurement. A single window cannot separate the cost of a
// synchronization tick from the cost of merely having the daemon alive, because
// the watcher synchronizes once at startup and once at shutdown whatever its
// interval is. The idle control window prices those two fixed synchronizations
// and the process itself, so subtracting it leaves the marginal per-tick cost.
//
// Mutation witness: dropping the control window, or giving both windows the same
// interval, would make the reported per-tick cost include startup and shutdown
// work that no scheduled tick performs.
func TestRunWatcherSteadyStateObservesAnIdleControlBesideTheRealisticInterval(t *testing.T) {
	var observed []watcherWindowSpec
	var fixtureRoots []string
	publishedRoots := []string{}
	dependencies := watcherDependencies{
		buildFixture: func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
			fixtureRoots = append(fixtureRoots, root)
			return Fixture{Root: root, TaskIDs: []string{"WB-00"}, ActiveTaskIDs: []string{"WB-00"}}, nil
		},
		publishOrigin: func(_ context.Context, _ time.Duration, root string) error {
			publishedRoots = append(publishedRoots, root)
			return nil
		},
		observeWindow: func(_ context.Context, spec watcherWindowSpec) (WatcherWindow, error) {
			observed = append(observed, spec)
			window := WatcherWindow{
				Name:                 spec.Name,
				IntervalMilliseconds: spec.Interval.Milliseconds(),
				ObservedMilliseconds: durationAsMilliseconds(spec.Window),
				Synchronizations:     1,
				GitProcesses:         8,
				CPUMilliseconds:      100,
				MaxResidentBytes:     20_000_000,
			}
			if spec.Name == watcherSteadyWindow {
				window.Synchronizations = 13
				window.GitProcesses = 104
				window.CPUMilliseconds = 1300
				window.MaxResidentBytes = 26_000_000
			}
			return window, nil
		},
	}
	spec := RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        FixtureSpec{TotalTasks: 11, ActiveTasks: 10, TombstonedTasks: 1, OperationsPerTask: 4, ObjectFormat: "sha1"},
		Samples:        2,
		CommandTimeout: time.Minute,
	}
	root := t.TempDir()

	report, err := runWatcherSteadyState(context.Background(), spec, root, dependencies)
	if err != nil {
		t.Fatal(err)
	}

	if len(fixtureRoots) != 1 {
		t.Fatalf("fixture builds = %d, want one fixture shared by every observation", len(fixtureRoots))
	}
	if want := []string{filepath.Join(root, "watcher")}; !reflect.DeepEqual(fixtureRoots, want) {
		t.Fatalf("fixture roots = %#v, want %#v", fixtureRoots, want)
	}
	if !reflect.DeepEqual(publishedRoots, fixtureRoots) {
		t.Fatalf("published origins = %#v, want the fixture published exactly once %#v", publishedRoots, fixtureRoots)
	}

	wantNames := []string{
		watcherIdleWindow, watcherSteadyWindow,
		watcherIdleWindow, watcherSteadyWindow,
	}
	gotNames := make([]string, len(observed))
	for index, window := range observed {
		gotNames[index] = window.Name
		if window.Directory != fixtureRoots[0] {
			t.Fatalf("observation %d directory = %q, want the published fixture %q", index, window.Directory, fixtureRoots[0])
		}
		if window.Window != watcherObservationWindow {
			t.Fatalf("observation %d window = %s, want %s", index, window.Window, watcherObservationWindow)
		}
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("observation names = %#v, want %#v", gotNames, wantNames)
	}
	if observed[0].Interval != watcherIdleInterval || observed[1].Interval != watcherSteadyInterval {
		t.Fatalf("observation intervals = %s and %s, want %s control and %s steady", observed[0].Interval, observed[1].Interval, watcherIdleInterval, watcherSteadyInterval)
	}

	if report.Format != WatcherReportFormat || report.Version != WatcherReportVersion {
		t.Fatalf("report = %q v%d, want %q v%d", report.Format, report.Version, WatcherReportFormat, WatcherReportVersion)
	}
	if report.Observations != 2 || len(report.Idle) != 2 || len(report.Steady) != 2 {
		t.Fatalf("report observations = %d with %d idle and %d steady windows, want 2 of each", report.Observations, len(report.Idle), len(report.Steady))
	}
	if report.WindowMilliseconds != watcherObservationWindow.Milliseconds() {
		t.Fatalf("report window = %d ms, want %d ms", report.WindowMilliseconds, watcherObservationWindow.Milliseconds())
	}
	if report.Fixture != spec.Fixture {
		t.Fatalf("report fixture = %#v, want %#v", report.Fixture, spec.Fixture)
	}

	cost := report.PerSynchronization
	if cost.Synchronizations != 12 {
		t.Fatalf("marginal synchronizations = %d, want 13 steady less 1 control", cost.Synchronizations)
	}
	if cost.CPUMillisecondsPerSynchronization != 100 {
		t.Fatalf("CPU per synchronization = %.2f ms, want (1300-100)/12", cost.CPUMillisecondsPerSynchronization)
	}
	if cost.GitProcessesPerSynchronization != 8 {
		t.Fatalf("Git processes per synchronization = %.2f, want (104-8)/12", cost.GitProcessesPerSynchronization)
	}
	if cost.MaxResidentByteDelta != 6_000_000 {
		t.Fatalf("peak resident delta = %d bytes, want 6000000", cost.MaxResidentByteDelta)
	}
	if !strings.Contains(cost.Description, watcherSteadyInterval.String()) ||
		!strings.Contains(cost.Description, watcherIdleInterval.String()) {
		t.Fatalf("per-synchronization description = %q, want both observed intervals named", cost.Description)
	}
}

// TestRunWatcherSteadyStateRejectsAWindowThatScheduledNoTick keeps the harness
// from publishing a per-tick number derived from a division by zero. A run whose
// steady window observed no more synchronizations than the idle control measured
// nothing about ticking, and reporting it would invent evidence.
func TestRunWatcherSteadyStateRejectsAWindowThatScheduledNoTick(t *testing.T) {
	dependencies := watcherDependencies{
		buildFixture: func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
			return Fixture{Root: root, TaskIDs: []string{"WB-00"}, ActiveTaskIDs: []string{"WB-00"}}, nil
		},
		publishOrigin: func(context.Context, time.Duration, string) error { return nil },
		observeWindow: func(_ context.Context, spec watcherWindowSpec) (WatcherWindow, error) {
			return WatcherWindow{Name: spec.Name, Synchronizations: 1, CPUMilliseconds: 100}, nil
		},
	}
	spec := RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        FixtureSpec{TotalTasks: 11, ActiveTasks: 10, TombstonedTasks: 1, OperationsPerTask: 4, ObjectFormat: "sha1"},
		Samples:        1,
		CommandTimeout: time.Minute,
	}

	_, err := runWatcherSteadyState(context.Background(), spec, t.TempDir(), dependencies)
	if err == nil || !strings.Contains(err.Error(), "no scheduled synchronization") {
		t.Fatalf("error = %v, want a refusal to derive a per-tick cost", err)
	}
}

// TestRunWatcherSteadyStatePropagatesObservationFailures keeps a partial
// observation from becoming a published number.
func TestRunWatcherSteadyStatePropagatesObservationFailures(t *testing.T) {
	dependencies := watcherDependencies{
		buildFixture: func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
			return Fixture{Root: root, TaskIDs: []string{"WB-00"}, ActiveTaskIDs: []string{"WB-00"}}, nil
		},
		publishOrigin: func(context.Context, time.Duration, string) error { return nil },
		observeWindow: func(context.Context, watcherWindowSpec) (WatcherWindow, error) {
			return WatcherWindow{}, errors.New("watcher exited during the observation window")
		},
	}
	spec := RunSpec{
		WorkbookBinary: "workbook",
		Fixture:        FixtureSpec{TotalTasks: 11, ActiveTasks: 10, TombstonedTasks: 1, OperationsPerTask: 4, ObjectFormat: "sha1"},
		Samples:        1,
		CommandTimeout: time.Minute,
	}

	if _, err := runWatcherSteadyState(context.Background(), spec, t.TempDir(), dependencies); err == nil ||
		!strings.Contains(err.Error(), "watcher exited during the observation window") {
		t.Fatalf("error = %v, want the observation failure preserved", err)
	}
}

// TestObserveWatcherWindowMeasuresARunningDaemon exercises the real product
// daemon over a short window. It is the only test that proves the scenario
// measures a live `workbook sync --watch` rather than a mock: the counted
// synchronizations have to grow with the interval, and the kernel has to report
// CPU and a peak resident set for the process.
//
// It deliberately carries no `-short` guard. `-short` skips exactly three
// build-heavy release and setup tests, and every other live-process test in this
// package runs under it; hiding the one test that distinguishes a real daemon
// from a mock would leave `go test -short ./...` unable to catch a watcher
// observation that measures nothing. Its two 1.5-second windows are the whole
// cost, against a package that already spends minutes building fixtures.
func TestObserveWatcherWindowMeasuresARunningDaemon(t *testing.T) {
	binary := buildWorkbookBinary(t)
	root := filepath.Join(t.TempDir(), "fixture")
	fixture, err := BuildFixture(context.Background(), root, FixtureSpec{
		TotalTasks: 10, ActiveTasks: 9, TombstonedTasks: 1, OperationsPerTask: 2, ObjectFormat: "sha1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := publishFixtureToLocalOrigin(context.Background(), 30*time.Second, fixture.Root); err != nil {
		t.Fatal(err)
	}

	idle, err := observeWatcherWindow(context.Background(), watcherWindowSpec{
		Binary: binary, Directory: fixture.Root, Name: watcherIdleWindow,
		Interval: time.Hour, Window: 1500 * time.Millisecond, Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	steady, err := observeWatcherWindow(context.Background(), watcherWindowSpec{
		Binary: binary, Directory: fixture.Root, Name: watcherSteadyWindow,
		Interval: 250 * time.Millisecond, Window: 1500 * time.Millisecond, Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	if idle.Synchronizations != 1 {
		t.Fatalf("idle synchronizations = %d, want only the shutdown synchronization", idle.Synchronizations)
	}
	if steady.Synchronizations <= idle.Synchronizations {
		t.Fatalf("steady synchronizations = %d, want more than the idle control's %d", steady.Synchronizations, idle.Synchronizations)
	}
	for _, window := range []WatcherWindow{idle, steady} {
		if window.MaxResidentBytes <= 0 {
			t.Fatalf("%s peak resident bytes = %d, want a kernel-reported peak", window.Name, window.MaxResidentBytes)
		}
		if window.CPUMilliseconds <= 0 {
			t.Fatalf("%s CPU = %.2f ms, want kernel-reported CPU time", window.Name, window.CPUMilliseconds)
		}
		if window.ObservedMilliseconds < 1500 {
			t.Fatalf("%s observed = %.2f ms, want at least the requested window", window.Name, window.ObservedMilliseconds)
		}
		if window.MaxResidentRawUnit != MaxResidentUnitForOS(runtime.GOOS) {
			t.Fatalf("%s peak resident unit = %q, want this platform's unit", window.Name, window.MaxResidentRawUnit)
		}
		if window.GitProcesses < window.Synchronizations {
			t.Fatalf("%s Git processes = %d, want at least one per synchronization", window.Name, window.GitProcesses)
		}
	}
}

// TestReportCarriesTheWatcherBlockInBothOutputs keeps the steady-state evidence
// machine-readable and readable. The family measures a daemon over time rather
// than a command's latency, so it deliberately contributes no row to the
// scenario table and needs its own section instead.
func TestReportCarriesTheWatcherBlockInBothOutputs(t *testing.T) {
	watcher := &WatcherSteadyStateReport{
		Format:             WatcherReportFormat,
		Version:            WatcherReportVersion,
		Platform:           "darwin/arm64",
		MaxResidentRawUnit: MaxResidentUnitBytes,
		Observations:       1,
		WindowMilliseconds: watcherObservationWindow.Milliseconds(),
		Idle: []WatcherWindow{{
			Name: watcherIdleWindow, IntervalMilliseconds: 3_600_000, ObservedMilliseconds: 62500,
			Synchronizations: 1, GitProcesses: 8, CPUMilliseconds: 120, MaxResidentBytes: 21_000_000,
		}},
		Steady: []WatcherWindow{{
			Name: watcherSteadyWindow, IntervalMilliseconds: 5000, ObservedMilliseconds: 62500,
			Synchronizations: 13, GitProcesses: 104, CPUMilliseconds: 1320, MaxResidentBytes: 27_000_000,
		}},
		PerSynchronization: WatcherTickCost{
			Synchronizations: 12, CPUMillisecondsPerSynchronization: 100,
			GitProcessesPerSynchronization: 8, MaxResidentByteDelta: 6_000_000,
			Description: "twelve additional synchronizations",
		},
	}
	report := Report{
		Format: ReportFormat, Version: ReportVersion, Phase: "acceptance",
		Scenarios: []ScenarioResult{}, WatcherSteadyState: watcher,
	}

	var encoded strings.Builder
	if err := report.WriteJSON(&encoded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded.String(), `"watcherSteadyState"`) ||
		!strings.Contains(encoded.String(), WatcherReportFormat) {
		t.Fatalf("JSON report = %s, want a versioned watcherSteadyState block", encoded.String())
	}

	var rendered strings.Builder
	if err := report.WriteMarkdown(&rendered); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Sync watcher steady state",
		watcherIdleWindow,
		watcherSteadyWindow,
		"Per synchronization with nothing pending",
	} {
		if !strings.Contains(rendered.String(), want) {
			t.Fatalf("Markdown report is missing %q:\n%s", want, rendered.String())
		}
	}

	var withoutWatcher strings.Builder
	if err := (Report{Format: ReportFormat, Version: ReportVersion, Scenarios: []ScenarioResult{}}).WriteMarkdown(&withoutWatcher); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(withoutWatcher.String(), "Sync watcher steady state") {
		t.Fatal("a run that observed no watcher rendered a watcher section")
	}
}

// TestTraceCursorCountsGitSubcommands pins the counter the watcher window uses
// to price ticks. A synchronization performs exactly one `git fetch`, so the
// subcommand tally is what turns a stream of Git processes into a tick count.
func TestTraceCursorCountsGitSubcommands(t *testing.T) {
	path := emptyTraceFile(t)
	appendTraceLines(t, path,
		`{"event":"start","argv":["git","fetch","--no-tags","origin"]}`,
		`{"event":"cmd_name","name":"fetch","hierarchy":"fetch"}`,
		`{"event":"start","argv":["git","push","origin"]}`,
		`{"event":"cmd_name","name":"push","hierarchy":"push"}`,
		`{"event":"exit"}`,
	)
	cursor, err := OpenTraceCursor(path)
	if err != nil {
		t.Fatal(err)
	}
	// The cursor opened after the existing contents, so nothing counts yet.
	counts, err := cursor.CountNew()
	if err != nil {
		t.Fatal(err)
	}
	if counts.GitProcesses != 0 || len(counts.Commands) != 0 {
		t.Fatalf("counts before new events = %#v, want empty", counts)
	}

	appendTraceLines(t, path,
		`{"event":"start","argv":["git","fetch","--no-tags","origin"]}`,
		`{"event":"cmd_name","name":"fetch","hierarchy":"fetch"}`,
		`{"event":"start","argv":["git","rev-parse","HEAD"]}`,
		`{"event":"cmd_name","name":"rev-parse","hierarchy":"rev-parse"}`,
		`{"event":"start","argv":["git","fetch","--no-tags","origin"]}`,
		`{"event":"cmd_name","name":"fetch","hierarchy":"fetch"}`,
	)
	counts, err = cursor.CountNew()
	if err != nil {
		t.Fatal(err)
	}
	if counts.GitProcesses != 3 {
		t.Fatalf("Git processes = %d, want 3", counts.GitProcesses)
	}
	if counts.Commands["fetch"] != 2 || counts.Commands["rev-parse"] != 1 {
		t.Fatalf("subcommand counts = %#v, want two fetches and one rev-parse", counts.Commands)
	}
}
