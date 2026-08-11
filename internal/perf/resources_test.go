package perf

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/perf/proctest"
)

const (
	resourceHelperEnv      = "WORKBOOK_PERF_RESOURCE_HELPER"
	resourceHelperWriteEnv = "WORKBOOK_PERF_RESOURCE_HELPER_WRITE"
	resourceHelperFailEnv  = "WORKBOOK_PERF_RESOURCE_HELPER_FAIL"
	resourceHelperBytes    = 96 << 20
	resourceHelperWritten  = 64 << 20
)

var resourceHelperSink []byte

// TestResourceHelperProcess is re-executed as a child process by the resource
// measurement tests. It touches a known amount of memory and optionally writes
// a known number of bytes so a measurement can be checked against real work.
func TestResourceHelperProcess(t *testing.T) {
	if os.Getenv(resourceHelperEnv) != "1" {
		t.Skip("helper process; runs only when re-executed by a resource measurement test")
	}
	block := make([]byte, resourceHelperBytes)
	for index := 0; index < len(block); index += 4096 {
		block[index] = byte(index)
	}
	resourceHelperSink = block

	if path := os.Getenv(resourceHelperWriteEnv); path != "" {
		file, err := os.Create(path)
		if err != nil {
			os.Exit(3)
		}
		payload := make([]byte, 1<<20)
		for written := 0; written < resourceHelperWritten; written += len(payload) {
			if _, err := file.Write(payload); err != nil {
				os.Exit(4)
			}
		}
		if err := file.Sync(); err != nil {
			os.Exit(5)
		}
		if err := file.Close(); err != nil {
			os.Exit(6)
		}
	}
	runtime.KeepAlive(resourceHelperSink)
	if os.Getenv(resourceHelperFailEnv) == "1" {
		os.Exit(7)
	}
	os.Exit(0)
}

func resourceHelperSpec(t *testing.T, environment []string) CommandSpec {
	t.Helper()
	return CommandSpec{
		Binary:      os.Args[0],
		Args:        []string{"-test.run=^TestResourceHelperProcess$"},
		Environment: append([]string{resourceHelperEnv + "=1"}, environment...),
		Timeout:     120 * time.Second,
	}
}

// Mutation witness: reporting the raw Maxrss value without applying the
// platform's unit, or applying the wrong platform's unit, moves the reported
// peak by a factor of 1024 and fails this range assertion.
func TestMeasureCommandResourcesReportsPeakResidentMemoryInDocumentedUnits(t *testing.T) {
	measurement := MeasureCommandResources(context.Background(), resourceHelperSpec(t, nil))

	if measurement.ExitCode != 0 || measurement.TimedOut || measurement.Error != "" {
		t.Fatalf("helper measurement = exit %d timedOut %t error %q", measurement.ExitCode, measurement.TimedOut, measurement.Error)
	}
	if measurement.MaxResidentRawUnit != MaxResidentUnitForOS(runtime.GOOS) {
		t.Fatalf("max resident unit = %q, want %q", measurement.MaxResidentRawUnit, MaxResidentUnitForOS(runtime.GOOS))
	}
	if measurement.MaxResidentRaw <= 0 {
		t.Fatalf("raw max resident = %d, want a positive value", measurement.MaxResidentRaw)
	}
	if measurement.MaxResidentBytes < resourceHelperBytes/2 || measurement.MaxResidentBytes > 8<<30 {
		t.Fatalf("peak resident bytes = %d, want between %d and %d after touching %d bytes",
			measurement.MaxResidentBytes, resourceHelperBytes/2, int64(8)<<30, resourceHelperBytes)
	}
	if measurement.MinorPageFaults <= 0 {
		t.Fatalf("minor page faults = %d, want a positive count", measurement.MinorPageFaults)
	}
	if measurement.Milliseconds <= 0 {
		t.Fatalf("elapsed milliseconds = %f, want a positive duration", measurement.Milliseconds)
	}
	if measurement.UserMilliseconds <= 0 && measurement.SystemMilliseconds <= 0 {
		t.Fatalf("user %f ms and system %f ms are both non-positive", measurement.UserMilliseconds, measurement.SystemMilliseconds)
	}
}

