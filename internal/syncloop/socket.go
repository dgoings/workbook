package syncloop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/dgoings/workbook/internal/core"
)

const (
	// PointerFormat and PointerVersion name the file that tells a command where
	// this repository's watcher is listening.
	PointerFormat  = "workbook.watcher"
	PointerVersion = 1

	pointerFilename = "watcher.json"
	socketFilename  = "watcher.sock"

	// maxSocketPath keeps a bind path inside the platform's sun_path field,
	// which is 104 bytes on darwin and 108 on Linux. The margin is deliberate:
	// exceeding it fails the bind rather than truncating.
	maxSocketPath = 100
)

// pointer publishes where a watcher listens. It lives at a canonical path
// derived from the repository rather than encoding the socket location in a
// name both sides must compute, because the two sides cannot be relied on to
// compute the same one: CommonGitDir is cleaned but not necessarily absolute,
// and $TMPDIR is per-user and per-bootstrap-namespace on darwin. A rendezvous
// that silently misses would cost the entire optimization with no diagnostic.
type pointer struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Socket  string `json:"socket"`
	PID     int    `json:"pid"`
}

// PointerPath is where a watcher publishes its socket for this repository.
// Exported so a test double can stand in for a watcher without reimplementing
// the rendezvous.
func PointerPath(commonGitDir string) string {
	return filepath.Join(commonGitDir, "workbook", pointerFilename)
}

func pointerPath(commonGitDir string) string {
	return PointerPath(commonGitDir)
}

func readPointer(commonGitDir string) (pointer, error) {
	contents, err := os.ReadFile(pointerPath(commonGitDir))
	if err != nil {
		return pointer{}, err
	}
	var published pointer
	if err := json.Unmarshal(contents, &published); err != nil {
		return pointer{}, core.Wrap(core.CategoryCorruptData, "watcher pointer file is not valid JSON", err)
	}
	if published.Format != PointerFormat || published.Version != PointerVersion {
		return pointer{}, core.Errorf(core.CategoryCorruptData, "watcher pointer file is not a %s version %d document", PointerFormat, PointerVersion)
	}
	if published.Socket == "" {
		return pointer{}, core.Errorf(core.CategoryCorruptData, "watcher pointer file names no socket")
	}
	return published, nil
}

func writePointer(commonGitDir string, published pointer) error {
	path := pointerPath(commonGitDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return core.Wrap(core.CategoryOperational, "create the watcher directory", err)
	}
	contents, err := json.Marshal(published)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "encode the watcher pointer", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(contents, '\n'), 0o600); err != nil {
		return core.Wrap(core.CategoryOperational, "write the watcher pointer", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return core.Wrap(core.CategoryOperational, "publish the watcher pointer", err)
	}
	return nil
}

// removePointer retracts the pointer only when it still names this watcher, so
// a shutting-down watcher never deletes a successor's rendezvous.
func removePointer(commonGitDir string, socket string) {
	published, err := readPointer(commonGitDir)
	if err != nil || published.Socket != socket {
		return
	}
	_ = os.Remove(pointerPath(commonGitDir))
}

// socketPath returns the first candidate bind path that fits inside sun_path.
//
// The name is derived from the repository so a restarted watcher reuses it and
// can clear its own stale socket. Rendezvous does not depend on that
// derivation; the pointer file does that job.
func socketPath(commonGitDir string) (string, error) {
	absolute, err := filepath.Abs(commonGitDir)
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "resolve the repository directory", err)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	sum := sha256.Sum256([]byte(absolute))
	name := "wb-" + hex.EncodeToString(sum[:8]) + ".sock"

	candidates := []string{filepath.Join(os.TempDir(), name)}
	if shared, err := userTempDir(); err == nil {
		candidates = append(candidates, filepath.Join(shared, name))
	}
	candidates = append(candidates, filepath.Join(commonGitDir, "workbook", socketFilename))

	for _, candidate := range candidates {
		if len(candidate) <= maxSocketPath {
			return candidate, nil
		}
	}
	return "", core.Errorf(
		core.CategoryOperational,
		"no socket path for this repository fits in %d bytes; run the watcher from a shorter path",
		maxSocketPath,
	)
}

// userTempDir returns a private per-user directory under /tmp, refusing one
// that is not exactly what it should be. /tmp is world-writable, so a symlink,
// a foreign owner, or loose permissions means somebody else could place the
// socket a command then trusts.
func userTempDir() (string, error) {
	uid := os.Getuid()
	dir := filepath.Join("/tmp", fmt.Sprintf("workbook-%d", uid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("shared watcher directory is not a directory")
	}
	if info.Mode().Perm() != 0o700 {
		return "", errors.New("shared watcher directory is not private")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid {
		return "", errors.New("shared watcher directory belongs to another user")
	}
	return dir, nil
}
