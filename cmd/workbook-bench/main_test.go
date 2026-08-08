package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/perf"
)

func TestRunResolvesRelativeWorkbookBinaryAndWritesCompletePerformanceReport(t *testing.T) {
	binaryDirectory, err := os.MkdirTemp(".", ".workbook-bench-relative-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(binaryDirectory) })
	workbookBinary, err := filepath.Abs(filepath.Join(binaryDirectory, "workbook"))
	if err != nil {
		t.Fatal(err)
	}
	buildWorkbookBinaryAt(t, workbookBinary)
	workbookArgument := filepath.Join(".", filepath.Base(binaryDirectory), "workbook")
	outputRoot := t.TempDir()
	jsonPath := filepath.Join(outputRoot, "report.json")
	markdownPath := filepath.Join(outputRoot, "report.md")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{
		"--workbook", workbookArgument,
		"--tasks", "10",
		"--operations", "4",
		"--samples", "1",
		"--timeout", "5s",
		"--object-format", "sha1",
		"--scenario", "cli-create",
		"--output-json", jsonPath,
		"--output-markdown", markdownPath,
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("run exit code = %d, want 0; stdout = %q; stderr = %q", exitCode, stdout.String(), stderr.String())
	}

	jsonData, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read JSON report: %v", err)
	}
	var report perf.Report
	if err := json.Unmarshal(jsonData, &report); err != nil {
		t.Fatalf("decode JSON report: %v", err)
	}
	if report.Format != perf.ReportFormat || report.Version != perf.ReportVersion {
		t.Fatalf("report identity = %q v%d, want %q v%d", report.Format, report.Version, perf.ReportFormat, perf.ReportVersion)
	}
	if report.Phase != "baseline" {
		t.Errorf("report phase = %q, want baseline", report.Phase)
	}
	if report.Fixture != (perf.FixtureSpec{TotalTasks: 10, ActiveTasks: 9, TombstonedTasks: 1, OperationsPerTask: 4, ObjectFormat: "sha1"}) {
		t.Errorf("report fixture = %#v, want ten tasks and four operations using SHA-1", report.Fixture)
	}

	gotScenarios := make([]string, len(report.Scenarios))
	for index, scenario := range report.Scenarios {
		gotScenarios[index] = scenario.Name
	}
	sort.Strings(gotScenarios)
	wantScenarios := []string{"cli-create"}
	if !reflect.DeepEqual(gotScenarios, wantScenarios) {
		t.Fatalf("scenario names = %#v, want %#v", gotScenarios, wantScenarios)
	}

	markdown, err := os.ReadFile(markdownPath)
	if err != nil {
		t.Fatalf("read Markdown report: %v", err)
	}
	if len(markdown) == 0 {
		t.Fatal("Markdown report is empty")
	}
}

