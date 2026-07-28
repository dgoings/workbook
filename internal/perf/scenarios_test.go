package perf

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestRunColdCLIIsolatesScenarioSamplesAndRunsTenCommandBursts(t *testing.T) {
	var mutex sync.Mutex
	var fixtureRoots []string
	measurements := make(map[string][]CommandSpec)
	dependencies := scenarioDependencies{
		buildFixture: func(_ context.Context, root string, spec FixtureSpec) (Fixture, error) {
			mutex.Lock()
			fixtureRoots = append(fixtureRoots, root)
			mutex.Unlock()
			taskIDs := make([]string, spec.ActiveTasks)
			for index := range taskIDs {
				taskIDs[index] = fmt.Sprintf("WB-%026d", index)
			}
			return Fixture{Root: root, TaskIDs: taskIDs}, nil
		},
		measureCommand: func(_ context.Context, spec CommandSpec) Sample {
			mutex.Lock()
			measurements[spec.Directory] = append(measurements[spec.Directory], spec)
			mutex.Unlock()
			return Sample{ExitCode: 0, GitProcesses: 1}
		},
	}
	spec := RunSpec{
		WorkbookBinary: "workbook",
		Fixture: FixtureSpec{
			ActiveTasks:       40,
			OperationsPerTask: 4,
			ObjectFormat:      "sha1",
		},
		Samples:        2,
		CommandTimeout: time.Second,
	}
	fixtureRoot := t.TempDir()

	results, err := runColdCLI(context.Background(), spec, fixtureRoot, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if len(result.Samples) != 2 {
			t.Errorf("%s samples = %d, want 2", result.Name, len(result.Samples))
		}
	}

	if got, want := len(fixtureRoots), 14; got != want {
		t.Fatalf("fixture builds = %d, want %d", got, want)
	}
	uniqueRoots := make(map[string]struct{}, len(fixtureRoots))
	for _, root := range fixtureRoots {
		uniqueRoots[root] = struct{}{}
	}
	if got, want := len(uniqueRoots), 14; got != want {
		t.Fatalf("unique fixture roots = %d, want %d", got, want)
	}

	wantCommandsPerGroup := map[string]int{
		"create":            1,
		"delete-restore":    2,
		"depend-free":       2,
		"move":              1,
		"update":            1,
		"burst-independent": 10,
		"burst-same-task":   10,
	}
	for sample := 1; sample <= 2; sample++ {
		for group, want := range wantCommandsPerGroup {
			directory := filepath.Join(fixtureRoot, fmt.Sprintf("sample-%03d", sample), group)
			commands := measurements[directory]
			if got := len(commands); got != want {
				t.Errorf("sample %d %s commands = %d, want %d", sample, group, got, want)
			}
		}
	}

	for directory, commands := range measurements {
		switch filepath.Base(directory) {
		case "burst-independent":
			targets := make(map[string]struct{}, len(commands))
			for _, command := range commands {
				targets[command.Args[1]] = struct{}{}
			}
			if got, want := len(targets), 10; got != want {
				t.Errorf("%s distinct targets = %d, want %d", directory, got, want)
			}
		case "burst-same-task":
			targets := make(map[string]struct{}, len(commands))
			for _, command := range commands {
				targets[command.Args[1]] = struct{}{}
			}
			if got, want := len(targets), 1; got != want {
				t.Errorf("%s distinct targets = %d, want %d", directory, got, want)
			}
		}
	}
}

func TestColdCLISampleFailureAllowsTimeoutsAndRejectsOtherFailures(t *testing.T) {
	tests := []struct {
		name   string
		sample Sample
		want   bool
	}{
		{name: "success", sample: Sample{ExitCode: 0}, want: false},
		{name: "timeout", sample: Sample{ExitCode: -1, TimedOut: true, Error: "signal: killed"}, want: false},
		{name: "nonzero exit", sample: Sample{ExitCode: 2, Error: "invalid invocation"}, want: true},
		{name: "immediate error", sample: Sample{ExitCode: 0, Error: "exec format error"}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := coldCLISampleFailed(test.sample); got != test.want {
				t.Fatalf("coldCLISampleFailed(%#v) = %t, want %t", test.sample, got, test.want)
			}
		})
	}
}

func coldCLISampleFailed(sample Sample) bool {
	return !sample.TimedOut && (sample.ExitCode != 0 || sample.Error != "")
}

func TestRunColdCLI(t *testing.T) {
	binary := buildWorkbookBinary(t)
	spec := RunSpec{
		WorkbookBinary: binary,
		Fixture: FixtureSpec{
			ActiveTasks:       40,
			OperationsPerTask: 4,
			ObjectFormat:      "sha1",
		},
		Samples:        1,
		CommandTimeout: 60 * time.Second,
	}

	results, err := RunColdCLI(context.Background(), spec, filepath.Join(t.TempDir(), "fixture"))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
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
	got := make([]string, len(results))
	for i, result := range results {
		got[i] = result.Name
		if result.Surface != "cold-cli" {
			t.Errorf("%s surface = %q, want cold-cli", result.Name, result.Surface)
		}
		if len(result.Samples) != 1 {
			t.Errorf("%s samples = %d, want 1", result.Name, len(result.Samples))
			continue
		}
		sample := result.Samples[0]
		if coldCLISampleFailed(sample) {
			t.Errorf("%s sample = %#v, want success or timeout", result.Name, sample)
			continue
		}
		if !sample.TimedOut && sample.GitProcesses < 1 {
			t.Errorf("%s Git processes = %d, want at least 1", result.Name, sample.GitProcesses)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scenario names = %#v, want %#v", got, want)
	}
}

func buildWorkbookBinary(t *testing.T) string {
	t.Helper()
	root := filepath.Clean(filepath.Join("..", ".."))
	binary := filepath.Join(t.TempDir(), "workbook")
	command := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "./cmd/workbook")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build workbook: %v\n%s", err, output)
	}
	return binary
}
