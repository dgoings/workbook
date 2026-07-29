package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"syscall"
	"time"

	"github.com/dgoings/workbook/internal/perf"
)

const (
	invocationExitCode = 2
	failureExitCode    = 1
	commandWaitDelay   = 100 * time.Millisecond
)

type options struct {
	workbookBinary string
	tasks          int
	tombstones     int
	tombstonesSet  bool
	operations     int
	samples        int
	timeout        time.Duration
	objectFormat   string
	outputJSON     string
	outputMarkdown string
	phase          string
	scenarioFlags  stringListFlag
	scenarios      []string
}

type stringListFlag []string

func (values *stringListFlag) String() string {
	return strings.Join(*values, ",")
}

func (values *stringListFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
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
	flags.IntVar(&options.tasks, "tasks", 500, "total task refs")
	flags.IntVar(&options.tombstones, "tombstones", 0, "tombstoned task refs (default depends on --tasks)")
	flags.IntVar(&options.operations, "operations", 20, "operations per task")
	flags.IntVar(&options.samples, "samples", 1, "samples per scenario")
	flags.DurationVar(&options.timeout, "timeout", 60*time.Second, "per-command timeout")
	flags.StringVar(&options.objectFormat, "object-format", "sha1", "Git object format (sha1 or sha256)")
	flags.StringVar(&options.outputJSON, "output-json", "", "JSON report path")
	flags.StringVar(&options.outputMarkdown, "output-markdown", "", "Markdown report path")
	flags.StringVar(&options.phase, "phase", "baseline", "report phase (baseline or acceptance)")
	flags.Var(&options.scenarioFlags, "scenario", "benchmark scenario to run (repeatable)")
	return flags, options
}

