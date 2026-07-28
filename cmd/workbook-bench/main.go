package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/perf"
)

const (
	invocationExitCode = 2
	failureExitCode    = 1
)

type options struct {
	workbookBinary string
	tasks          int
	operations     int
	samples        int
	timeout        time.Duration
	objectFormat   string
	outputJSON     string
	outputMarkdown string
	phase          string
}

type workbookVersionResult struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Command string `json:"command"`
	Data    struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	} `json:"data"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags, options := newFlagSet(stderr)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return invocationExitCode
	}
	if err := validateOptions(flags, options); err != nil {
		fmt.Fprintf(stderr, "workbook-bench: %v\n", err)
		return invocationExitCode
	}

	report, err := runBenchmark(ctx, *options)
	if err != nil {
		fmt.Fprintf(stderr, "workbook-bench: %v\n", err)
		return failureExitCode
	}
	if err := writeReports(options.outputJSON, options.outputMarkdown, report); err != nil {
		fmt.Fprintf(stderr, "workbook-bench: %v\n", err)
		return failureExitCode
	}

	fmt.Fprintf(stdout, "wrote %s and %s\n", options.outputJSON, options.outputMarkdown)
	return 0
}

func newFlagSet(stderr io.Writer) (*flag.FlagSet, *options) {
	options := &options{}
	flags := flag.NewFlagSet("workbook-bench", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&options.workbookBinary, "workbook", "", "path to the Workbook executable")
	flags.IntVar(&options.tasks, "tasks", 500, "active task count")
	flags.IntVar(&options.operations, "operations", 20, "operations per task")
	flags.IntVar(&options.samples, "samples", 1, "samples per scenario")
	flags.DurationVar(&options.timeout, "timeout", 60*time.Second, "per-command timeout")
	flags.StringVar(&options.objectFormat, "object-format", "sha1", "Git object format (sha1 or sha256)")
	flags.StringVar(&options.outputJSON, "output-json", "", "JSON report path")
	flags.StringVar(&options.outputMarkdown, "output-markdown", "", "Markdown report path")
	flags.StringVar(&options.phase, "phase", "baseline", "report phase (baseline or acceptance)")
	return flags, options
}

func validateOptions(flags *flag.FlagSet, options *options) error {
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if options.workbookBinary == "" {
		return fmt.Errorf("--workbook is required")
	}
	if options.tasks < 10 {
		return fmt.Errorf("--tasks must be at least 10")
	}
	if options.operations < 2 {
		return fmt.Errorf("--operations must be at least 2")
	}
	if options.samples < 1 {
		return fmt.Errorf("--samples must be at least 1")
	}
	if options.timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	if options.objectFormat != "sha1" && options.objectFormat != "sha256" {
		return fmt.Errorf("--object-format must be sha1 or sha256")
	}
	if options.outputJSON == "" {
		return fmt.Errorf("--output-json is required")
	}
	if options.outputMarkdown == "" {
		return fmt.Errorf("--output-markdown is required")
	}
	if options.phase != "baseline" && options.phase != "acceptance" {
		return fmt.Errorf("--phase must be baseline or acceptance")
	}

	jsonPath, err := filepath.Abs(options.outputJSON)
	if err != nil {
		return fmt.Errorf("resolve --output-json: %w", err)
	}
	markdownPath, err := filepath.Abs(options.outputMarkdown)
	if err != nil {
		return fmt.Errorf("resolve --output-markdown: %w", err)
	}
	if jsonPath == markdownPath {
		return fmt.Errorf("--output-json and --output-markdown must be different paths")
	}
	options.outputJSON = jsonPath
	options.outputMarkdown = markdownPath
	return nil
}

func runBenchmark(ctx context.Context, options options) (perf.Report, error) {
	environment, err := benchmarkEnvironment(ctx, options.workbookBinary)
	if err != nil {
		return perf.Report{}, err
	}

	fixtureRoot, err := os.MkdirTemp("", "workbook-benchmark-")
	if err != nil {
		return perf.Report{}, fmt.Errorf("create temporary fixture root: %w", err)
	}
	defer os.RemoveAll(fixtureRoot)

	fixtureSpec := perf.FixtureSpec{
		ActiveTasks:       options.tasks,
		OperationsPerTask: options.operations,
		ObjectFormat:      options.objectFormat,
	}
	runSpec := perf.RunSpec{
		WorkbookBinary: options.workbookBinary,
		Fixture:        fixtureSpec,
		Samples:        options.samples,
		CommandTimeout: options.timeout,
	}

	cold, err := perf.RunColdCLI(ctx, runSpec, filepath.Join(fixtureRoot, "cold"))
	if err != nil {
		return perf.Report{}, fmt.Errorf("run cold CLI scenarios: %w", err)
	}
	warm, err := perf.RunWarmHTTP(ctx, runSpec, filepath.Join(fixtureRoot, "warm"))
	if err != nil {
		return perf.Report{}, fmt.Errorf("run warm HTTP scenarios: %w", err)
	}

	repositoryFixture, err := perf.BuildFixture(ctx, filepath.Join(fixtureRoot, "repository"), fixtureSpec)
	if err != nil {
		return perf.Report{}, fmt.Errorf("build repository fixture: %w", err)
	}
	repositoryMetrics, repositoryScenarios, err := perf.MeasureRepository(
		ctx,
		options.workbookBinary,
		repositoryFixture.Root,
		options.timeout,
	)
	if err != nil {
		return perf.Report{}, fmt.Errorf("measure repository scenarios: %w", err)
	}

	scenarios := make([]perf.ScenarioResult, 0, len(cold)+len(warm)+len(repositoryScenarios))
	scenarios = append(scenarios, cold...)
	scenarios = append(scenarios, warm...)
	scenarios = append(scenarios, repositoryScenarios...)
	return perf.Report{
		Format:      perf.ReportFormat,
		Version:     perf.ReportVersion,
		Phase:       options.phase,
		GeneratedAt: time.Now().UTC(),
		Environment: environment,
		Fixture:     fixtureSpec,
		Targets: perf.Targets{
			WarmP95Milliseconds: 100,
			ColdP95Milliseconds: 200,
			BurstMilliseconds:   1000,
		},
		Scenarios:  scenarios,
		Repository: repositoryMetrics,
	}, nil
}

func benchmarkEnvironment(ctx context.Context, workbookBinary string) (perf.Environment, error) {
	gitVersion, err := commandOutput(ctx, "git", "--version")
	if err != nil {
		return perf.Environment{}, fmt.Errorf("read Git version: %w", err)
	}
	goVersion, err := commandOutput(ctx, "go", "version")
	if err != nil {
		return perf.Environment{}, fmt.Errorf("read Go version: %w", err)
	}
	versionOutput, err := commandOutput(ctx, workbookBinary, "version", "--json")
	if err != nil {
		return perf.Environment{}, fmt.Errorf("read Workbook version: %w", err)
	}
	var version workbookVersionResult
	if err := json.Unmarshal([]byte(versionOutput), &version); err != nil {
		return perf.Environment{}, fmt.Errorf("decode Workbook version: %w", err)
	}
	if version.Format != "workbook.result" || version.Version != 1 || version.Command != "version" ||
		version.Data.Version == "" || version.Data.Commit == "" {
		return perf.Environment{}, fmt.Errorf("decode Workbook version: unexpected result envelope")
	}
	return perf.Environment{
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		GitVersion:      gitVersion,
		GoVersion:       goVersion,
		WorkbookVersion: version.Data.Version,
		WorkbookCommit:  version.Data.Commit,
	}, nil
}

func commandOutput(ctx context.Context, binary string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, binary, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func writeReports(jsonPath, markdownPath string, report perf.Report) error {
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

func stageReport(target string, write func(io.Writer) error) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return "", err
	}
	temporary := file.Name()
	succeeded := false
	defer func() {
		if !succeeded {
			file.Close()
			os.Remove(temporary)
		}
	}()

	if err := write(file); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	succeeded = true
	return temporary, nil
}
