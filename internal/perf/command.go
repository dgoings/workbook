package perf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// CommandSpec describes one bounded command measurement.
type CommandSpec struct {
	Binary      string
	Args        []string
	Directory   string
	Environment []string
	Timeout     time.Duration
}

// TraceCursor counts Git process starts written after it was opened or last
// read, so a server's shared Trace2 file can be sampled per request.
type TraceCursor struct {
	path   string
	offset int64
}

type traceEvent struct {
	Event string   `json:"event"`
	Argv  []string `json:"argv"`
	Name  string   `json:"name"`
}

// TraceCounts summarizes the Git work a Trace2 event file recorded since a
// cursor last read it. Commands tallies Trace2's own `cmd_name` events, which
// name the Git subcommand each process ran, so a caller can price a unit of work
// by the command that defines it rather than by a raw process total.
type TraceCounts struct {
	GitProcesses int
	Commands     map[string]int
}

const commandWaitDelay = 100 * time.Millisecond

// OpenTraceCursor opens a Trace2 event file and starts counting after its
// existing contents.
func OpenTraceCursor(path string) (*TraceCursor, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve Trace2 path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("stat Trace2 path: %w", err)
	}
	return &TraceCursor{path: absPath, offset: info.Size()}, nil
}

// CountNewGitProcesses returns the number of Trace2 start events written
// since the cursor's last count.
func (cursor *TraceCursor) CountNewGitProcesses() (int, error) {
	counts, err := cursor.CountNew()
	return counts.GitProcesses, err
}

// CountNew returns the Git process starts and per-subcommand tallies written
// since the cursor's last count, and advances the cursor past them.
func (cursor *TraceCursor) CountNew() (TraceCounts, error) {
	counts := TraceCounts{Commands: map[string]int{}}
	file, err := os.Open(cursor.path)
	if err != nil {
		return TraceCounts{}, fmt.Errorf("open Trace2 event file: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return TraceCounts{}, fmt.Errorf("stat Trace2 event file: %w", err)
	}
	if info.Size() < cursor.offset {
		cursor.offset = 0
	}
	if _, err := file.Seek(cursor.offset, io.SeekStart); err != nil {
		return TraceCounts{}, fmt.Errorf("seek Trace2 event file: %w", err)
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return TraceCounts{}, fmt.Errorf("read Trace2 event file: %w", err)
	}
	lastNewline := bytes.LastIndexByte(data, '\n')
	if lastNewline < 0 {
		return counts, nil
	}
	data = data[:lastNewline+1]
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var event traceEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return TraceCounts{}, fmt.Errorf("parse Trace2 event: %w", err)
		}
		switch {
		case event.Event == "start" && len(event.Argv) > 0:
			counts.GitProcesses++
		case event.Event == "cmd_name" && event.Name != "":
			counts.Commands[event.Name]++
		}
	}
	cursor.offset += int64(len(data))
	return counts, nil
}

// CommandMeasurement preserves a command's measurement and its output streams.
type CommandMeasurement struct {
	Sample Sample
	Stdout []byte
	Stderr []byte
}

// MeasureCommandOutput runs a command under a fresh Trace2 event file and
// returns its elapsed runtime, exit outcome, Git process count, and output
// streams.
func MeasureCommandOutput(ctx context.Context, spec CommandSpec) CommandMeasurement {
	measurement := CommandMeasurement{Sample: Sample{ExitCode: -1}}
	traceFile, err := os.CreateTemp("", "workbook-git-trace-*.json")
	if err != nil {
		measurement.Sample.Error = fmt.Sprintf("create Trace2 event file: %v", err)
		return measurement
	}
	tracePath := traceFile.Name()
	traceFile.Close()
	defer os.Remove(tracePath)

	absTracePath, err := filepath.Abs(tracePath)
	if err != nil {
		measurement.Sample.Error = fmt.Sprintf("resolve Trace2 event file: %v", err)
		return measurement
	}
	cursor, err := OpenTraceCursor(absTracePath)
	if err != nil {
		measurement.Sample.Error = err.Error()
		return measurement
	}

	commandContext, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, spec.Binary, spec.Args...)
	command.Dir = spec.Directory
	command.Env = append(append(os.Environ(), spec.Environment...), "GIT_TRACE2_EVENT="+absTracePath)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	command.WaitDelay = commandWaitDelay
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	startedAt := time.Now()
	err = command.Run()
	measurement.Stdout = append([]byte(nil), stdout.Bytes()...)
	measurement.Stderr = append([]byte(nil), stderr.Bytes()...)
	measurement.Sample.Duration = time.Since(startedAt)
	// Reap only once the duration is stamped. This is a measurement harness, so
	// the kill(2) belongs to the harness rather than to the command it prices.
	reapProcessGroup(command.Process)
	if err == nil {
		measurement.Sample.ExitCode = 0
	} else {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			measurement.Sample.ExitCode = exitError.ExitCode()
		}
		measurement.Sample.TimedOut = commandContext.Err() == context.DeadlineExceeded
		measurement.Sample.Error = stderrSummary(string(measurement.Stderr), err)
	}

	gitProcesses, traceErr := cursor.CountNewGitProcesses()
	if traceErr != nil {
		if measurement.Sample.Error == "" {
			measurement.Sample.Error = traceErr.Error()
		}
		return measurement
	}
	measurement.Sample.GitProcesses = gitProcesses
	return measurement
}

// reapProcessGroup kills everything left in a finished command's process group.
//
// SysProcAttr.Setpgid puts a measured command in a process group of its own, and
// cancelling the measurement signals that whole group, but cancellation is not
// the only way a measurement ends with descendants still running. A command that
// exits on its own leaves a background descendant behind, os/exec's WaitDelay
// kill reaches only the leader and not the group, and a descendant forked while
// the cancellation signal was already in flight can miss it. Every one of those
// escapes a bounded measurement as a process that outlives the run, and a stray
// busy process silently skews every later measurement on the same host.
//
// Waiting on the command already reaped the leader, so its pid could in
// principle name a different process by now. Three cases follow. While any
// descendant survives, the pid cannot name a different process group at all:
// neither darwin nor Linux allocates a pid that is still in use as a process
// group id, and the group stays in use for as long as it has a member. When the
// group is empty and the pid is still unused, the signal fails with ESRCH and
// there was nothing to kill. The remaining case is the only hazard: the group is
// empty, the pid has been reused, and its new owner made itself a group leader
// through setsid or setpgid, so this would signal a stranger. Reaching it takes
// the allocator wrapping the whole pid space and the winner becoming a group
// leader in the microseconds between Wait reaping the leader and the next
// statement issuing the signal. Closing it would mean signalling the group
// before the leader is reaped, which Cmd.Run offers no hook for, so the case is
// named and accepted rather than guarded.
func reapProcessGroup(process *os.Process) {
	if process == nil || process.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
}

// MeasureCommand preserves the original sample-only measurement API.
func MeasureCommand(ctx context.Context, spec CommandSpec) Sample {
	return MeasureCommandOutput(ctx, spec).Sample
}

func stderrSummary(stderr string, commandErr error) string {
	for _, line := range strings.FieldsFunc(stderr, func(r rune) bool {
		return r == '\n' || r == '\r'
	}) {
		if summary := strings.TrimSpace(line); summary != "" {
			return summary
		}
	}
	return commandErr.Error()
}
