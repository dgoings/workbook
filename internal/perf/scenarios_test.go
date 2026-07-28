package perf

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

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
		if sample.ExitCode != 0 || sample.TimedOut || sample.Error != "" {
			t.Errorf("%s sample = %#v, want success", result.Name, sample)
		}
		if sample.GitProcesses < 1 {
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
