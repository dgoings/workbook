package perf

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const coldCLITasksPerSample = 17

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

// RunColdCLI builds a deterministic fixture and measures cold CLI mutations
// against disjoint fixture tasks.
func RunColdCLI(ctx context.Context, spec RunSpec, fixtureRoot string) ([]ScenarioResult, error) {
	if spec.Samples < 1 {
		return nil, fmt.Errorf("samples must be positive")
	}
	if spec.CommandTimeout <= 0 {
		return nil, fmt.Errorf("command timeout must be positive")
	}
	if fixtureRoot == "" {
		return nil, fmt.Errorf("fixture root is required")
	}

	fixture, err := BuildFixture(ctx, fixtureRoot, spec.Fixture)
	if err != nil {
		return nil, fmt.Errorf("build fixture: %w", err)
	}
	tasks, err := allocateColdCLITasks(fixture.TaskIDs, spec.Samples)
	if err != nil {
		return nil, err
	}

	results := []ScenarioResult{
		measureColdCLI(ctx, spec, fixture.Root, "cli-create", func(sample int) []string {
			return []string{
				"create", fmt.Sprintf("Benchmark created task %d", sample+1),
				"--status", "ready", "--priority", "high", "--json",
			}
		}),
		measureColdCLI(ctx, spec, fixture.Root, "cli-delete", func(sample int) []string {
			return []string{"delete", tasks[sample].delete, "--json"}
		}),
		measureColdCLI(ctx, spec, fixture.Root, "cli-depend", func(sample int) []string {
			return []string{"depend", tasks[sample].dependent, tasks[sample].dependency, "--json"}
		}),
		measureColdCLI(ctx, spec, fixture.Root, "cli-free", func(sample int) []string {
			return []string{"free", tasks[sample].dependent, tasks[sample].dependency, "--json"}
		}),
		measureColdCLI(ctx, spec, fixture.Root, "cli-move", func(sample int) []string {
			return []string{"move", tasks[sample].move, "--before", tasks[sample].moveAnchor, "--json"}
		}),
		measureColdCLI(ctx, spec, fixture.Root, "cli-restore", func(sample int) []string {
			return []string{"restore", tasks[sample].delete, "--json"}
		}),
		measureColdCLI(ctx, spec, fixture.Root, "cli-update", func(sample int) []string {
			return []string{"update", tasks[sample].update, "--status", "ready", "--json"}
		}),
		measureColdCLIBurst(spec, "cli-burst-independent-10", func(sample int) Sample {
			return measureIndependentBurst(ctx, spec, fixture.Root, tasks[sample].independent)
		}),
		measureColdCLIBurst(spec, "cli-burst-same-task-10", func(sample int) Sample {
			return measureSameTaskBurst(ctx, spec, fixture.Root, tasks[sample].sameBurst)
		}),
	}
	return results, nil
}

func allocateColdCLITasks(taskIDs []string, samples int) ([]coldCLITasks, error) {
	required := samples * coldCLITasksPerSample
	if len(taskIDs) < required {
		return nil, fmt.Errorf("fixture has %d tasks, need %d for %d cold CLI samples", len(taskIDs), required, samples)
	}

	allocations := make([]coldCLITasks, samples)
	next := 0
	for sample := range samples {
		allocation := &allocations[sample]
		allocation.update = taskIDs[next]
		next++
		allocation.delete = taskIDs[next]
		next++
		allocation.moveAnchor = taskIDs[next]
		next++
		allocation.move = taskIDs[next]
		next++
		allocation.dependent = taskIDs[next]
		next++
		allocation.dependency = taskIDs[next]
		next++
		allocation.sameBurst = taskIDs[next]
		next++
		allocation.independent = append([]string(nil), taskIDs[next:next+10]...)
		next += 10
	}
	return allocations, nil
}

func measureColdCLI(ctx context.Context, spec RunSpec, directory, name string, args func(int) []string) ScenarioResult {
	result := ScenarioResult{Name: name, Surface: "cold-cli", Samples: make([]Sample, spec.Samples)}
	for sample := range spec.Samples {
		result.Samples[sample] = MeasureCommand(ctx, CommandSpec{
			Binary:    spec.WorkbookBinary,
			Args:      args(sample),
			Directory: directory,
			Timeout:   spec.CommandTimeout,
		})
	}
	result.Summary = Summarize(result.Samples)
	return result
}

func measureColdCLIBurst(spec RunSpec, name string, measure func(int) Sample) ScenarioResult {
	result := ScenarioResult{Name: name, Surface: "cold-cli", Samples: make([]Sample, spec.Samples)}
	for sample := range spec.Samples {
		result.Samples[sample] = measure(sample)
	}
	result.Summary = Summarize(result.Samples)
	return result
}

func measureSameTaskBurst(ctx context.Context, spec RunSpec, directory, taskID string) Sample {
	startedAt := time.Now()
	members := make([]Sample, 10)
	for command := range members {
		status := "ready"
		if command%2 == 1 {
			status = "in-progress"
		}
		members[command] = MeasureCommand(ctx, CommandSpec{
			Binary:    spec.WorkbookBinary,
			Args:      []string{"update", taskID, "--status", status, "--json"},
			Directory: directory,
			Timeout:   spec.CommandTimeout,
		})
	}
	return aggregateBurst(time.Since(startedAt), members)
}

func measureIndependentBurst(ctx context.Context, spec RunSpec, directory string, taskIDs []string) Sample {
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
			members[index] = MeasureCommand(ctx, CommandSpec{
				Binary:    spec.WorkbookBinary,
				Args:      []string{"update", taskID, "--status", "ready", "--json"},
				Directory: directory,
				Timeout:   spec.CommandTimeout,
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
