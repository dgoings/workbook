package perf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dgoings/workbook/internal/historyvalidation"
)

type validationResultEnvelope struct {
	Format  string                   `json:"format"`
	Version int                      `json:"version"`
	Command string                   `json:"command"`
	Data    historyvalidation.Result `json:"data"`
}

type validationScenarioDefinition struct {
	name string
	args []string
}

var validationScenarioDefinitions = []validationScenarioDefinition{
	{name: "validate-full-history", args: []string{"validate", "--full", "--json"}},
	{name: "validate-cached-unchanged", args: []string{"validate", "--json"}},
	{name: "validate-five-changed", args: []string{"validate", "--json"}},
}

type validationScenarioDependencies struct {
	buildFixture   func(context.Context, string, FixtureSpec) (Fixture, error)
	runSetup       func(context.Context, CommandSpec) CommandMeasurement
	measureCommand func(context.Context, CommandSpec) CommandMeasurement
}

// RunValidationScenarios measures only the selected semantic history
// validation paths. Each measured sample receives an independent fixture.
func RunValidationScenarios(ctx context.Context, spec RunSpec, fixtureRoot string, selected []string) ([]ScenarioResult, error) {
	return runValidationScenarios(ctx, spec, fixtureRoot, selected, validationScenarioDependencies{
		buildFixture: func(ctx context.Context, root string, fixture FixtureSpec) (Fixture, error) {
			return buildFixtureWithinTimeout(ctx, root, fixture, spec.CommandTimeout)
		},
		runSetup:       runValidationSetupCommand,
		measureCommand: MeasureCommandOutput,
	})
}

func runValidationScenarios(ctx context.Context, spec RunSpec, fixtureRoot string, selected []string, dependencies validationScenarioDependencies) ([]ScenarioResult, error) {
	if spec.WorkbookBinary == "" {
		return nil, fmt.Errorf("workbook binary is required")
	}
	if spec.Samples < 1 {
		return nil, fmt.Errorf("samples must be positive")
	}
	if spec.CommandTimeout <= 0 {
		return nil, fmt.Errorf("command timeout must be positive")
	}
	if fixtureRoot == "" {
		return nil, fmt.Errorf("fixture root is required")
	}
	if dependencies.buildFixture == nil || dependencies.runSetup == nil || dependencies.measureCommand == nil {
		return nil, fmt.Errorf("validation scenario dependencies are required")
	}
	definitions, err := selectValidationScenarioDefinitions(selected)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(fixtureRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create validation fixture root: %w", err)
	}

	results := make([]ScenarioResult, 0, len(definitions))
	for _, definition := range definitions {
		result := ScenarioResult{
			Name:    definition.name,
			Surface: "history-validation",
			Samples: make([]Sample, spec.Samples),
		}
		for sample := range spec.Samples {
			fixtureContext, cancel := context.WithTimeout(ctx, spec.CommandTimeout)
			fixture, err := dependencies.buildFixture(fixtureContext, filepath.Join(fixtureRoot, definition.name, fmt.Sprintf("sample-%03d", sample+1)), spec.Fixture)
			cancel()
			if err != nil {
				return nil, fmt.Errorf("build %s sample %d fixture: %w", definition.name, sample+1, err)
			}
			if err := prepareValidationScenario(ctx, definition.name, fixture, spec, dependencies.runSetup); err != nil {
				return nil, fmt.Errorf("prepare %s sample %d: %w", definition.name, sample+1, err)
			}
			measurement := dependencies.measureCommand(ctx, CommandSpec{
				Binary:    spec.WorkbookBinary,
				Args:      append([]string(nil), definition.args...),
				Directory: fixture.Root,
				Timeout:   spec.CommandTimeout,
			})
			if !measurement.Sample.TimedOut {
				if err := verifyValidationMeasurement(definition.name, measurement, spec.Fixture); err != nil {
					return nil, fmt.Errorf("verify %s sample %d: %w", definition.name, sample+1, err)
				}
			}
			result.Samples[sample] = measurement.Sample
		}
		result.Summary = Summarize(result.Samples)
		results = append(results, result)
	}
	return results, nil
}

func validationScenarioNames() []string {
	names := make([]string, len(validationScenarioDefinitions))
	for index, definition := range validationScenarioDefinitions {
		names[index] = definition.name
	}
	return names
}

func selectValidationScenarioDefinitions(selected []string) ([]validationScenarioDefinition, error) {
	requested := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		if _, duplicate := requested[name]; duplicate {
			return nil, fmt.Errorf("duplicate validation scenario %q", name)
		}
		requested[name] = struct{}{}
	}
	definitions := make([]validationScenarioDefinition, 0, len(selected))
	for _, definition := range validationScenarioDefinitions {
		if _, wanted := requested[definition.name]; wanted {
			definitions = append(definitions, definition)
			delete(requested, definition.name)
		}
	}
	if len(requested) != 0 {
		for _, name := range selected {
			if _, unknown := requested[name]; unknown {
				return nil, fmt.Errorf("unknown validation scenario %q", name)
			}
		}
	}
	return definitions, nil
}

