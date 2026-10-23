package perf

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/perf/proctest"
)

func TestMeasureCommandCountsGitProcesses(t *testing.T) {
	t.Parallel()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: gitPath, Args: []string{"--version"}, Directory: t.TempDir(),
		// Hang guard, not a measurement. Every budget in this file that bounds a
		// command expected to finish is thirty seconds for the same reason: the
		// command is one process spawn that answers in milliseconds, the test
		// asserts the sample's contents and never its elapsed time, and a bare
		// whole-tree run keeps all of this machine's cores saturated, where a
		// tight budget arrives as a `signal: killed` instead of an assertion.
		// The two budgets that stay narrow — the ones the tests below require to
		// expire — are the subject rather than a guard.
		Timeout: 30 * time.Second,
	})
	if sample.ExitCode != 0 || sample.TimedOut || sample.GitProcesses != 1 {
		t.Fatalf("sample = %#v", sample)
	}
}

func TestMeasureCommandRecordsTimeout(t *testing.T) {
	t.Parallel()
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: proctest.Shell, Args: []string{"-c", proctest.BusyLoopWhileTestBinaryLives()},
		Directory: t.TempDir(), Timeout: 20 * time.Millisecond,
	})
	if !sample.TimedOut || sample.ExitCode == 0 {
		t.Fatalf("sample = %#v", sample)
	}
}

func TestMeasureCommandRecordsExitCodeAndSingleLineStderr(t *testing.T) {
	t.Parallel()
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: "/bin/sh", Args: []string{"-c", "printf 'first failure\\nsecond failure\\n' >&2; exit 7"},
		Directory: t.TempDir(), Timeout: 30 * time.Second, // hang guard, not a measurement
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
		Timeout:   30 * time.Second, // hang guard, not a measurement
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
	t.Parallel()
	sample := MeasureCommand(context.Background(), CommandSpec{
		Binary: "/bin/sh", Args: []string{"-c", "test \"$WORKBOOK_PERF_TEST_VALUE\" = present"},
		Directory: t.TempDir(), Environment: []string{"WORKBOOK_PERF_TEST_VALUE=present"}, Timeout: 30 * time.Second, // hang guard
	})
	if sample.ExitCode != 0 || sample.TimedOut || sample.Error != "" {
		t.Fatalf("sample = %#v", sample)
	}
}

// TestMeasureCommandTerminatesTimedOutDescendant proves that a measurement
// whose deadline passes kills the measured command's whole process group, not
// just its leader.
//
// The deadline expires when the fixture is ready, never on a clock. This test
// gave MeasureCommand a 100 ms Timeout, and inside it the leader shell had to
// fork a second shell that recorded its pid. Under a whole-tree run the fork
// alone could take longer than that, so the group died before the pid file
// existed and the test failed on its own fixture: measured on a loaded
// 18-core machine, 3 failures in 1,440 runs. The deadline now comes from the
// parent context and arrives only once the descendant has recorded its pid.
// MeasureCommand reports a parent deadline as a timeout exactly as it reports
// its own, and cancels the group the same way. Its own Timeout is only a hang
// guard here. TestMeasureCommandRecordsTimeout, which has no fixture to wait
// for, keeps covering a Timeout that expires on its own.
func TestMeasureCommandTerminatesTimedOutDescendant(t *testing.T) {
	t.Parallel()
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	proctest.ReapRecordedProcessGroup(t, childPIDPath)
	sample := MeasureCommand(deadlineOncePIDRecorded(t, childPIDPath), CommandSpec{
		Binary: proctest.Shell, Args: proctest.BusyLeaderArgs(childPIDPath),
		Directory: t.TempDir(), Timeout: 30 * time.Second, // hang guard, not a measurement
	})
	if !sample.TimedOut {
		t.Fatalf("sample = %#v", sample)
	}
	proctest.RequireDescendantTerminated(t, childPIDPath)
}

// fixtureDeadline is a context whose deadline passes when a test closes done
// rather than at a time. The standard library cannot build one: cancelling a
// context reports context.Canceled, and MeasureCommand counts only
// context.DeadlineExceeded as a timeout.
type fixtureDeadline struct {
	context.Context
	done chan struct{}
}

func (deadline *fixtureDeadline) Done() <-chan struct{} { return deadline.done }

func (deadline *fixtureDeadline) Err() error {
	select {
	case <-deadline.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// deadlineOncePIDRecorded returns a context whose deadline passes as soon as a
// helper has recorded a usable pid in pidPath. It stops watching when the test
// ends, whether or not the pid ever arrived.
func deadlineOncePIDRecorded(t *testing.T, pidPath string) context.Context {
	t.Helper()
	deadline := &fixtureDeadline{Context: context.Background(), done: make(chan struct{})}
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			if pidRecorded(pidPath) {
				close(deadline.done)
				return
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		<-stopped
	})
	return deadline
}

func pidRecorded(pidPath string) bool {
	pidText, err := os.ReadFile(pidPath)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidText)))
	return err == nil && pid > 0
}

// TestMeasureCommandReapsDescendantOfCommandThatExits covers the other way a
// measured command leaves a descendant behind: the command itself finishes, so
// the timeout cancellation that kills the process group never runs, and a
// background descendant it started keeps burning a core after the measurement
// reported a clean exit.
func TestMeasureCommandReapsDescendantOfCommandThatExits(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