// Mutation witness: claiming darwin populates block I/O counters, or silently
// reporting them as if they were meaningful there, fails this assertion after
// the helper writes 64 MiB.
func TestMeasureCommandResourcesDocumentsBlockIOCounterAvailability(t *testing.T) {
	target := filepath.Join(t.TempDir(), "written.bin")
	measurement := MeasureCommandResources(context.Background(), resourceHelperSpec(t, []string{
		resourceHelperWriteEnv + "=" + target,
	}))

	if measurement.ExitCode != 0 || measurement.Error != "" {
		t.Fatalf("helper measurement = exit %d error %q", measurement.ExitCode, measurement.Error)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != resourceHelperWritten {
		t.Fatalf("helper wrote %d bytes, want %d", info.Size(), resourceHelperWritten)
	}
	if measurement.BlockIOCountersSupported != BlockIOCountersSupportedForOS(runtime.GOOS) {
		t.Fatalf("block I/O support = %t, want %t on %s",
			measurement.BlockIOCountersSupported, BlockIOCountersSupportedForOS(runtime.GOOS), runtime.GOOS)
	}
	if !measurement.BlockIOCountersSupported &&
		(measurement.BlockInputOperations != 0 || measurement.BlockOutputOperations != 0) {
		t.Fatalf("unsupported block I/O counters reported %d in and %d out; the documented caveat says they stay zero",
			measurement.BlockInputOperations, measurement.BlockOutputOperations)
	}
}

func TestMeasureCommandResourcesRetainsFailingExitCode(t *testing.T) {
	measurement := MeasureCommandResources(context.Background(), resourceHelperSpec(t, []string{
		resourceHelperFailEnv + "=1",
	}))

	if measurement.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", measurement.ExitCode)
	}
	if measurement.TimedOut {
		t.Fatalf("failing helper reported a timeout")
	}
	if measurement.MaxResidentBytes <= 0 {
		t.Fatalf("peak resident bytes = %d, want resource usage even for a failing command", measurement.MaxResidentBytes)
	}
}

func TestMaxResidentUnitFollowsPlatformConvention(t *testing.T) {
	for _, testCase := range []struct {
		goos string
		want string
	}{
		{goos: "darwin", want: MaxResidentUnitBytes},
		{goos: "ios", want: MaxResidentUnitBytes},
		{goos: "linux", want: MaxResidentUnitKilobytes},
		{goos: "freebsd", want: MaxResidentUnitKilobytes},
	} {
		if got := MaxResidentUnitForOS(testCase.goos); got != testCase.want {
			t.Fatalf("max resident unit for %s = %q, want %q", testCase.goos, got, testCase.want)
		}
	}
	if got := maxResidentBytes(2048, MaxResidentUnitKilobytes); got != 2048*1024 {
		t.Fatalf("2048 kilobytes = %d bytes, want %d", got, 2048*1024)
	}
	if got := maxResidentBytes(2048, MaxResidentUnitBytes); got != 2048 {
		t.Fatalf("2048 bytes = %d bytes, want 2048", got)
	}
}

func TestBlockIOCounterSupportFollowsPlatformConvention(t *testing.T) {
	if BlockIOCountersSupportedForOS("darwin") {
		t.Fatal("darwin does not maintain ru_inblock and ru_oublock")
	}
	if !BlockIOCountersSupportedForOS("linux") {
		t.Fatal("linux maintains ru_inblock and ru_oublock")
	}
}

func TestDirectoryBytesSumsEveryRegularFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.bin"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "deeper", "b.bin"), make([]byte, 2345), 0o644); err != nil {
		t.Fatal(err)
	}

	total, err := directoryBytes(root)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3345 {
		t.Fatalf("directory bytes = %d, want 3345", total)
	}

	missing, err := directoryBytes(filepath.Join(root, "absent"))
	if err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatalf("absent directory bytes = %d, want 0", missing)
	}
}

// TestMeasureCommandResourcesReapsDescendantOfCommandThatExits covers the way a
// resource measurement leaves a descendant behind without ever timing out: the
// measured command finishes, so the cancellation that kills the process group
// never runs, and a background descendant it started keeps burning a core after
// the measurement reported a clean exit.
func TestMeasureCommandResourcesReapsDescendantOfCommandThatExits(t *testing.T) {
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	proctest.ReapRecordedProcessGroup(t, childPIDPath)
	measurement := MeasureCommandResources(context.Background(), CommandSpec{
		Binary: proctest.Shell, Args: proctest.ExitingLeaderArgs(childPIDPath),
		Directory: t.TempDir(), Timeout: 30 * time.Second,
	})
	if measurement.ExitCode != 0 || measurement.TimedOut || measurement.Error != "" {
		t.Fatalf("measurement = %#v", measurement)
	}
	proctest.RequireDescendantTerminated(t, childPIDPath)
}
