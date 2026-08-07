package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/perf"
)

// scalingPhase runs the task-count and history-depth scaling matrix instead of
// the single-fixture benchmark. It is a separate phase so the baseline and
// acceptance guards keep their existing strictness.
const scalingPhase = "scaling"

// configureScalingOptions resolves the matrix points and rejects flags whose
// meaning the matrix already owns.
func configureScalingOptions(flags *flag.FlagSet, options *options) error {
	explicit := explicitFlagNames(flags)
	for _, name := range []string{"tasks", "tombstones", "operations"} {
		if _, set := explicit[name]; set {
			return fmt.Errorf("--%s is not valid with --phase scaling; each matrix point defines its own fixture", name)
		}
	}
	if len(options.scenarioFlags) != 0 {
		return fmt.Errorf("--scenario is not valid with --phase scaling; the matrix defines its own scenario set")
	}
	points, err := resolveScalingPoints(options.scalingPointFlags)
	if err != nil {
		return err
	}
	for _, point := range points {
		if _, err := point.FixtureSpec(options.objectFormat); err != nil {
			return err
		}
	}
	options.scalingPoints = points
	options.scenarios = perf.ScalingScenarioNames()
	return nil
}

func explicitFlagNames(flags *flag.FlagSet) map[string]struct{} {
	names := make(map[string]struct{})
	flags.Visit(func(visited *flag.Flag) { names[visited.Name] = struct{}{} })
	return names
}

// resolveScalingPoints parses repeated `<active tasks>x<history depth>`
// selectors, or returns the default story matrix when none are supplied.
func resolveScalingPoints(selectors []string) ([]perf.ScalingPointSpec, error) {
	if len(selectors) == 0 {
		return perf.DefaultScalingPoints(), nil
	}
	points := make([]perf.ScalingPointSpec, 0, len(selectors))
	seen := make(map[perf.ScalingPointSpec]struct{}, len(selectors))
	for _, selector := range selectors {
		point, err := parseScalingPoint(selector)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[point]; duplicate {
			return nil, fmt.Errorf("duplicate --scaling-point %s", selector)
		}
		seen[point] = struct{}{}
		points = append(points, point)
	}
	return points, nil
}

func parseScalingPoint(selector string) (perf.ScalingPointSpec, error) {
	malformed := fmt.Errorf("--scaling-point must be <active tasks>x<history depth>, got %q", selector)
	active, depth, found := strings.Cut(selector, "x")
	if !found {
		return perf.ScalingPointSpec{}, malformed
	}
	activeTasks, err := strconv.Atoi(active)
	if err != nil {
		return perf.ScalingPointSpec{}, malformed
	}
	operations, err := strconv.Atoi(depth)
	if err != nil {
		return perf.ScalingPointSpec{}, malformed
	}
	return perf.ScalingPointSpec{ActiveTasks: activeTasks, OperationsPerTask: operations}, nil
}

func runScalingWithMatrix(
	ctx context.Context,
	options options,
	stdout io.Writer,
	stderr io.Writer,
	matrix func(context.Context, options) (perf.ScalingReport, error),
) int {
	report, err := matrix(ctx, options)
	if err != nil {
		fmt.Fprintf(stderr, "workbook-bench: %v\n", err)
		return failureExitCode
	}
	if err := writeScalingReports(options.outputJSON, options.outputMarkdown, report); err != nil {
		fmt.Fprintf(stderr, "workbook-bench: %v\n", err)
		return failureExitCode
	}
	fmt.Fprintf(stdout, "wrote %s and %s\n", options.outputJSON, options.outputMarkdown)
	if hasFailedScalingMeasurement(report) {
		fmt.Fprintln(stderr, "workbook-bench: measurement failed; see retained reports")
		return failureExitCode
	}
	return 0
}

func runScalingBenchmark(ctx context.Context, options options) (perf.ScalingReport, error) {
	environment, err := benchmarkEnvironment(ctx, options.workbookBinary, options.timeout)
	if err != nil {
		return perf.ScalingReport{}, err
	}
	if err := requireMeasuredCommit(options.phase, environment); err != nil {
		return perf.ScalingReport{}, err
	}
	fixtureRoot, err := os.MkdirTemp("", "workbook-scaling-")
	if err != nil {
		return perf.ScalingReport{}, fmt.Errorf("create temporary scaling fixture root: %w", err)
	}
	defer os.RemoveAll(fixtureRoot)

	points, err := perf.RunScalingMatrix(ctx, perf.ScalingSpec{
		WorkbookBinary: options.workbookBinary,
		ObjectFormat:   options.objectFormat,
		Samples:        options.samples,
		CommandTimeout: options.timeout,
		Points:         options.scalingPoints,
	}, filepath.Join(fixtureRoot, "matrix"))
	if err != nil {
		return perf.ScalingReport{}, err
	}
	return perf.ScalingReport{
		Format:       perf.ScalingReportFormat,
		Version:      perf.ScalingReportVersion,
		Phase:        scalingPhase,
		GeneratedAt:  time.Now().UTC(),
		Environment:  environment,
		ObjectFormat: options.objectFormat,
		Samples:      options.samples,
		Points:       points,
		Slopes:       perf.ComputeScalingSlopes(points),
	}, nil
}

func hasFailedScalingMeasurement(report perf.ScalingReport) bool {
	for _, point := range report.Points {
		for _, scenario := range point.Scenarios {
			for _, sample := range scenario.Samples {
				if sample.TimedOut || sample.ExitCode != 0 || sample.Error != "" {
					return true
				}
			}
		}
	}
	return false
}

func writeScalingReports(jsonPath, markdownPath string, report perf.ScalingReport) error {
	jsonTemporary, err := stageReport(jsonPath, report.WriteJSON)
	if err != nil {
		return fmt.Errorf("write JSON report: %w", err)
	}
	defer os.Remove(jsonTemporary)

	markdownTemporary, err := stageReport(markdownPath, report.WriteMarkdown)
	if err != nil {
		return fmt.Errorf("write Markdown report: %w", err)
	}
	defer os.Remove(markdownTemporary)

	if err := os.Rename(jsonTemporary, jsonPath); err != nil {
		return fmt.Errorf("replace JSON report: %w", err)
	}
	if err := os.Rename(markdownTemporary, markdownPath); err != nil {
		return fmt.Errorf("replace Markdown report: %w", err)
	}
	return nil
}
