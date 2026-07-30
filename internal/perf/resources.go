package perf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// Peak resident set sizes are reported by wait4 in a platform-specific unit.
// Darwin reports ru_maxrss in bytes; Linux and the BSDs report kilobytes.
const (
	MaxResidentUnitBytes     = "bytes"
	MaxResidentUnitKilobytes = "kilobytes"
)

// ResourceMeasurement records one command's descriptive resource usage. It
// carries no target and is never compared against a threshold.
type ResourceMeasurement struct {
	Command      string   `json:"command"`
	Argv         []string `json:"argv"`
	Milliseconds float64  `json:"milliseconds"`
	ExitCode     int      `json:"exitCode"`
	TimedOut     bool     `json:"timedOut"`
	Error        string   `json:"error,omitempty"`

	// MaxResidentBytes normalizes MaxResidentRaw into bytes using
	// MaxResidentRawUnit. It is the largest resident set observed for the
	// measured process or any descendant it reaped, not the sum of the
	// concurrent resident sets of a process tree.
	MaxResidentBytes   int64  `json:"maxResidentBytes"`
	MaxResidentRaw     int64  `json:"maxResidentRaw"`
	MaxResidentRawUnit string `json:"maxResidentRawUnit"`

	UserMilliseconds   float64 `json:"userMilliseconds"`
	SystemMilliseconds float64 `json:"systemMilliseconds"`

	// BlockInputOperations and BlockOutputOperations are ru_inblock and
	// ru_oublock. Darwin never populates them, so they read zero there and
	// BlockIOCountersSupported is false.
	BlockInputOperations     int64 `json:"blockInputOperations"`
	BlockOutputOperations    int64 `json:"blockOutputOperations"`
	BlockIOCountersSupported bool  `json:"blockIoCountersSupported"`

	MinorPageFaults            int64 `json:"minorPageFaults"`
	MajorPageFaults            int64 `json:"majorPageFaults"`
	VoluntaryContextSwitches   int64 `json:"voluntaryContextSwitches"`
	InvoluntaryContextSwitches int64 `json:"involuntaryContextSwitches"`

	// RepositoryBytesDelta is the change in total on-disk bytes under the
	// repository root across the command, sampled outside the timing window.
	// It is a durable-write lower bound, not a syscall-level I/O counter.
	RepositoryBytesDelta int64 `json:"repositoryBytesDelta"`

	Stdout []byte `json:"-"`
	Stderr []byte `json:"-"`
}

// MaxResidentUnitForOS returns the unit wait4 uses for ru_maxrss on the named
// GOOS.
func MaxResidentUnitForOS(goos string) string {
	switch goos {
	case "darwin", "ios":
		return MaxResidentUnitBytes
	default:
		return MaxResidentUnitKilobytes
	}
}

// BlockIOCountersSupportedForOS reports whether the named GOOS maintains the
// ru_inblock and ru_oublock rusage counters.
func BlockIOCountersSupportedForOS(goos string) bool {
	switch goos {
	case "darwin", "ios":
		return false
	default:
		return true
	}
}

func maxResidentBytes(raw int64, unit string) int64 {
	if unit == MaxResidentUnitKilobytes {
		return raw * 1024
	}
	return raw
}

// MeasureCommandResources runs a command to completion and records its peak
// resident set, rusage I/O and fault counters, and elapsed runtime from the
// wait4 resource usage Go collects when reaping the child.
func MeasureCommandResources(ctx context.Context, spec CommandSpec) ResourceMeasurement {
	measurement := ResourceMeasurement{
		Argv:                     append([]string(nil), spec.Args...),
		ExitCode:                 -1,
		MaxResidentRawUnit:       MaxResidentUnitForOS(runtime.GOOS),
		BlockIOCountersSupported: BlockIOCountersSupportedForOS(runtime.GOOS),
	}

	commandContext, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, spec.Binary, spec.Args...)
	command.Dir = spec.Directory
	command.Env = append(os.Environ(), spec.Environment...)
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
	err := command.Run()
	elapsed := time.Since(startedAt)
	measurement.Milliseconds = durationAsMilliseconds(elapsed)
	measurement.Stdout = append([]byte(nil), stdout.Bytes()...)
	measurement.Stderr = append([]byte(nil), stderr.Bytes()...)

	if err == nil {
		measurement.ExitCode = 0
	} else {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			measurement.ExitCode = exitError.ExitCode()
		}
		measurement.TimedOut = commandContext.Err() == context.DeadlineExceeded
		measurement.Error = stderrSummary(string(measurement.Stderr), err)
	}

	state := command.ProcessState
	if state == nil {
		return measurement
	}
	measurement.UserMilliseconds = durationAsMilliseconds(state.UserTime())
	measurement.SystemMilliseconds = durationAsMilliseconds(state.SystemTime())
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || usage == nil {
		return measurement
	}
	measurement.MaxResidentRaw = int64(usage.Maxrss)
	measurement.MaxResidentBytes = maxResidentBytes(measurement.MaxResidentRaw, measurement.MaxResidentRawUnit)
	measurement.MinorPageFaults = int64(usage.Minflt)
	measurement.MajorPageFaults = int64(usage.Majflt)
	measurement.VoluntaryContextSwitches = int64(usage.Nvcsw)
	measurement.InvoluntaryContextSwitches = int64(usage.Nivcsw)
	if measurement.BlockIOCountersSupported {
		measurement.BlockInputOperations = int64(usage.Inblock)
		measurement.BlockOutputOperations = int64(usage.Oublock)
	}
	return measurement
}

// directoryBytes sums the apparent size of every regular file under root. An
// absent root contributes zero bytes.
func directoryBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("measure directory %q: %w", root, err)
	}
	return total, nil
}
