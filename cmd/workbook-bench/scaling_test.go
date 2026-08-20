package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/perf"
)

func scalingArgs(workbookBinary, outputRoot string, extra ...string) []string {
	args := []string{
		"--workbook", workbookBinary,
		"--scaling",
		"--output-json", filepath.Join(outputRoot, "report.json"),
		"--output-markdown", filepath.Join(outputRoot, "report.md"),
	}
	return append(args, extra...)
}

func parseScalingOptions(t *testing.T, args []string) (*options, error) {
	t.Helper()
	var stderr bytes.Buffer
	flags, parsed := newFlagSet(&stderr)
	if err := flags.Parse(args); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	return parsed, validateOptions(flags, parsed)
}

// Mutation witness: relaxing the workload minimums for every invocation, rather
// than only for the scaling matrix, would let a single-run remote or validation
// baseline measure a fixture the harness never approved.
func TestValidateOptionsRelaxesWorkloadMinimumsOnlyForTheScalingMatrix(t *testing.T) {
	workbookBinary := buildWorkbookBinary(t)

	t.Run("scaling matrix accepts its own small point", func(t *testing.T) {
		parsed, err := parseScalingOptions(t, scalingArgs(workbookBinary, t.TempDir(), "--scaling-point", "100x20"))
		if err != nil {
			t.Fatalf("scaling validation error = %v, want acceptance of the 100-active point", err)
		}
		if want := []perf.ScalingPointSpec{{ActiveTasks: 100, OperationsPerTask: 20}}; !reflect.DeepEqual(parsed.scalingPoints, want) {
			t.Fatalf("scaling points = %#v, want %#v", parsed.scalingPoints, want)
		}
		if !reflect.DeepEqual(parsed.scenarios, perf.ScalingScenarioNames()) {
			t.Fatalf("scaling scenarios = %#v, want %#v", parsed.scenarios, perf.ScalingScenarioNames())
		}
	})

	t.Run("scaling matrix defaults to the story matrix", func(t *testing.T) {
		parsed, err := parseScalingOptions(t, scalingArgs(workbookBinary, t.TempDir()))
		if err != nil {
			t.Fatalf("scaling validation error = %v, want the default matrix", err)
		}
		if !reflect.DeepEqual(parsed.scalingPoints, perf.DefaultScalingPoints()) {
			t.Fatalf("default scaling points = %#v, want %#v", parsed.scalingPoints, perf.DefaultScalingPoints())
		}
	})

	strict := []struct {
		name     string
		selector string
		want     string
	}{
		{"single-run remote", "sync-already-synchronized", "remote scenarios require at least 500 tasks and 20 operations per task"},
		{"single-run validation", "validate-cached-unchanged", "validation scenarios require at least 500 tasks and 20 operations per task"},
	}
	for _, test := range strict {
		t.Run(test.name+" keeps its workload minimum", func(t *testing.T) {
			outputRoot := t.TempDir()
			_, err := parseScalingOptions(t, []string{
				"--workbook", workbookBinary,
				"--tasks", "105",
				"--operations", "20",
				"--scenario", test.selector,
				"--output-json", filepath.Join(outputRoot, "report.json"),
				"--output-markdown", filepath.Join(outputRoot, "report.md"),
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("single-run validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateOptionsRejectsConflictingScalingInvocations(t *testing.T) {
	workbookBinary := buildWorkbookBinary(t)
	tests := []struct {
		name string
		args func(outputRoot string) []string
		want string
	}{
		{
			name: "explicit scenario selection",
			args: func(root string) []string { return scalingArgs(workbookBinary, root, "--scenario", "cli-update") },
			want: "--scenario is not valid with --scaling",
		},
		{
			name: "explicit task count",
			args: func(root string) []string { return scalingArgs(workbookBinary, root, "--tasks", "500") },
			want: "--tasks is not valid with --scaling",
		},
		{
			name: "explicit tombstone count",
			args: func(root string) []string { return scalingArgs(workbookBinary, root, "--tombstones", "25") },
			want: "--tombstones is not valid with --scaling",
		},
		{
			name: "explicit operation count",
			args: func(root string) []string { return scalingArgs(workbookBinary, root, "--operations", "20") },
			want: "--operations is not valid with --scaling",
		},
		{
			name: "malformed point",
			args: func(root string) []string { return scalingArgs(workbookBinary, root, "--scaling-point", "100-20") },
			want: "--scaling-point must be <active tasks>x<history depth>",
		},
		{
			name: "nonnumeric point",
			args: func(root string) []string { return scalingArgs(workbookBinary, root, "--scaling-point", "manyx20") },
			want: "--scaling-point must be <active tasks>x<history depth>",
		},
		{
			name: "unmeasurable point",
			args: func(root string) []string { return scalingArgs(workbookBinary, root, "--scaling-point", "9x20") },
			want: "at least 10 active tasks",
		},
		{
			name: "duplicate point",
			args: func(root string) []string {
				return scalingArgs(workbookBinary, root, "--scaling-point", "100x20", "--scaling-point", "100x20")
			},
			want: "duplicate --scaling-point 100x20",
		},
		{
			name: "scaling point without scaling mode",
			args: func(root string) []string {
				return []string{
					"--workbook", workbookBinary,
					"--scaling-point", "100x20",
					"--output-json", filepath.Join(root, "report.json"),
					"--output-markdown", filepath.Join(root, "report.md"),
				}
			},
			want: "--scaling-point requires --scaling",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseScalingOptions(t, test.args(t.TempDir()))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateOptions error = %v, want %q", err, test.want)
			}
		})
	}
}

func scalingTestReport() perf.ScalingReport {
	point := func(active, depth int, milliseconds float64, gitProcesses int) perf.ScalingPoint {
		spec := perf.ScalingPointSpec{ActiveTasks: active, OperationsPerTask: depth}
		fixture, err := spec.FixtureSpec("sha1")
		if err != nil {
			panic(err)
		}
		scenarios := make([]perf.ScenarioResult, 0, len(perf.ScalingScenarioNames()))
		for _, name := range perf.ScalingScenarioNames() {
			scenarios = append(scenarios, perf.ScenarioResult{
				Name:    name,
				Surface: "cold-cli",
				Samples: []perf.Sample{{Duration: time.Duration(milliseconds) * time.Millisecond, GitProcesses: gitProcesses}},
			})
		}
		return perf.ScalingPoint{Name: spec.Name(), Spec: spec, Fixture: fixture, Scenarios: scenarios}
	}
	points := []perf.ScalingPoint{point(100, 20, 10, 3), point(500, 20, 20, 6)}
	return perf.ScalingReport{
		Format:       perf.ScalingReportFormat,
		Version:      perf.ScalingReportVersion,
		GeneratedAt:  time.Date(2026, time.July, 30, 0, 0, 0, 0, time.UTC),
		ObjectFormat: "sha1",
		Samples:      1,
		Points:       points,
		Slopes:       perf.ComputeScalingSlopes(points),
	}
}

// Mutation witness: reporting success after a timed-out or failed matrix sample
// would present unusable measurements as complete scaling evidence.
func TestRunScalingWritesBothReportsAndReturnsExecutionStatus(t *testing.T) {
	tests := []struct {
		name         string
		report       func() perf.ScalingReport
		matrixErr    error
		wantExitCode int
		wantReports  bool
	}{
		{name: "complete matrix", report: scalingTestReport, wantExitCode: 0, wantReports: true},
		{
			name: "failed sample",
			report: func() perf.ScalingReport {
				report := scalingTestReport()
				report.Points[0].Scenarios[0].Samples[0].ExitCode = 1
				report.Points[0].Scenarios[0].Samples[0].Error = "product failure"
				return report
			},
			wantExitCode: failureExitCode,
			wantReports:  true,
		},
		{
			name: "timed-out sample",
			report: func() perf.ScalingReport {
				report := scalingTestReport()
				report.Points[1].Scenarios[2].Samples[0].TimedOut = true
				return report
			},
			wantExitCode: failureExitCode,
			wantReports:  true,
		},
		{
			name:         "matrix failure",
			report:       scalingTestReport,
			matrixErr:    errors.New("build point fixture: no space"),
			wantExitCode: failureExitCode,
			wantReports:  false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := t.TempDir()
			runOptions := options{
				outputJSON:     filepath.Join(outputRoot, "report.json"),
				outputMarkdown: filepath.Join(outputRoot, "report.md"),
			}
			var stdout, stderr bytes.Buffer
			exitCode := runScalingWithMatrix(context.Background(), runOptions, &stdout, &stderr, func(context.Context, options) (perf.ScalingReport, error) {
				if test.matrixErr != nil {
					return perf.ScalingReport{}, test.matrixErr
				}
				return test.report(), nil
			})
			if exitCode != test.wantExitCode {
				t.Fatalf("exit code = %d, want %d; stderr = %q", exitCode, test.wantExitCode, stderr.String())
			}
			jsonData, err := os.ReadFile(runOptions.outputJSON)
			if !test.wantReports {
				if err == nil {
					t.Fatalf("wrote a JSON report for a failed matrix run")
				}
				return
			}
			if err != nil {
				t.Fatalf("read JSON report: %v", err)
			}
			var decoded perf.ScalingReport
			if err := json.Unmarshal(jsonData, &decoded); err != nil {
				t.Fatalf("decode JSON report: %v", err)
			}
			if decoded.Format != perf.ScalingReportFormat || decoded.Version != perf.ScalingReportVersion {
				t.Fatalf("report identity = %q v%d, want %q v%d", decoded.Format, decoded.Version, perf.ScalingReportFormat, perf.ScalingReportVersion)
			}
			if len(decoded.Points) != 2 || len(decoded.Slopes) == 0 {
				t.Fatalf("report has %d points and %d slopes, want 2 points and at least one slope", len(decoded.Points), len(decoded.Slopes))
			}
			markdown, err := os.ReadFile(runOptions.outputMarkdown)
			if err != nil {
				t.Fatalf("read Markdown report: %v", err)
			}
			if !strings.Contains(string(markdown), "Observed slopes") {
				t.Fatalf("Markdown report is missing the slope section:\n%s", markdown)
			}
		})
	}
}

// Mutation witness: running the matrix through the single-fixture benchmark
// path would measure one fixture instead of every point, and would reimpose the
// workload minimums the matrix deliberately relaxes.
func TestRunScalingBenchmarkMeasuresEveryRequestedPointEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end scaling matrix run is slow")
	}
	workbookBinary := filepath.Join(t.TempDir(), "workbook")
	buildWorkbookBinaryWithCommit(t, workbookBinary, "0000000000000000000000000000000000000000")
	outputRoot := t.TempDir()
	var stdout, stderr bytes.Buffer

	exitCode := run(context.Background(), []string{
		"--workbook", workbookBinary,
		"--scaling",
		"--scaling-point", "10x4",
		"--scaling-point", "10x6",
		"--samples", "1",
		"--timeout", "60s",
		"--object-format", "sha1",
		"--output-json", filepath.Join(outputRoot, "report.json"),
		"--output-markdown", filepath.Join(outputRoot, "report.md"),
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("scaling run exit code = %d, want 0; stdout = %q; stderr = %q", exitCode, stdout.String(), stderr.String())
	}

	jsonData, err := os.ReadFile(filepath.Join(outputRoot, "report.json"))
	if err != nil {
		t.Fatalf("read JSON report: %v", err)
	}
	var report perf.ScalingReport
	if err := json.Unmarshal(jsonData, &report); err != nil {
		t.Fatalf("decode JSON report: %v", err)
	}
	if len(report.Points) != 2 {
		t.Fatalf("measured points = %d, want 2", len(report.Points))
	}
	for _, point := range report.Points {
		names := make([]string, len(point.Scenarios))
		for index, scenario := range point.Scenarios {
			names[index] = scenario.Name
			if scenario.Summary.Completed != 1 {
				t.Fatalf("%s %s completed = %d, want 1", point.Name, scenario.Name, scenario.Summary.Completed)
			}
		}
		if !reflect.DeepEqual(names, perf.ScalingScenarioNames()) {
			t.Fatalf("%s scenarios = %#v, want %#v", point.Name, names, perf.ScalingScenarioNames())
		}
		if point.Fixture != (perf.FixtureSpec{TotalTasks: 11, ActiveTasks: 10, TombstonedTasks: 1, OperationsPerTask: point.Spec.OperationsPerTask, ObjectFormat: "sha1"}) {
			t.Fatalf("%s realized fixture = %#v, want the eleven-ref representative shape", point.Name, point.Fixture)
		}
	}
	if len(report.Slopes) == 0 {
		t.Fatal("end-to-end scaling report has no slopes")
	}
	for _, slope := range report.Slopes {
		if slope.Axis != perf.ScalingAxisHistoryDepth {
			t.Fatalf("slope axis = %q, want only history-depth segments for a single active population", slope.Axis)
		}
	}
}

// Mutation witness: dropping the warning would let a run publish a report
// that names no source commit with no hint that later runs cannot be
// compared against it; warning on a known commit would add noise to every
// ordinary run.
func TestRunScalingWarnsWhenTheMeasuredCommitIsUnknown(t *testing.T) {
	for _, test := range []struct {
		name        string
		commit      string
		wantWarning bool
	}{
		{name: "unknown commit warns", commit: unknownWorkbookCommit, wantWarning: true},
		{name: "empty commit warns", commit: "", wantWarning: true},
		{name: "known commit stays quiet", commit: "725a2838adde83e20e9b73eb959820ca9871cb0c", wantWarning: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := t.TempDir()
			runOptions := options{
				outputJSON:     filepath.Join(outputRoot, "report.json"),
				outputMarkdown: filepath.Join(outputRoot, "report.md"),
			}
			var stdout, stderr bytes.Buffer
			exitCode := runScalingWithMatrix(context.Background(), runOptions, &stdout, &stderr, func(context.Context, options) (perf.ScalingReport, error) {
				report := scalingTestReport()
				report.Environment.WorkbookCommit = test.commit
				return report, nil
			})
			if exitCode != 0 {
				t.Fatalf("exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
			}
			warned := strings.Contains(stderr.String(), "no source commit")
			if warned != test.wantWarning {
				t.Fatalf("stderr = %q, warning presence = %t, want %t", stderr.String(), warned, test.wantWarning)
			}
		})
	}
}
