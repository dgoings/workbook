package perf

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const coldCLITasksPerFixture = 17

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

// RunColdCLI builds deterministic fixtures and measures cold CLI mutations
// against an acceptance-sized baseline isolated by scenario and sample.
func RunColdCLI(ctx context.Context, spec RunSpec, fixtureRoot string) ([]ScenarioResult, error) {
	return runColdCLI(ctx, spec, fixtureRoot, scenarioDependencies{
		buildFixture:   BuildFixture,
		measureCommand: MeasureCommand,
	})
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
	tasks, err := allocateColdCLITasks(fixture.TaskIDs)
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
		independent: append([]string(nil), taskIDs[7:17]...),
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
