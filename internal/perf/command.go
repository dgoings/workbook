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
	file, err := os.Open(cursor.path)
	if err != nil {
		return 0, fmt.Errorf("open Trace2 event file: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat Trace2 event file: %w", err)
	}
	if info.Size() < cursor.offset {
		cursor.offset = 0
	}
	if _, err := file.Seek(cursor.offset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek Trace2 event file: %w", err)
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return 0, fmt.Errorf("read Trace2 event file: %w", err)
	}
	lastNewline := bytes.LastIndexByte(data, '\n')
	if lastNewline < 0 {
		return 0, nil
	}
	data = data[:lastNewline+1]
	count := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var event traceEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return 0, fmt.Errorf("parse Trace2 event: %w", err)
		}
		if event.Event == "start" && len(event.Argv) > 0 {
			count++
		}
	}
	cursor.offset += int64(len(data))
	return count, nil
}

// MeasureCommand runs a command under a fresh Trace2 event file and returns
// its elapsed runtime, exit outcome, and Git process count.
func MeasureCommand(ctx context.Context, spec CommandSpec) Sample {
	sample := Sample{ExitCode: -1}
	traceFile, err := os.CreateTemp("", "workbook-git-trace-*.json")
	if err != nil {
		sample.Error = fmt.Sprintf("create Trace2 event file: %v", err)
		return sample
	}
	tracePath := traceFile.Name()
	traceFile.Close()
	defer os.Remove(tracePath)

	absTracePath, err := filepath.Abs(tracePath)
	if err != nil {
		sample.Error = fmt.Sprintf("resolve Trace2 event file: %v", err)
		return sample
	}
	cursor, err := OpenTraceCursor(absTracePath)
	if err != nil {
		sample.Error = err.Error()
		return sample
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
	var stderr bytes.Buffer
	command.Stderr = &stderr

	startedAt := time.Now()
	err = command.Run()
	sample.Duration = time.Since(startedAt)
	if err == nil {
		sample.ExitCode = 0
	} else {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			sample.ExitCode = exitError.ExitCode()
		}
		sample.TimedOut = commandContext.Err() == context.DeadlineExceeded
		sample.Error = stderrSummary(stderr.String(), err)
	}

	gitProcesses, traceErr := cursor.CountNewGitProcesses()
	if traceErr != nil {
		if sample.Error == "" {
			sample.Error = traceErr.Error()
		}
		return sample
	}
	sample.GitProcesses = gitProcesses
	return sample
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
