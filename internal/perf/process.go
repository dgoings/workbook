package perf

import (
	"os"
	"syscall"
)

// ReapProcessGroup kills everything left in a finished command's process group.
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
//
// This is exported because every exec site that sets Setpgid owes its caller the
// same reap, and cmd/workbook-bench runs one of those sites outside this
// package. It applies only to a command that has already been waited on and is
// not expected to outlive the call: a site that starts a process deliberately -
// the warm HTTP server and the sync watchers - owns its group for as long as
// that process runs and terminates it on its own schedule instead.
func ReapProcessGroup(process *os.Process) {
	if process == nil || process.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
}
