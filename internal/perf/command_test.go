package perf

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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
		Binary: "/bin/sh", Args: []string{"-c", "while :; do :; done"},
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
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: "/bin/sh", Args: []string{
			"-c",
			"sh -c 'echo $$ > \"$1\"; while :; do :; done' sh \"$1\" & while [ ! -s \"$1\" ]; do :; done; while :; do :; done",
			"sh",
			childPIDPath,
		},
		Directory: t.TempDir(), Timeout: 100 * time.Millisecond,
	})
	if !sample.TimedOut {
		t.Fatalf("sample = %#v", sample)
	}
	pidText, err := os.ReadFile(childPIDPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidText)))
	if err != nil {
		t.Fatal(err)
	}
	// MeasureCommand kills the whole process group, but a terminated
	// descendant stays visible to kill(2) until the init process it was
	// reparented to reaps it. Poll instead of sampling once. This cannot mask a
	// descendant that genuinely survived, because that descendant busy-loops
	// forever and never reaches a terminated state.
	deadline := time.Now().Add(30 * time.Second)
	for !descendantTerminated(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("descendant process %d still running after the process group was killed", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// descendantTerminated reports whether a process has stopped running, counting
// a not-yet-reaped zombie as terminated.
func descendantTerminated(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	status, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		// Without procfs, kill(2) is the only available signal.
		return false
	}
	// The state follows the parenthesised command name, which may itself
	// contain spaces or brackets, so scan from the final closing parenthesis.
	tail := string(status)
	if end := strings.LastIndex(tail, ")"); end >= 0 {
		if fields := strings.Fields(tail[end+1:]); len(fields) > 0 {
			return fields[0] == "Z"
		}
	}
	return false
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
