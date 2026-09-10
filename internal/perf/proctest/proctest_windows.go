//go:build windows

// Stubs so the package compiles on Windows.
//
// Everything the real implementation does — putting a command in its own
// process group, signalling that group, reading /proc to tell a zombie from a
// live process — describes POSIX process semantics. Windows has job objects
// instead, which is a different model, not a translation of this one. The perf
// suite that uses these helpers does not run here, so the honest stub is one
// that skips rather than one that pretends.

package proctest

import "testing"

func TestBinaryAlive() string { return "" }

func BusyLoopWhileTestBinaryLives() string { return "" }

func ExitingLeaderArgs(string) []string { return nil }

func BusyLeaderArgs(string) []string { return nil }

func ExitingLeaderShimPATH(t *testing.T, _, _ string) string {
	t.Skip("descendant reaping is a POSIX process-group property")
	return ""
}

func ReapRecordedProcessGroup(t *testing.T, _ string) {
	t.Skip("descendant reaping is a POSIX process-group property")
}

func RequireDescendantTerminated(t *testing.T, _ string) {
	t.Skip("descendant reaping is a POSIX process-group property")
}