// Mutation witness: returning success unconditionally after writing the report
// would make timeout, product-failure, and harness-error evidence look like a
// valid completed invocation. A target miss remains valid acceptance evidence.
func TestRunRetainsLocalMeasurementOutcomesAndReturnsExecutionStatus(t *testing.T) {
	tests := []struct {
		name         string
		scenarioName string
		surface      string
		target       *perf.ScenarioTarget
		sample       perf.Sample
		timeout      string
		wantExitCode int
		wantOutcome  string
	}{
		{
			name:         "timeout exits one",
			scenarioName: "cli-update",
			surface:      "cold-cli",
			target: &perf.ScenarioTarget{
				DurationStatistic:  perf.DurationP95,
				DurationComparison: perf.DurationAtMost,
				MaxMilliseconds:    200,
			},
			sample:       perf.Sample{Duration: time.Second, TimedOut: true},
			timeout:      "1s",
			wantExitCode: failureExitCode,
			wantOutcome:  "timeout",
		},
		{
			name:         "nonzero exit exits one",
			scenarioName: "cli-update",
			surface:      "cold-cli",
			target: &perf.ScenarioTarget{
				DurationStatistic:  perf.DurationP95,
				DurationComparison: perf.DurationAtMost,
				MaxMilliseconds:    200,
			},
			sample:       perf.Sample{Duration: time.Millisecond, ExitCode: 7},
			timeout:      "1s",
			wantExitCode: failureExitCode,
			wantOutcome:  "failed",
		},
		{
			name:         "measurement start error exits one",
			scenarioName: "cli-update",
			surface:      "cold-cli",
			target: &perf.ScenarioTarget{
				DurationStatistic:  perf.DurationP95,
				DurationComparison: perf.DurationAtMost,
				MaxMilliseconds:    200,
			},
			sample:       perf.Sample{Duration: time.Millisecond, Error: "start measured command"},
			timeout:      "1s",
			wantExitCode: failureExitCode,
			wantOutcome:  "failed",
		},
		{
			name:         "valid target miss exits zero",
			scenarioName: "cli-update",
			surface:      "cold-cli",
			target: &perf.ScenarioTarget{
				DurationStatistic:  perf.DurationP95,
				DurationComparison: perf.DurationAtMost,
				MaxMilliseconds:    200,
			},
			sample:       perf.Sample{Duration: 250 * time.Millisecond},
			timeout:      "1s",
			wantExitCode: 0,
			wantOutcome:  "miss",
		},
		{
			name:         "repository failure exits one after reports",
			scenarioName: "sync-initial-local-bare",
			surface:      "repository",
			sample:       perf.Sample{Duration: time.Millisecond, ExitCode: 1, Error: "sync failed"},
			timeout:      "1s",
			wantExitCode: failureExitCode,
			wantOutcome:  "failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := t.TempDir()
			workbookPath := filepath.Join(outputRoot, "workbook")
			writeExecutableScript(t, workbookPath, ":")

			jsonPath := filepath.Join(outputRoot, "report.json")
			markdownPath := filepath.Join(outputRoot, "report.md")
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := runWithBenchmark(context.Background(), []string{
				"--workbook", workbookPath,
				"--tasks", "10",
				"--tombstones", "0",
				"--operations", "2",
				"--samples", "1",
				"--timeout", test.timeout,
				"--scenario", "cli-update",
				"--output-json", jsonPath,
				"--output-markdown", markdownPath,
			}, &stdout, &stderr, func(context.Context, options) (perf.Report, error) {
				return perf.Report{
					Format:  perf.ReportFormat,
					Version: perf.ReportVersion,
					Phase:   "baseline",
					Scenarios: []perf.ScenarioResult{{
						Name:    test.scenarioName,
						Surface: test.surface,
						Target:  test.target,
						Samples: []perf.Sample{test.sample},
					}},
				}, nil
			})

			if exitCode != test.wantExitCode {
				t.Fatalf("exit code = %d, want %d; stdout = %q; stderr = %q", exitCode, test.wantExitCode, stdout.String(), stderr.String())
			}
			jsonReport, err := os.ReadFile(jsonPath)
			if err != nil {
				t.Fatalf("read retained JSON report: %v; exit code = %d; stdout = %q; stderr = %q", err, exitCode, stdout.String(), stderr.String())
			}
			markdownReport, err := os.ReadFile(markdownPath)
			if err != nil {
				t.Fatalf("read retained Markdown report: %v; exit code = %d; stdout = %q; stderr = %q", err, exitCode, stdout.String(), stderr.String())
			}
			if !bytes.Contains(jsonReport, []byte(`"outcome":"`+test.wantOutcome+`"`)) {
				t.Fatalf("JSON report does not retain %q outcome:\n%s", test.wantOutcome, jsonReport)
			}
			if !bytes.Contains(markdownReport, []byte("| "+test.wantOutcome+" |")) {
				t.Fatalf("Markdown report does not retain %q outcome:\n%s", test.wantOutcome, markdownReport)
			}
		})
	}
}

func TestScenarioFlagParsesRepeatedSelectors(t *testing.T) {
	workbookBinary := buildWorkbookBinary(t)
	outputRoot := t.TempDir()
	var stderr bytes.Buffer
	flags, _ := newFlagSet(&stderr)
	if err := flags.Parse([]string{
		"--workbook", workbookBinary,
		"--scenario", "sync-fresh-checkout",
		"--scenario", "cli-update",
		"--output-json", filepath.Join(outputRoot, "report.json"),
		"--output-markdown", filepath.Join(outputRoot, "report.md"),
	}); err != nil {
		t.Fatalf("parse repeated scenario flags: %v", err)
	}
}

