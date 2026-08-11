// Package proctest builds the shell helpers and assertions a descendant-reaping
// test needs. The measurement harness gives every command it runs a process
// group of its own, so proving that a finished command leaves nothing behind
// takes a command that deliberately leaks a background descendant. That shape
// is identical wherever the harness execs, and cmd/workbook-bench runs its own
// exec site outside package perf, so it lives here rather than in one package's
// test files.
package proctest

import (
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

// Shell names the interpreter these helpers' scripts are written for.
const Shell = "/bin/sh"

// TestBinaryAlive returns a shell condition, built only from shell builtins so
// it adds no process churn to a loop, that succeeds while the process running
// these tests is still alive.
func TestBinaryAlive() string {
	return "kill -0 " + strconv.Itoa(os.Getpid()) + " 2>/dev/null"
}

// BusyLoopWhileTestBinaryLives returns a shell loop that spins a core until the
// process running these tests exits.
//
// These helpers have to burn CPU until the code under test terminates them, but
// a plain `while :; do :; done` outlives an interrupted run. The measurement
// harness deliberately gives the measured command its own process group, so a
// signal aimed at the test runner's process group - a terminal interrupt, a CI
// job wrapper signalling the group it started, a supervisor killing the run -
// never reaches the helper. The test binary dies before it can cancel the
// measurement, the helper is reparented to init, and it spins a core until
// somebody notices. Ending the loop when the process that started it is gone
// bounds the damage to one loop iteration no matter how the run dies.
func BusyLoopWhileTestBinaryLives() string {
	return "while " + TestBinaryAlive() + "; do :; done"
}

// startDescendant returns the shell fragment that launches the background
// descendant and waits for it to record its pid in $1.
func startDescendant(redirect string) string {
	return "sh -c 'echo $$ > \"$1\"; " + BusyLoopWhileTestBinaryLives() + "' sh \"$1\"" + redirect + " & " +
		"while [ ! -s \"$1\" ] && " + TestBinaryAlive() + "; do :; done"
}

// ExitingLeaderArgs returns the Shell arguments for a command that starts a
// background descendant, waits for the descendant to record its pid in pidPath,
// and then exits normally. Nothing cancels such a command, so only an
// unconditional reap after it finishes can stop the descendant.
func ExitingLeaderArgs(pidPath string) []string {
	// The descendant drops the inherited output pipes so the leader's exit is
	// the only thing this measures.
	return []string{"-c", startDescendant(" >/dev/null 2>&1"), "sh", pidPath}
}

// BusyLeaderArgs returns the Shell arguments for a command that starts the same
// background descendant and then spins itself, so only a timeout ends it.
func BusyLeaderArgs(pidPath string) []string {
	return []string{"-c", startDescendant("") + "; " + BusyLoopWhileTestBinaryLives(), "sh", pidPath}
}

// ExitingLeaderShimPATH writes an executable named program that leaks a
// background descendant exactly as ExitingLeaderArgs does, and returns a PATH
// value that finds it ahead of any real installation.
//
// An exec site that runs a fixed program cannot be handed a leaky command: the
// perf harness's repository and storage helpers run "git" and nothing else, so
// replacing the program they look up is the only way to make one of their runs
// end with a descendant still alive.
func ExitingLeaderShimPATH(t *testing.T, program, pidPath string) string {
	t.Helper()
	directory := t.TempDir()
	shim := "#!/bin/sh\nset -- \"" + pidPath + "\"\n" + startDescendant(" >/dev/null 2>&1") + "\n"
	if err := os.WriteFile(filepath.Join(directory, program), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	return directory + string(os.PathListSeparator) + os.Getenv("PATH")
}

// ReapRecordedProcessGroup kills whatever a helper recorded in pidPath once the
// test ends, however it ends. Registering it before the measurement runs keeps
// the reap independent of the test body, so a failed assertion or a panic still
// leaves a dead helper behind rather than a spinning one, and it never trusts
// the code under test to have done the killing.
func ReapRecordedProcessGroup(t *testing.T, pidPath string) {
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

// RequireDescendantTerminated fails unless the descendant a helper recorded has
// stopped running. Reaping kills the whole process group, but a terminated
// descendant stays visible to kill(2) until the init process it was reparented
// to reaps it, so poll instead of sampling once. Polling cannot mask a
// descendant that genuinely survived, because that descendant busy-loops for as
// long as this process lives and never reaches a terminated state.
func RequireDescendantTerminated(t *testing.T, pidPath string) {
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
