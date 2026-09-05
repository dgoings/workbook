//go:build !windows

package syncloop

import (
	"fmt"
	"os"
	"syscall"
)

// defaultUserTempRoot is the shared temporary directory the private per-user
// socket directory is created inside.
const defaultUserTempRoot = "/tmp"

// defaultCurrentUID reports the user a socket directory must belong to.
var defaultCurrentUID = os.Getuid

// dirIsPrivate reports whether a directory is one only this user can write to.
//
// /tmp is world-writable, so a socket placed in a directory somebody else owns
// or can write to is a socket somebody else can read the repository's status
// through, or silently drop a recorded conflict into. Both facts come from the
// kernel: the mode bits, and the owning uid.
func dirIsPrivate(dir string, info os.FileInfo) error {
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by other users", dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != currentUID() {
		return fmt.Errorf("%s belongs to another user", dir)
	}
	return nil
}