func TestRunBenchmarkRunsOnlySelectedRemoteScenario(t *testing.T) {
	report, err := runBenchmark(context.Background(), options{
		workbookBinary: buildWorkbookBinary(t),
		tasks:          10,
		operations:     4,
		samples:        1,
		timeout:        5 * time.Second,
		objectFormat:   "sha1",
		phase:          "baseline",
		scenarios:      []string{"sync-fresh-checkout"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Scenarios) != 1 || report.Scenarios[0].Name != "sync-fresh-checkout" {
		t.Fatalf("remote-only scenarios = %#v, want fresh checkout only", report.Scenarios)
	}
	if report.Scenarios[0].Target == nil || len(report.Scenarios[0].Samples) != 1 {
		t.Fatalf("remote scenario evidence = %#v, want target and sample", report.Scenarios[0])
	}
}

// Mutation witness: filtering warm results after running the whole family
// would attempt the ten-task bursts and reject this valid api-update-only run.
func TestRunBenchmarkRunsOnlySelectedWarmScenario(t *testing.T) {
	report, err := runBenchmark(context.Background(), options{
		workbookBinary: buildWorkbookBinary(t),
		tasks:          1,
		operations:     2,
		samples:        1,
		timeout:        5 * time.Second,
		objectFormat:   "sha1",
		phase:          "baseline",
		scenarios:      []string{"api-update"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Scenarios) != 1 || report.Scenarios[0].Name != "api-update" {
		t.Fatalf("warm-only scenarios = %#v, want api-update only", report.Scenarios)
	}
}

// Mutation witness: routing sync-only repository selection through the full
// repository suite runs three unselected projection measurements. Its setup
// update appends an extra operation to the first canonical task before sync.
func TestRunBenchmarkPackedSyncOnlyPreservesRequestedFixtureDepth(t *testing.T) {
	realWorkbook := buildWorkbookBinary(t)
	recordingRoot := t.TempDir()
	recordingWorkbook := filepath.Join(recordingRoot, "workbook")
	commandLog := filepath.Join(recordingRoot, "commands.log")
	t.Setenv("WORKBOOK_REAL_BINARY", realWorkbook)
	t.Setenv("WORKBOOK_DEPTH_LOG", commandLog)
	writeExecutableScript(t, recordingWorkbook, `
printf 'command	%s\n' "$1" >> "$WORKBOOK_DEPTH_LOG"
if [ "$1" != "sync" ]; then
	exec "$WORKBOOK_REAL_BINARY" "$@"
fi

record_depths() {
	phase=$1
	git for-each-ref --format='%(refname)' refs/workbook/tasks/ |
	while IFS= read -r ref; do
		depth=$(git rev-list --count "$ref") || exit $?
		printf 'depth	%s	%s	%s\n' "$phase" "$ref" "$depth" >> "$WORKBOOK_DEPTH_LOG"
	done
}

record_depths before
"$WORKBOOK_REAL_BINARY" "$@"
status=$?
record_depths after
exit "$status"
`)

	report, err := runBenchmark(context.Background(), options{
		workbookBinary: recordingWorkbook,
		tasks:          10,
		tombstones:     0,
		operations:     2,
		samples:        1,
		timeout:        20 * time.Second,
		objectFormat:   "sha1",
		phase:          "baseline",
		scenarios: []string{
			"sync-initial-local-bare",
			"sync-unchanged-local-bare",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	gotScenarios := make([]string, len(report.Scenarios))
	for index := range report.Scenarios {
		gotScenarios[index] = report.Scenarios[index].Name
	}
	wantScenarios := []string{"sync-initial-local-bare", "sync-unchanged-local-bare"}
	if !reflect.DeepEqual(gotScenarios, wantScenarios) {
		t.Errorf("scenario names = %#v, want exactly %#v", gotScenarios, wantScenarios)
	}

	logData, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	syncCall := 0
	depths := make(map[string]map[string]int)
	for _, line := range strings.Split(strings.TrimSpace(string(logData)), "\n") {
		fields := strings.Split(line, "\t")
		switch {
		case len(fields) == 2 && fields[0] == "command":
			commands = append(commands, fields[1])
			if fields[1] == "sync" {
				syncCall++
			}
		case len(fields) == 4 && fields[0] == "depth":
			depth, err := strconv.Atoi(fields[3])
			if err != nil {
				t.Fatalf("parse recorded depth line %q: %v", line, err)
			}
			key := fmt.Sprintf("%s-sync-%d", fields[1], syncCall)
			if depths[key] == nil {
				depths[key] = make(map[string]int)
			}
			depths[key][fields[2]] = depth
		default:
			t.Fatalf("unrecognized recording line %q", line)
		}
	}
	if want := []string{"version", "sync", "sync"}; !reflect.DeepEqual(commands, want) {
		t.Errorf("Workbook commands = %#v, want sync-only execution %#v", commands, want)
	}
	for syncIndex := 1; syncIndex <= 2; syncIndex++ {
		for _, phase := range []string{"before", "after"} {
			key := fmt.Sprintf("%s-sync-%d", phase, syncIndex)
			if len(depths[key]) != 10 {
				t.Errorf("%s canonical task refs = %d, want 10", key, len(depths[key]))
			}
			for ref, depth := range depths[key] {
				if depth != 2 {
					t.Errorf("%s %s ancestry depth = %d, want requested depth 2", key, ref, depth)
				}
			}
		}
	}
}

// Mutation witness: dispatching every scenario family after selector
// resolution would construct unrelated cold, warm, or remote fixtures.
func TestBenchmarkMainRunsOnlySelectedValidationScenarios(t *testing.T) {
	workbook := buildWorkbookBinary(t)
	for _, test := range []struct {
		name       string
		tasks      string
		operations string
		wantErr    bool
	}{
		{name: "task count below minimum", tasks: "499", operations: "20", wantErr: true},
		{name: "history depth below minimum", tasks: "500", operations: "19", wantErr: true},
		{name: "exact baseline minimum", tasks: "500", operations: "20"},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := t.TempDir()
			var stderr bytes.Buffer
			flags, options := newFlagSet(&stderr)
			if err := flags.Parse([]string{
				"--workbook", workbook,
				"--tasks", test.tasks,
				"--operations", test.operations,
				"--phase", "baseline",
				"--scenario", "validate-cached-unchanged",
				"--output-json", filepath.Join(outputRoot, "report.json"),
				"--output-markdown", filepath.Join(outputRoot, "report.md"),
			}); err != nil {
				t.Fatal(err)
			}
			err := validateOptions(flags, options)
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), "validation scenarios require at least 500 tasks and 20 operations per task") {
					t.Fatalf("validation minimum error = %v, want exact minimum guidance", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate exact baseline minimum: %v", err)
			}
			if !reflect.DeepEqual(options.scenarios, []string{"validate-cached-unchanged"}) {
				t.Fatalf("selected baseline scenarios = %v, want cached validation only", options.scenarios)
			}
		})
	}

	// The lower-dimensional direct runner is the separate regular diagnostic
	// witness. It must still dispatch only the selected validation path.
	report, err := runBenchmark(context.Background(), options{
		workbookBinary: workbook,
		tasks:          10,
		operations:     4,
		samples:        1,
		timeout:        20 * time.Second,
		objectFormat:   "sha1",
		phase:          "baseline",
		scenarios:      []string{"validate-cached-unchanged"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Scenarios) != 1 || report.Scenarios[0].Name != "validate-cached-unchanged" {
		t.Fatalf("validation-only scenarios = %#v, want cached validation only", report.Scenarios)
	}
}

func TestValidateOptionsRejectsInvalidScenarioSelectors(t *testing.T) {
	workbookBinary := buildWorkbookBinary(t)
	tests := []struct {
		name       string
		selectors  []string
		tasks      string
		operations string
		want       string
	}{
		{
			name:       "duplicate",
			selectors:  []string{"cli-update", "cli-update"},
			tasks:      "10",
			operations: "4",
			want:       "duplicate scenario \"cli-update\"",
		},
		{
			name:       "unknown",
			selectors:  []string{"not-a-scenario"},
			tasks:      "10",
			operations: "4",
			want:       "unknown scenario \"not-a-scenario\"",
		},
		{
			name:       "remote workload below minimum",
			selectors:  []string{"sync-fresh-checkout"},
			tasks:      "499",
			operations: "20",
			want:       "remote scenarios require at least 500 tasks and 20 operations per task",
		},
		{
			name:       "remote history below minimum",
			selectors:  []string{"sync-fresh-checkout"},
			tasks:      "500",
			operations: "19",
			want:       "remote scenarios require at least 500 tasks and 20 operations per task",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := t.TempDir()
			var stderr bytes.Buffer
			flags, options := newFlagSet(&stderr)
			args := []string{
				"--workbook", workbookBinary,
				"--tasks", test.tasks,
				"--operations", test.operations,
				"--output-json", filepath.Join(outputRoot, "report.json"),
				"--output-markdown", filepath.Join(outputRoot, "report.md"),
			}
			for _, selector := range test.selectors {
				args = append(args, "--scenario", selector)
			}
			if err := flags.Parse(args); err != nil {
				t.Fatalf("parse scenario flags: %v", err)
			}
			err := validateOptions(flags, options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateOptions error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunRejectsInvalidInvocation(t *testing.T) {
	workbookBinary := buildWorkbookBinary(t)
	tests := []struct {
		name   string
		change func([]string) []string
	}{
		{
			name: "missing Workbook binary",
			change: func(args []string) []string {
				return removeFlag(args, "--workbook")
			},
		},
		{
			name: "tasks below ten",
			change: func(args []string) []string {
				return replaceFlagValue(args, "--tasks", "9")
			},
		},
		{
			name: "operations below two",
			change: func(args []string) []string {
				return replaceFlagValue(args, "--operations", "1")
			},
		},
		{
			name: "samples below one",
			change: func(args []string) []string {
				return replaceFlagValue(args, "--samples", "0")
			},
		},
		{
			name: "nonpositive timeout",
			change: func(args []string) []string {
				return replaceFlagValue(args, "--timeout", "0s")
			},
		},
		{
			name: "unsupported object format",
			change: func(args []string) []string {
				return replaceFlagValue(args, "--object-format", "sha512")
			},
		},
		{
			name: "identical output paths",
			change: func(args []string) []string {
				return replaceFlagValue(args, "--output-markdown", flagValue(args, "--output-json"))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := t.TempDir()
			args := []string{
				"--workbook", workbookBinary,
				"--tasks", "10",
				"--operations", "4",
				"--samples", "1",
				"--timeout", "5s",
				"--object-format", "sha1",
				"--output-json", filepath.Join(outputRoot, "report.json"),
				"--output-markdown", filepath.Join(outputRoot, "report.md"),
			}
			var stdout bytes.Buffer
			var stderr bytes.Buffer

			exitCode := run(context.Background(), test.change(args), &stdout, &stderr)
			if exitCode == 0 {
				t.Fatalf("run accepted invalid invocation; stdout = %q; stderr = %q", stdout.String(), stderr.String())
			}
			if stderr.Len() == 0 {
				t.Fatal("invalid invocation produced empty stderr")
			}
		})
	}
}

func TestValidateOptionsEnforcesAcceptanceMinimum(t *testing.T) {
	workbookBinary := buildWorkbookBinary(t)
	rejected := []struct {
		name       string
		tasks      string
		operations string
		want       string
	}{
		{name: "diagnostic task count", tasks: "499", operations: "20", want: "acceptance requires at least 500 total tasks"},
		{name: "short history", tasks: "500", operations: "19", want: "acceptance requires at least 20 operations per task"},
	}
	for _, test := range rejected {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := t.TempDir()
			var stderr bytes.Buffer
			flags, options := newFlagSet(&stderr)
			err := flags.Parse([]string{
				"--workbook", workbookBinary,
				"--tasks", test.tasks,
				"--operations", test.operations,
				"--phase", "acceptance",
				"--output-json", filepath.Join(outputRoot, "report.json"),
				"--output-markdown", filepath.Join(outputRoot, "report.md"),
			})
			if err != nil {
				t.Fatalf("parse flags: %v", err)
			}
			err = validateOptions(flags, options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateOptions error = %v, want acceptance minimum guidance", err)
			}
		})
	}

	// An omitted selection includes the whole registry, so a larger acceptance
	// workload must also satisfy the 500-changed projection refresh point's
	// 500 mutable active task heads.
	t.Run("larger future workload", func(t *testing.T) {
		outputRoot := t.TempDir()
		var stderr bytes.Buffer
		flags, options := newFlagSet(&stderr)
		err := flags.Parse([]string{
			"--workbook", workbookBinary,
			"--tasks", "526",
			"--operations", "21",
			"--samples", "20",
			"--phase", "acceptance",
			"--output-json", filepath.Join(outputRoot, "report.json"),
			"--output-markdown", filepath.Join(outputRoot, "report.md"),
		})
		if err != nil {
			t.Fatalf("parse flags: %v", err)
		}
		if err := validateOptions(flags, options); err != nil {
			t.Fatalf("validateOptions rejected larger acceptance workload: %v", err)
		}
	})
}

func TestValidateOptionsNormalizesTombstonePopulation(t *testing.T) {
	workbookBinary := buildWorkbookBinary(t)
	tests := []struct {
		name           string
		tasks          string
		tombstones     string
		phase          string
		wantActive     int
		wantTombstones int
		wantError      string
	}{
		{name: "default acceptance-sized fixture", tasks: "500", wantActive: 475, wantTombstones: 25},
		{name: "default diagnostic fixture", tasks: "10", wantActive: 9, wantTombstones: 1},
		{name: "explicit diagnostic zero", tasks: "10", tombstones: "0", wantActive: 10, wantTombstones: 0},
		{name: "acceptance requires tombstones", tasks: "500", tombstones: "0", phase: "acceptance", wantError: "acceptance requires at least 25 tombstoned tasks"},
		{name: "cannot exceed total", tasks: "10", tombstones: "11", wantError: "--tombstones must not exceed --tasks"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := t.TempDir()
			args := []string{
				"--workbook", workbookBinary,
				"--tasks", test.tasks,
				"--operations", "20",
				"--scenario", "cli-create",
				"--output-json", filepath.Join(outputRoot, "report.json"),
				"--output-markdown", filepath.Join(outputRoot, "report.md"),
			}
			if test.tombstones != "" {
				args = append(args, "--tombstones", test.tombstones)
			}
			if test.phase != "" {
				args = append(args, "--phase", test.phase)
			}
			var stderr bytes.Buffer
			flags, options := newFlagSet(&stderr)
			if err := flags.Parse(args); err != nil {
				t.Fatal(err)
			}
			err := validateOptions(flags, options)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("validateOptions error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if options.tasks-options.tombstones != test.wantActive || options.tombstones != test.wantTombstones {
				t.Fatalf("normalized fixture = total %d active %d tombstoned %d, want total %d active %d tombstoned %d", options.tasks, options.tasks-options.tombstones, options.tombstones, test.wantActive+test.wantTombstones, test.wantActive, test.wantTombstones)
			}
		})
	}
}

func TestValidateOptionsRejectsEveryAcceptanceFixtureShortfall(t *testing.T) {
	workbookBinary := buildWorkbookBinary(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "499 total tasks", args: []string{"--tasks", "499", "--tombstones", "25", "--operations", "20"}, want: "acceptance requires at least 500 total tasks"},
		{name: "24 tombstones", args: []string{"--tasks", "500", "--tombstones", "24", "--operations", "20"}, want: "acceptance requires at least 25 tombstoned tasks"},
		{name: "19 operations", args: []string{"--tasks", "500", "--tombstones", "25", "--operations", "19"}, want: "acceptance requires at least 20 operations per task"},
		{name: "nine active tasks", args: []string{"--tasks", "500", "--tombstones", "491", "--operations", "20"}, want: "acceptance requires at least 10 active tasks"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := t.TempDir()
			args := append([]string{
				"--workbook", workbookBinary,
				"--phase", "acceptance",
				"--output-json", filepath.Join(outputRoot, "report.json"),
				"--output-markdown", filepath.Join(outputRoot, "report.md"),
			}, test.args...)
			var stderr bytes.Buffer
			flags, options := newFlagSet(&stderr)
			if err := flags.Parse(args); err != nil {
				t.Fatal(err)
			}
			if err := validateOptions(flags, options); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateOptions error = %v, want %q", err, test.want)
			}
		})
	}
}

// Mutation witness: applying the minimum to every acceptance invocation would
// break historical non-local scenarios, while accepting 19 local samples would
// make nearest-rank p95 evidence too weak at the required boundary.
func TestValidateOptionsRequiresTwentySamplesForLocalAcceptance(t *testing.T) {
	workbookBinary := buildWorkbookBinary(t)
	tests := []struct {
		name     string
		phase    string
		scenario string
		samples  string
		wantErr  string
	}{
		{
			name:     "nineteen local acceptance samples",
			phase:    "acceptance",
			scenario: "cli-update",
			samples:  "19",
			wantErr:  "local acceptance requires at least 20 samples",
		},
		{
			name:     "twenty local acceptance samples",
			phase:    "acceptance",
			scenario: "api-update",
			samples:  "20",
		},
		{
			name:     "baseline local diagnostic",
			phase:    "baseline",
			scenario: "cli-update",
			samples:  "1",
		},
		{
			name:     "non-local acceptance compatibility",
			phase:    "acceptance",
			scenario: "sync-fresh-checkout",
			samples:  "19",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := t.TempDir()
			var stderr bytes.Buffer
			flags, options := newFlagSet(&stderr)
			if err := flags.Parse([]string{
				"--workbook", workbookBinary,
				"--tasks", "500",
				"--tombstones", "25",
				"--operations", "20",
				"--samples", test.samples,
				"--phase", test.phase,
				"--scenario", test.scenario,
				"--output-json", filepath.Join(outputRoot, "report.json"),
				"--output-markdown", filepath.Join(outputRoot, "report.md"),
			}); err != nil {
				t.Fatal(err)
			}

			err := validateOptions(flags, options)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("validateOptions error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateOptions rejected valid boundary: %v", err)
			}
		})
	}
}

func TestBenchmarkEnvironmentRecordsMeasuredBinarySHA256(t *testing.T) {
	binaryDirectory := t.TempDir()
	gitPath := filepath.Join(binaryDirectory, "git")
	goPath := filepath.Join(binaryDirectory, "go")
	workbookPath := filepath.Join(binaryDirectory, "workbook")
	writeExecutableScript(t, gitPath, "printf 'git version test\\n'")
	writeExecutableScript(t, goPath, "printf 'go version test\\n'")
	writeExecutableScript(t, workbookPath, `printf '%s\n' '{"format":"workbook.result","version":1,"command":"version","data":{"version":"dev","commit":"test"}}'`)
	t.Setenv("PATH", binaryDirectory)

	want := fmt.Sprintf("%x", sha256.Sum256([]byte("#!/bin/sh\nprintf '%s\\n' '{\"format\":\"workbook.result\",\"version\":1,\"command\":\"version\",\"data\":{\"version\":\"dev\",\"commit\":\"test\"}}'\n")))
	environment, err := benchmarkEnvironment(context.Background(), workbookPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if environment.WorkbookBinarySHA256 != want {
		t.Fatalf("Workbook binary SHA-256 = %q, want %q", environment.WorkbookBinarySHA256, want)
	}
}

func TestRunBenchmarkRejectsUnknownAcceptanceCommitBeforeFixtureConstruction(t *testing.T) {
	binaryDirectory := t.TempDir()
	gitPath := filepath.Join(binaryDirectory, "git")
	goPath := filepath.Join(binaryDirectory, "go")
	workbookPath := filepath.Join(binaryDirectory, "workbook")
	writeExecutableScript(t, gitPath, "printf 'git version test\\n'")
	writeExecutableScript(t, goPath, "printf 'go version test\\n'")
	writeExecutableScript(t, workbookPath, `printf '%s\n' '{"format":"workbook.result","version":1,"command":"version","data":{"version":"dev","commit":"unknown"}}'`)
	t.Setenv("PATH", binaryDirectory)

	_, err := runBenchmark(context.Background(), options{
		workbookBinary: workbookPath,
		tasks:          500,
		tombstones:     25,
		operations:     20,
		samples:        1,
		timeout:        time.Second,
		objectFormat:   "sha1",
		phase:          "acceptance",
		scenarios:      []string{"cli-create"},
	})
	if err == nil || !strings.Contains(err.Error(), "acceptance requires a measured Workbook commit") {
		t.Fatalf("runBenchmark error = %v, want unknown measured commit rejection", err)
	}
}

func TestBenchmarkEnvironmentBoundsEveryMetadataCommand(t *testing.T) {
	tests := []string{"git", "go", "workbook"}
	for _, hungCommand := range tests {
		t.Run(hungCommand, func(t *testing.T) {
			binaryDirectory := t.TempDir()
			gitPath := filepath.Join(binaryDirectory, "git")
			goPath := filepath.Join(binaryDirectory, "go")
			workbookPath := filepath.Join(binaryDirectory, "workbook")
			writeExecutableScript(t, gitPath, "printf 'git version test\\n'")
			writeExecutableScript(t, goPath, "printf 'go version test\\n'")
			writeExecutableScript(t, workbookPath, `printf '%s\n' '{"format":"workbook.result","version":1,"command":"version","data":{"version":"dev","commit":"test"}}'`)
			switch hungCommand {
			case "git":
				writeExecutableScript(t, gitPath, "/bin/sleep 5")
			case "go":
				writeExecutableScript(t, goPath, "/bin/sleep 5")
			case "workbook":
				writeExecutableScript(t, workbookPath, "/bin/sleep 5")
			}
			t.Setenv("PATH", binaryDirectory)

			startedAt := time.Now()
			_, err := benchmarkEnvironment(context.Background(), workbookPath, 20*time.Millisecond)
			elapsed := time.Since(startedAt)
			if err == nil || !strings.Contains(err.Error(), "timed out after 20ms") {
				t.Fatalf("benchmarkEnvironment error = %v, want %s timeout", err, hungCommand)
			}
			if elapsed > 2*time.Second {
				t.Fatalf("benchmarkEnvironment returned after %s, want bounded descendant termination", elapsed)
			}
		})
	}
}

func buildWorkbookBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "workbook")
	buildWorkbookBinaryAt(t, binary)
	return binary
}

func buildWorkbookBinaryAt(t *testing.T, binary string) {
	t.Helper()
	buildWorkbookBinaryWithCommit(t, binary, "")
}

// buildWorkbookBinaryWithCommit mirrors the evidence build. An evidence phase
// refuses to measure a binary that reports no commit, so a test that exercises
// one has to embed a commit exactly as the documented build does.
func buildWorkbookBinaryWithCommit(t *testing.T, binary, commit string) {
	t.Helper()
	root := filepath.Clean(filepath.Join("..", ".."))
	args := []string{"build", "-buildvcs=false"}
	if commit != "" {
		args = append(args, "-ldflags", "-X main.commit="+commit)
	}
	args = append(args, "-o", binary, "./cmd/workbook")
	command := exec.Command("go", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build workbook: %v\n%s", err, output)
	}
}

func writeExecutableScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func removeFlag(args []string, name string) []string {
	for index := range args {
		if args[index] == name {
			return append(append([]string(nil), args[:index]...), args[index+2:]...)
		}
	}
	panic("missing test flag " + name)
}

func replaceFlagValue(args []string, name, value string) []string {
	result := append([]string(nil), args...)
	for index := range result {
		if result[index] == name {
			result[index+1] = value
			return result
		}
	}
	panic("missing test flag " + name)
}

func flagValue(args []string, name string) string {
	for index := range args {
		if args[index] == name {
			return args[index+1]
		}
	}
	panic("missing test flag " + name)
}

func TestBenchmarkReportsEveryProjectionRefreshChangeCountPoint(t *testing.T) {
	workbook := buildWorkbookBinary(t)
	selected := []string{
		"projection-refresh-unchanged",
		"projection-refresh-one-changed",
		"projection-refresh-five-changed",
	}
	report, err := runBenchmark(context.Background(), options{
		workbookBinary: workbook,
		tasks:          12,
		tombstones:     2,
		operations:     3,
		samples:        2,
		timeout:        60 * time.Second,
		objectFormat:   "sha1",
		phase:          "baseline",
		scenarios:      selected,
	})
	if err != nil {
		t.Fatal(err)
	}

	gotNames := make([]string, len(report.Scenarios))
	for index, scenario := range report.Scenarios {
		gotNames[index] = scenario.Name
		if len(scenario.Samples) != 2 {
			t.Errorf("%s samples = %d, want the requested 2", scenario.Name, len(scenario.Samples))
		}
	}
	if !reflect.DeepEqual(gotNames, selected) {
		t.Fatalf("measured scenarios = %#v, want only %#v", gotNames, selected)
	}
	if report.ProjectionRefresh == nil {
		t.Fatal("report has no projection refresh block")
	}
	if report.ProjectionRefresh.Samples != 2 {
		t.Errorf("projection refresh samples = %d, want 2", report.ProjectionRefresh.Samples)
	}
	if report.ProjectionRefresh.Fixture.ObjectFormat != "sha1" ||
		report.ProjectionRefresh.Fixture.TotalTasks != 12 ||
		report.ProjectionRefresh.Fixture.ActiveTasks != 10 ||
		report.ProjectionRefresh.Fixture.TombstonedTasks != 2 {
		t.Errorf("projection refresh fixture = %#v, want the measured fixture shape", report.ProjectionRefresh.Fixture)
	}
	wantChanged := []int{0, 1, 5}
	if len(report.ProjectionRefresh.Points) != len(wantChanged) {
		t.Fatalf("projection refresh points = %#v, want %d", report.ProjectionRefresh.Points, len(wantChanged))
	}
	for index, point := range report.ProjectionRefresh.Points {
		if point.Scenario != selected[index] || point.ChangedTaskHeads != wantChanged[index] {
			t.Fatalf("point %d = %#v, want %s at %d changed heads", index+1, point, selected[index], wantChanged[index])
		}
	}

	outputRoot := t.TempDir()
	jsonPath := filepath.Join(outputRoot, "report.json")
	markdownPath := filepath.Join(outputRoot, "report.md")
	if err := writeReports(jsonPath, markdownPath, report); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded perf.Report
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ProjectionRefresh == nil ||
		!reflect.DeepEqual(*decoded.ProjectionRefresh, *report.ProjectionRefresh) {
		t.Fatalf("decoded projection refresh = %#v, want %#v", decoded.ProjectionRefresh, report.ProjectionRefresh)
	}
	markdown, err := os.ReadFile(markdownPath)
	if err != nil {
		t.Fatal(err)
	}
	fragments := append([]string{
		"## Projection refresh change-count family",
		"2 sample(s) per point",
		"sha1 object format",
		"Slope:",
	}, selected...)
	for _, fragment := range fragments {
		if !strings.Contains(string(markdown), fragment) {
			t.Fatalf("Markdown report does not contain %q:\n%s", fragment, markdown)
		}
	}
}

func TestValidateOptionsRejectsProjectionRefreshFixtureShortfallBeforeAnyWork(t *testing.T) {
	workbook := buildWorkbookBinary(t)
	for _, test := range []struct {
		name       string
		tasks      string
		tombstones string
		want       string
	}{
		{
			name:       "default acceptance fixture is too small",
			tasks:      "500",
			tombstones: "25",
			want:       "projection-refresh-five-hundred-changed requires 500 mutable active task heads",
		},
		{
			name:       "five hundred active tasks are enough",
			tasks:      "525",
			tombstones: "25",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := t.TempDir()
			var stderr bytes.Buffer
			flags, options := newFlagSet(&stderr)
			if err := flags.Parse([]string{
				"--workbook", workbook,
				"--tasks", test.tasks,
				"--tombstones", test.tombstones,
				"--operations", "20",
				"--scenario", "projection-refresh-five-hundred-changed",
				"--output-json", filepath.Join(outputRoot, "report.json"),
				"--output-markdown", filepath.Join(outputRoot, "report.md"),
			}); err != nil {
				t.Fatal(err)
			}
			err := validateOptions(flags, options)
			if test.want == "" {
				if err != nil {
					t.Fatalf("validate sufficient fixture: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("fixture shortfall error = %v, want %q", err, test.want)
			}
			if !strings.Contains(err.Error(), "--tasks 525 --tombstones 25") {
				t.Fatalf("fixture shortfall error = %v, want actionable fixture guidance", err)
			}
		})
	}
}

// TestWatcherScenarioSelectionIsExclusive keeps the long observation family out
// of every run that did not ask for it, and keeps a run that did ask for it from
// dragging unrelated scenario families along.
//
// Mutation witness: matching the selector by prefix, or ignoring it entirely,
// would either run the watcher for two minutes on an unrelated selection or
// silently drop the only measurement the selection requested.
func TestWatcherScenarioSelectionIsExclusive(t *testing.T) {
	if !selectsWatcherScenario([]string{"cli-update", perf.WatcherScenario}) {
		t.Fatal("watcher selector was not recognized alongside another scenario")
	}
	for _, selection := range [][]string{
		nil,
		{"cli-update"},
		{"sync-already-synchronized", "validate-full-history"},
	} {
		if selectsWatcherScenario(selection) {
			t.Fatalf("selection %#v was routed to the watcher family", selection)
		}
	}
	if names := selectedColdScenarioNames([]string{perf.WatcherScenario}); len(names) != 0 {
		t.Fatalf("cold scenarios for a watcher-only selection = %#v, want none", names)
	}
	if names := selectedWarmScenarioNames([]string{perf.WatcherScenario}); len(names) != 0 {
		t.Fatalf("warm scenarios for a watcher-only selection = %#v, want none", names)
	}
	if hasRepositoryScenario([]string{perf.WatcherScenario}) {
		t.Fatal("a watcher-only selection was routed to the repository family")
	}
}

// TestValidateOptionsRejectsWatcherFixtureShortfall keeps the steady-state
// evidence honest. Every synchronization tick reads each canonical and tracking
// tip, so its cost scales with the project's task count; measuring it on a tiny
// fixture would publish a per-tick number that describes no real board.
func TestValidateOptionsRejectsWatcherFixtureShortfall(t *testing.T) {
	workbook := buildWorkbookBinary(t)
	for _, test := range []struct {
		name       string
		tasks      string
		operations string
		want       string
	}{
		{
			name:       "task population below minimum",
			tasks:      "499",
			operations: "20",
			want:       "watcher scenarios require at least 500 tasks and 20 operations per task",
		},
		{
			name:       "history depth below minimum",
			tasks:      "500",
			operations: "19",
			want:       "watcher scenarios require at least 500 tasks and 20 operations per task",
		},
		{
			name:       "acceptance fixture is accepted",
			tasks:      "500",
			operations: "20",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := t.TempDir()
			var stderr bytes.Buffer
			flags, options := newFlagSet(&stderr)
			if err := flags.Parse([]string{
				"--workbook", workbook,
				"--tasks", test.tasks,
				"--operations", test.operations,
				"--scenario", perf.WatcherScenario,
				"--output-json", filepath.Join(outputRoot, "report.json"),
				"--output-markdown", filepath.Join(outputRoot, "report.md"),
			}); err != nil {
				t.Fatal(err)
			}
			err := validateOptions(flags, options)
			if test.want == "" {
				if err != nil {
					t.Fatalf("validate sufficient watcher fixture: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("watcher fixture shortfall error = %v, want %q", err, test.want)
			}
		})
	}
}
