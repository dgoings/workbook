//go:build windows

package syncloop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// defaultUserTempRoot is the per-user temporary directory. Unlike /tmp it is
// already inside the user's own profile, which is what makes the privacy check
// below a containment test rather than a permission test.
var defaultUserTempRoot = os.TempDir()

// defaultCurrentUID has no meaning on Windows, which identifies users by SID
// rather than by a small integer. It exists so that the per-user directory name
// is stable within a session and distinct between accounts; os.Getuid returns
// -1 here, which would give every account the same name.
var defaultCurrentUID = func() int {
	name := os.Getenv("USERNAME")
	if name == "" {
		return 0
	}
	// FNV-1a, kept positive: a short stable number, not a security boundary.
	hash := uint32(2166136261)
	for i := 0; i < len(name); i++ {
		hash ^= uint32(name[i])
		hash *= 16777619
	}
	return int(hash & 0x7fffffff)
}

// dirIsPrivate reports whether a directory is one only this user can reach.
//
// Windows has no POSIX mode bits — Go synthesises 0666/0777 for every file, so
// the Unix check would reject every directory — and no uid to compare. What it
// has instead is a per-user profile whose ACL already excludes other
// unprivileged accounts, and a TEMP that lives inside it. So privacy is
// established by containment: the directory must be under this user's own
// temporary directory.
//
// This is weaker than the Unix check in one way worth stating plainly: it
// trusts the profile's inherited ACL rather than verifying it. An administrator
// can read another account's profile, but an administrator can read the
// repository itself, so the socket is not the boundary that matters there.
func dirIsPrivate(dir string, _ os.FileInfo) error {
	root, err := filepath.Abs(userTempRoot)
	if err != nil {
		return fmt.Errorf("cannot resolve %s: %w", userTempRoot, err)
	}
	target, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("cannot resolve %s: %w", dir, err)
	}
	rooted := strings.EqualFold(target, root) ||
		strings.HasPrefix(strings.ToLower(target), strings.ToLower(root)+string(filepath.Separator))
	if !rooted {
		return fmt.Errorf("%s is outside this user's temporary directory", dir)
	}
	return nil
}
