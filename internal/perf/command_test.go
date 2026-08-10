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
		Binary: "/bin/sh", Args: []string{"-c", busyLoopWhileTestBinaryLives()},
		Directory: t.TempDir(), Timeout: 20 * time.Millisecond,
	})
	if !sample.TimedOut || sample.ExitCode == 0 {
		t.Fatalf("sample = %#v", sample)
	}
}

// testBinaryAlive returns a shell condition, built only from shell builtins so
// it adds no process churn to a loop, that succeeds while the process running
// these tests is still alive.
func testBinaryAlive() string {
	return "kill -0 " + strconv.Itoa(os.Getpid()) + " 2>/dev/null"
}

// busyLoopWhileTestBinaryLives returns a shell loop that spins a core until the
// process running these tests exits.
//
// These helpers have to burn CPU until the code under test terminates them, but
// a plain `while :; do :; done` outlives an interrupted run. MeasureCommandOutput
// deliberately gives the measured command its own process group, so a signal
// aimed at the test runner's process group - a terminal interrupt, a CI job
// wrapper signalling the group it started, a supervisor killing the run - never
// reaches the helper. The test binary dies before it can cancel the measurement,
// the helper is reparented to init, and it spins a core until somebody notices.
// Ending the loop when the process that started it is gone bounds the damage to
// one loop iteration no matter how the run dies.
func busyLoopWhileTestBinaryLives() string {
	return "while " + testBinaryAlive() + "; do :; done"
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
	reapRecordedProcessGroup(t, childPIDPath)
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: "/bin/sh", Args: []string{
			"-c",
			"sh -c 'echo $$ > \"$1\"; " + busyLoopWhileTestBinaryLives() + "' sh \"$1\" & " +
				"while [ ! -s \"$1\" ] && " + testBinaryAlive() + "; do :; done; " +
				busyLoopWhileTestBinaryLives(),
			"sh",
			childPIDPath,
		},
		Directory: t.TempDir(), Timeout: 100 * time.Millisecond,
	})
	if !sample.TimedOut {
		t.Fatalf("sample = %#v", sample)
	}
	requireDescendantTerminated(t, childPIDPath)
}

// TestMeasureCommandReapsDescendantOfCommandThatExits covers the other way a
// measured command leaves a descendant behind: the command itself finishes, so
// the timeout cancellation that kills the process group never runs, and a
// background descendant it started keeps burning a core after the measurement
// reported a clean exit.
func TestMeasureCommandReapsDescendantOfCommandThatExits(t *testing.T) {
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	reapRecordedProcessGroup(t, childPIDPath)
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: "/bin/sh", Args: []string{
			"-c",
			// The descendant drops the inherited output pipes so the leader's
			// exit is the only thing this measures.
			"sh -c 'echo $$ > \"$1\"; " + busyLoopWhileTestBinaryLives() + "' sh \"$1\" >/dev/null 2>&1 & " +
				"while [ ! -s \"$1\" ] && " + testBinaryAlive() + "; do :; done",
			"sh",
			childPIDPath,
		},
		Directory: t.TempDir(), Timeout: 30 * time.Second,
	})
	if sample.ExitCode != 0 || sample.TimedOut || sample.Error != "" {
		t.Fatalf("sample = %#v", sample)
	}
	requireDescendantTerminated(t, childPIDPath)
}

// reapRecordedProcessGroup kills whatever a helper recorded in pidPath once the
// test ends, however it ends. Registering it before the measurement runs keeps
// the reap independent of the test body, so a failed assertion or a panic still
// leaves a dead helper behind rather than a spinning one, and it never trusts
// the code under test to have done the killing.
func reapRecordedProcessGroup(t *testing.T, pidPath string) {
	t.Helper()
	t.Cleanup(func() {
		pid, ok := recordedHelperPID(pidPath)
		if !ok {
			return
		}
		// A recorded pid is only safe to signal while it still names the helper
		// itself: the helper is normally already dead by now, and the operating
		// system is free to hand its pid to an unrelated process. The pid file
		// lives in this test's temporary directory, so its path appears in the
		// helper's arguments and nowhere else.
		if !strings.Contains(processCommandLine(pid), pidPath) {
			return
		}
		// Signal the helper's whole process group first, so a descendant it
		// spawned dies with it, then the process itself in case its group is
		// already gone.
		if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 0 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})
}

func recordedHelperPID(pidPath string) (int, bool) {
	pidText, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidText)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// processCommandLine returns a process's arguments, or an empty string when the
// process is gone or cannot be inspected.
func processCommandLine(pid int) string {
	output, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return string(output)
}

// requireDescendantTerminated fails unless the descendant a helper recorded has
// stopped running. MeasureCommand kills the whole process group, but a
// terminated descendant stays visible to kill(2) until the init process it was
// reparented to reaps it, so poll instead of sampling once. Polling cannot mask
// a descendant that genuinely survived, because that descendant busy-loops for
// as long as this process lives and never reaches a terminated state.
func requireDescendantTerminated(t *testing.T, pidPath string) {
	t.Helper()
	pid, ok := recordedHelperPID(pidPath)
	if !ok {
		t.Fatalf("helper recorded no usable pid in %s", pidPath)
	}
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
