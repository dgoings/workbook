package perf

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/perf/proctest"
)

func TestMeasureCommandCountsGitProcesses(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: gitPath, Args: []string{"--version"}, Directory: t.TempDir(),
		Timeout: 5 * time.Second,
	})
	if sample.ExitCode != 0 || sample.TimedOut || sample.GitProcesses != 1 {
		t.Fatalf("sample = %#v", sample)
	}
}

func TestMeasureCommandRecordsTimeout(t *testing.T) {
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: proctest.Shell, Args: []string{"-c", proctest.BusyLoopWhileTestBinaryLives()},
		Directory: t.TempDir(), Timeout: 20 * time.Millisecond,
	})
	if !sample.TimedOut || sample.ExitCode == 0 {
		t.Fatalf("sample = %#v", sample)
	}
}

func TestMeasureCommandRecordsExitCodeAndSingleLineStderr(t *testing.T) {
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: "/bin/sh", Args: []string{"-c", "printf 'first failure\\nsecond failure\\n' >&2; exit 7"},
		Directory: t.TempDir(), Timeout: 5 * time.Second,
	})
	if sample.ExitCode != 7 || sample.TimedOut || sample.Error != "first failure" {
		t.Fatalf("sample = %#v", sample)
	}
}

func TestMeasureCommandOutputPreservesStreamsAndCompatibilityWrapper(t *testing.T) {
	got := MeasureCommandOutput(context.Background(), CommandSpec{
		Binary:    "/bin/sh",
		Args:      []string{"-c", "printf stdout; printf stderr >&2; exit 7"},
		Directory: t.TempDir(),
		Timeout:   time.Second,
	})

	if string(got.Stdout) != "stdout" || string(got.Stderr) != "stderr" {
		t.Fatalf("measurement = %#v", got)
	}
	if got.Sample.ExitCode != 7 || got.Sample.TimedOut || got.Sample.Error != "stderr" ||
		got.Sample.Duration <= 0 || got.Sample.GitProcesses != 0 {
		t.Fatalf("sample = %#v", got.Sample)
	}
}

func TestMeasureCommandPassesCallerEnvironment(t *testing.T) {
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: "/bin/sh", Args: []string{"-c", "test \"$WORKBOOK_PERF_TEST_VALUE\" = present"},
		Directory: t.TempDir(), Environment: []string{"WORKBOOK_PERF_TEST_VALUE=present"}, Timeout: 5 * time.Second,
	})
	if sample.ExitCode != 0 || sample.TimedOut || sample.Error != "" {
		t.Fatalf("sample = %#v", sample)
	}
}

func TestMeasureCommandTerminatesTimedOutDescendant(t *testing.T) {
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	proctest.ReapRecordedProcessGroup(t, childPIDPath)
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: proctest.Shell, Args: proctest.BusyLeaderArgs(childPIDPath),
		Directory: t.TempDir(), Timeout: 100 * time.Millisecond,
	})
	if !sample.TimedOut {
		t.Fatalf("sample = %#v", sample)
	}
	proctest.RequireDescendantTerminated(t, childPIDPath)
}

// TestMeasureCommandReapsDescendantOfCommandThatExits covers the other way a
// measured command leaves a descendant behind: the command itself finishes, so
// the timeout cancellation that kills the process group never runs, and a
// background descendant it started keeps burning a core after the measurement
// reported a clean exit.
func TestMeasureCommandReapsDescendantOfCommandThatExits(t *testing.T) {
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	proctest.ReapRecordedProcessGroup(t, childPIDPath)
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: proctest.Shell, Args: proctest.ExitingLeaderArgs(childPIDPath),
		Directory: t.TempDir(), Timeout: 30 * time.Second,
	})
	if sample.ExitCode != 0 || sample.TimedOut || sample.Error != "" {
		t.Fatalf("sample = %#v", sample)
	}
	proctest.RequireDescendantTerminated(t, childPIDPath)
}

func TestTraceCursorCountsOnlyNewGitProcesses(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "trace.json")
	if err := os.WriteFile(tracePath, []byte("{\"event\":\"start\",\"argv\":[\"git\",\"status\"]}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cursor, err := OpenTraceCursor(tracePath)
	if err != nil {
		t.Fatal(err)
	}

	appendTrace(t, tracePath, "{\"event\":\"start\",\"argv\":[\"git\",\"log\"]}")
	if got, err := cursor.CountNewGitProcesses(); err != nil || got != 0 {
		t.Fatalf("partial count = %d, %v; want 0, nil", got, err)
	}

	appendTrace(t, tracePath, "\n{\"event\":\"start\",\"argv\":[]}\n")
	if got, err := cursor.CountNewGitProcesses(); err != nil || got != 1 {
		t.Fatalf("first count = %d, %v; want 1, nil", got, err)
	}

	appendTrace(t, tracePath, "{\"event\":\"start\",\"argv\":[\"git\",\"show\"]}\n")
	if got, err := cursor.CountNewGitProcesses(); err != nil || got != 1 {
		t.Fatalf("second count = %d, %v; want 1, nil", got, err)
	}
	if got, err := cursor.CountNewGitProcesses(); err != nil || got != 0 {
		t.Fatalf("repeated count = %d, %v; want 0, nil", got, err)
	}
}

func appendTrace(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