func validateOptions(flags *flag.FlagSet, options *options) error {
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if options.workbookBinary == "" {
		return fmt.Errorf("--workbook is required")
	}
	workbookPath, err := exec.LookPath(options.workbookBinary)
	if err != nil {
		return fmt.Errorf("resolve --workbook: %w", err)
	}
	workbookPath, err = filepath.Abs(workbookPath)
	if err != nil {
		return fmt.Errorf("resolve --workbook: %w", err)
	}
	options.workbookBinary = workbookPath
	if options.tasks < 10 {
		return fmt.Errorf("--tasks must be at least 10")
	}
	options.tombstonesSet = false
	flags.Visit(func(flag *flag.Flag) {
		if flag.Name == "tombstones" {
			options.tombstonesSet = true
		}
	})
	if !options.tombstonesSet {
		if options.tasks >= 500 {
			options.tombstones = 25
		} else {
			options.tombstones = 1
		}
	}
	if options.tombstones < 0 {
		return fmt.Errorf("--tombstones must not be negative")
	}
	if options.tombstones > options.tasks {
		return fmt.Errorf("--tombstones must not exceed --tasks")
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
	if options.phase == "acceptance" {
		switch {
		case options.tasks < 500:
			return fmt.Errorf("acceptance requires at least 500 total tasks")
		case options.tombstones < 25:
			return fmt.Errorf("acceptance requires at least 25 tombstoned tasks")
		case options.operations < 20:
			return fmt.Errorf("acceptance requires at least 20 operations per task")
		case options.tasks-options.tombstones < 10:
			return fmt.Errorf("acceptance requires at least 10 active tasks")
		}
	}
	scenarios, err := perf.ResolveScenarios(options.scenarioFlags)
	if err != nil {
		return err
	}
	if containsRemoteScenario(scenarios) && (options.tasks < 500 || options.operations < 20) {
		return fmt.Errorf("remote scenarios require at least 500 tasks and 20 operations per task")
	}
	if containsValidationScenario(scenarios) && (options.tasks < 500 || options.operations < 20) {
		return fmt.Errorf("validation scenarios require at least 500 tasks and 20 operations per task")
	}
	options.scenarios = scenarios

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
	environment, err := benchmarkEnvironment(ctx, options.workbookBinary, options.timeout)
	if err != nil {
		return perf.Report{}, err
	}
	if options.phase == "acceptance" && environment.WorkbookCommit == "unknown" {
		return perf.Report{}, fmt.Errorf("acceptance requires a measured Workbook commit")
	}

	fixtureRoot, err := os.MkdirTemp("", "workbook-benchmark-")
	if err != nil {
		return perf.Report{}, fmt.Errorf("create temporary fixture root: %w", err)
	}
	defer os.RemoveAll(fixtureRoot)

	fixtureSpec := perf.FixtureSpec{
		TotalTasks:        options.tasks,
		ActiveTasks:       options.tasks - options.tombstones,
		TombstonedTasks:   options.tombstones,
		OperationsPerTask: options.operations,
		ObjectFormat:      options.objectFormat,
	}
	runSpec := perf.RunSpec{
		WorkbookBinary: options.workbookBinary,
		Fixture:        fixtureSpec,
		Samples:        options.samples,
		CommandTimeout: options.timeout,
	}

	var scenarios []perf.ScenarioResult
	var repositoryMetrics perf.RepositoryMetrics
	coldScenarios := selectedColdScenarioNames(options.scenarios)
	if len(coldScenarios) != 0 {
		cold, err := perf.RunColdCLI(ctx, runSpec, filepath.Join(fixtureRoot, "cold"), coldScenarios)
		if err != nil {
			return perf.Report{}, fmt.Errorf("run cold CLI scenarios: %w", err)
		}
		scenarios = append(scenarios, cold...)
	}
	warmScenarios := selectedWarmScenarioNames(options.scenarios)
	if len(warmScenarios) != 0 {
		warm, err := perf.RunWarmHTTP(ctx, runSpec, filepath.Join(fixtureRoot, "warm"), warmScenarios)
		if err != nil {
			return perf.Report{}, fmt.Errorf("run warm HTTP scenarios: %w", err)
		}
		scenarios = append(scenarios, warm...)
	}
	if hasRepositoryScenario(options.scenarios) {
		fixtureContext, cancelFixture := context.WithTimeout(ctx, options.timeout)
		repositoryFixture, err := perf.BuildFixture(fixtureContext, filepath.Join(fixtureRoot, "repository"), fixtureSpec)
		cancelFixture()
		if err != nil {
			return perf.Report{}, fmt.Errorf("build repository fixture: %w", err)
		}
		metrics, repositoryScenarios, err := perf.MeasureRepository(
			ctx,
			options.workbookBinary,
			repositoryFixture.Root,
			options.timeout,
		)
		if err != nil {
			return perf.Report{}, fmt.Errorf("measure repository scenarios: %w", err)
		}
		repositoryMetrics = metrics
		scenarios = append(scenarios, selectedScenarioResults(repositoryScenarios, options.scenarios)...)
	}
	remoteScenarios := selectedRemoteScenarioNames(options.scenarios)
	if len(remoteScenarios) != 0 {
		remote, err := perf.RunRemoteScenarios(ctx, runSpec, filepath.Join(fixtureRoot, "remote"), remoteScenarios)
		if err != nil {
			return perf.Report{}, fmt.Errorf("run remote sync scenarios: %w", err)
		}
		scenarios = append(scenarios, remote...)
	}
	validationScenarios := selectedValidationScenarioNames(options.scenarios)
	if len(validationScenarios) != 0 {
		validation, err := perf.RunValidationScenarios(ctx, runSpec, filepath.Join(fixtureRoot, "validation"), validationScenarios)
		if err != nil {
			return perf.Report{}, fmt.Errorf("run validation scenarios: %w", err)
		}
		scenarios = append(scenarios, validation...)
	}
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

func hasScenarioWithPrefix(scenarios []string, prefix string) bool {
	for _, scenario := range scenarios {
		if strings.HasPrefix(scenario, prefix) {
			return true
		}
	}
	return false
}

func selectedColdScenarioNames(scenarios []string) []string {
	cold := make([]string, 0, len(scenarios))
	for _, scenario := range scenarios {
		if strings.HasPrefix(scenario, "cli-") {
			cold = append(cold, scenario)
		}
	}
	return cold
}

func selectedWarmScenarioNames(scenarios []string) []string {
	warm := make([]string, 0, len(scenarios))
	for _, scenario := range scenarios {
		if strings.HasPrefix(scenario, "api-") {
			warm = append(warm, scenario)
		}
	}
	return warm
}

func hasRepositoryScenario(scenarios []string) bool {
	for _, scenario := range scenarios {
		if strings.HasPrefix(scenario, "projection-") || scenario == "sync-initial-local-bare" || scenario == "sync-unchanged-local-bare" {
			return true
		}
	}
	return false
}

func selectedScenarioResults(results []perf.ScenarioResult, selected []string) []perf.ScenarioResult {
	wanted := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		wanted[name] = struct{}{}
	}
	filtered := make([]perf.ScenarioResult, 0, len(results))
	for _, result := range results {
		if _, selected := wanted[result.Name]; selected {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

func containsRemoteScenario(scenarios []string) bool {
	return len(selectedRemoteScenarioNames(scenarios)) != 0
}

func containsValidationScenario(scenarios []string) bool {
	return len(selectedValidationScenarioNames(scenarios)) != 0
}

func selectedValidationScenarioNames(scenarios []string) []string {
	validation := make([]string, 0, len(scenarios))
	for _, scenario := range scenarios {
		switch scenario {
		case "validate-full-history", "validate-cached-unchanged", "validate-five-changed":
			validation = append(validation, scenario)
		}
	}
	return validation
}

func selectedRemoteScenarioNames(scenarios []string) []string {
	remote := make([]string, 0, len(scenarios))
	for _, scenario := range scenarios {
		switch scenario {
		case "sync-fresh-checkout", "sync-initial-publication", "sync-already-synchronized", "sync-small-changed-ref-set", "sync-divergent-tips", "sync-malformed-local-tip", "sync-malformed-remote-tip":
			remote = append(remote, scenario)
		}
	}
	return remote
}

func benchmarkEnvironment(ctx context.Context, workbookBinary string, commandTimeout time.Duration) (perf.Environment, error) {
	workbookChecksum, err := fileSHA256(workbookBinary)
	if err != nil {
		return perf.Environment{}, fmt.Errorf("hash Workbook binary: %w", err)
	}
	gitVersion, err := commandOutput(ctx, commandTimeout, "git", "--version")
	if err != nil {
		return perf.Environment{}, fmt.Errorf("read Git version: %w", err)
	}
	goVersion, err := commandOutput(ctx, commandTimeout, "go", "version")
	if err != nil {
		return perf.Environment{}, fmt.Errorf("read Go version: %w", err)
	}
	versionOutput, err := commandOutput(ctx, commandTimeout, workbookBinary, "version", "--json")
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
		OS:                   runtime.GOOS,
		Arch:                 runtime.GOARCH,
		GitVersion:           gitVersion,
		GoVersion:            goVersion,
		WorkbookVersion:      version.Data.Version,
		WorkbookCommit:       version.Data.Commit,
		WorkbookBinarySHA256: workbookChecksum,
	}, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func commandOutput(ctx context.Context, timeout time.Duration, binary string, args ...string) (string, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, binary, args...)
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
	output, err := command.CombinedOutput()
	if commandContext.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s %s timed out after %s", binary, strings.Join(args, " "), timeout)
	}
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
