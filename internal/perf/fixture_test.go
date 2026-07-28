package perf

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/gitstore"
)

func TestBuildFixtureCreatesCompleteTipStatesWithoutReplay(t *testing.T) {
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			if objectFormat == "sha256" && !supportsObjectFormat(t, objectFormat) {
				t.Skip("Git does not support SHA-256 repositories")
			}

			spec := FixtureSpec{ActiveTasks: 3, OperationsPerTask: 4, ObjectFormat: objectFormat}
			fixture, err := BuildFixture(context.Background(), filepath.Join(t.TempDir(), "fixture"), spec)
			if err != nil {
				t.Fatal(err)
			}
			if len(fixture.TaskIDs) != 3 {
				t.Fatalf("task IDs = %d, want 3", len(fixture.TaskIDs))
			}

			repository, err := gitstore.Open(context.Background(), fixture.Root)
			if err != nil {
				t.Fatal(err)
			}
			for _, taskID := range fixture.TaskIDs {
				snapshot, err := repository.Get(context.Background(), fixture.Config, taskID)
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.State.LogicalClock != 4 || snapshot.State.Task.Title == "" {
					t.Fatalf("tip = %#v", snapshot.State)
				}
				output := runGit(t, fixture.Root, "rev-list", "--count", snapshot.Head)
				if strings.TrimSpace(output) != "4" {
					t.Fatalf("history count = %q, want 4", output)
				}
			}
		})
	}
}

func supportsObjectFormat(t *testing.T, objectFormat string) bool {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	command := exec.Command("git", "init", "--object-format="+objectFormat, probe)
	if _, err := command.CombinedOutput(); err != nil {
		return false
	}
	return true
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}