func prepareValidationScenario(ctx context.Context, scenario string, fixture Fixture, spec RunSpec, runSetup func(context.Context, CommandSpec) CommandMeasurement) error {
	if scenario == "validate-full-history" {
		return nil
	}
	if err := requireSuccessfulSetup(scenario, runSetup(ctx, CommandSpec{
		Binary: spec.WorkbookBinary, Args: []string{"validate", "--json"}, Directory: fixture.Root, Timeout: spec.CommandTimeout,
	})); err != nil {
		return err
	}
	if scenario != "validate-five-changed" {
		return nil
	}
	if len(fixture.ActiveTaskIDs) < 5 {
		return fmt.Errorf("fixture has %d active tasks, want at least 5", len(fixture.ActiveTaskIDs))
	}
	for index, taskID := range fixture.ActiveTaskIDs[:5] {
		if err := requireSuccessfulSetup(scenario, runSetup(ctx, CommandSpec{
			Binary:    spec.WorkbookBinary,
			Args:      []string{"update", taskID, "--description", fmt.Sprintf("validation benchmark change %d", index+1), "--json"},
			Directory: fixture.Root,
			Timeout:   spec.CommandTimeout,
		})); err != nil {
			return err
		}
	}
	return nil
}

func requireSuccessfulSetup(name string, measurement CommandMeasurement) error {
	if sampleSucceeded(measurement.Sample) {
		return nil
	}
	if measurement.Sample.TimedOut {
		return fmt.Errorf("%s setup timed out: %s", name, measurement.Sample.Error)
	}
	if measurement.Sample.Error != "" {
		return fmt.Errorf("%s setup failed with exit code %d: %s", name, measurement.Sample.ExitCode, measurement.Sample.Error)
	}
	return fmt.Errorf("%s setup failed with exit code %d", name, measurement.Sample.ExitCode)
}

func verifyValidationMeasurement(scenario string, measurement CommandMeasurement, fixture FixtureSpec) error {
	if measurement.Sample.ExitCode < 0 {
		return fmt.Errorf("command did not produce an exit code: %s", measurement.Sample.Error)
	}
	if measurement.Sample.ExitCode != 0 || measurement.Sample.Error != "" {
		return fmt.Errorf("command failed with exit code %d: %s", measurement.Sample.ExitCode, measurement.Sample.Error)
	}
	return verifyValidationResultOutput(scenario, measurement.Stdout, fixture)
}

// verifyValidationResultOutput checks one measured `workbook validate` result
// against the scenario's exact literal oracle for the measured fixture. Every
// measured validation, whatever family runs it, is held to this oracle: an exit
// code alone cannot tell a complete audit from one that validated nothing.
func verifyValidationResultOutput(scenario string, stdout []byte, fixture FixtureSpec) error {
	var envelope validationResultEnvelope
	if err := json.Unmarshal(stdout, &envelope); err != nil {
		return fmt.Errorf("decode validate result: %w", err)
	}
	if envelope.Format != workbookResultFormat || envelope.Version != workbookJSONVersion || envelope.Command != "validate" {
		return fmt.Errorf("unexpected validate result envelope")
	}
	want := expectedValidationResult(scenario, fixture)
	got := envelope.Data
	if got.ValidatorVersion != 1 || got.Full != want.Full || got.TaskCount != fixture.TotalTasks ||
		got.TasksChecked != want.TasksChecked || got.CommitsChecked != want.CommitsChecked || got.CacheHits != want.CacheHits ||
		got.Valid != fixture.TotalTasks || got.Invalid != 0 || got.Pending != 0 || len(got.Failures) != 0 {
		return fmt.Errorf("validate result does not match %s literal oracle: validatorVersion=%d full=%t taskCount=%d tasksChecked=%d commitsChecked=%d cacheHits=%d valid=%d invalid=%d pending=%d failures=%d", scenario, got.ValidatorVersion, got.Full, got.TaskCount, got.TasksChecked, got.CommitsChecked, got.CacheHits, got.Valid, got.Invalid, got.Pending, len(got.Failures))
	}
	return nil
}

func expectedValidationResult(scenario string, fixture FixtureSpec) historyvalidation.Result {
	result := historyvalidation.Result{ValidatorVersion: 1, TaskCount: fixture.TotalTasks, Valid: fixture.TotalTasks, Failures: []historyvalidation.Failure{}}
	switch scenario {
	case "validate-full-history":
		result.Full = true
		result.TasksChecked = fixture.TotalTasks
		result.CommitsChecked = fixture.TotalTasks * fixture.OperationsPerTask
	case "validate-cached-unchanged":
		result.CacheHits = fixture.TotalTasks
	case "validate-five-changed":
		result.TasksChecked = 5
		result.CommitsChecked = 5
		result.CacheHits = fixture.TotalTasks - 5
	}
	return result
}

// runValidationSetupCommand intentionally does not call MeasureCommandOutput:
// fixture preparation is outside the measured product command and Trace2 count.
func runValidationSetupCommand(ctx context.Context, spec CommandSpec) CommandMeasurement {
	measurement := CommandMeasurement{Sample: Sample{ExitCode: -1}}
	commandContext, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, spec.Binary, spec.Args...)
	command.Dir = spec.Directory
	command.Env = append(os.Environ(), spec.Environment...)
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
	stdout, stderr, err := runCommandOutput(command)
	measurement.Stdout = stdout
	measurement.Stderr = stderr
	measurement.Sample.Duration = time.Since(startedAt)
	// Reap only once the duration is stamped, and before the successful return,
	// so a setup command that exits cleanly still gives up its descendants.
	ReapProcessGroup(command.Process)
	if err == nil {
		measurement.Sample.ExitCode = 0
		return measurement
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		measurement.Sample.ExitCode = exitError.ExitCode()
	}
	measurement.Sample.TimedOut = commandContext.Err() == context.DeadlineExceeded
	measurement.Sample.Error = stderrSummary(string(stderr), err)
	return measurement
}

func runCommandOutput(command *exec.Cmd) ([]byte, []byte, error) {
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return []byte(stdout.String()), []byte(stderr.String()), err
}
