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
