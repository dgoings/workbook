package syncloop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
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

// socketPath returns the first candidate bind path that no other user can
// interfere with and that fits inside sun_path.
//
// The name is derived from the repository so a restarted watcher reuses it and
// can clear its own stale socket. Rendezvous does not depend on that
// derivation; the pointer file does that job.
//
// The per-user private directory is tried before os.TempDir(), and every
// candidate's directory is checked. os.TempDir() is the per-user $TMPDIR on
// darwin, but on Linux it is world-writable /tmp, and a derived name is a
// guessable name: anyone who can guess the repository path can bind
// /tmp/wb-<hash>.sock first. bind dials before binding and reads anything that
// answers as a live watcher, so a squatter would deny the entire optimization
// behind "a Workbook watcher already owns this repository" — permanently, since
// a sticky directory also refuses this process the unlink.
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

	// Each candidate resolves its directory lazily, so the repository fallback
	// is not created unless a temporary directory has already been ruled out.
	candidates := []struct {
		directory func() (string, error)
		name      string
	}{
		{directory: userTempDir, name: name},
		{directory: func() (string, error) { return os.TempDir(), nil }, name: name},
		{directory: func() (string, error) { return repositorySocketDir(commonGitDir) }, name: socketFilename},
	}

	for _, candidate := range candidates {
		directory, err := candidate.directory()
		if err != nil {
			continue
		}
		path := filepath.Join(directory, candidate.name)
		if len(path) > maxSocketPath {
			continue
		}
		if err := usableSocketDir(directory); err != nil {
			continue
		}
		return path, nil
	}
	return "", core.Errorf(
		core.CategoryOperational,
		"no socket path for this repository is both private to you and under %d bytes; run the watcher from a shorter path in a directory only you can write",
		maxSocketPath,
	)
}

// umaskMu serializes the umask window in listenPrivate. syscall.Umask is
// process-wide, so two binds must not overlap it.
var umaskMu sync.Mutex

// listenPrivate creates the socket with a umask that denies everyone but the
// owner, so there is no instant at which another user could connect. The chmod
// that follows still matters, because a platform may ignore umask for sockets;
// the umask is what closes the window before it. Under the usual umask 022 the
// interim mode denies connect anyway, but under umask 0 the socket was briefly
// world-connectable, and anything that connects can read the repository's
// status or silently drop a recorded conflict.
//
// The window is one Listen call, and a watcher binds once at startup, so no
// other file this process creates is realistically affected.
func listenPrivate(path string) (net.Listener, error) {
	umaskMu.Lock()
	previous := syscall.Umask(0o177)
	listener, err := net.Listen("unix", path)
	syscall.Umask(previous)
	umaskMu.Unlock()
	return listener, err
}

// userTempRoot is where the private per-user directory is created. It is a
// variable only so a test can point it at a root it controls.
var userTempRoot = "/tmp"

// userTempDir returns a private per-user directory under userTempRoot,
// refusing one that is not exactly what it should be. The root is
// world-writable, so a symlink, a foreign owner, or loose permissions means
// somebody else could place the socket a command then trusts.
func userTempDir() (string, error) {
	dir := filepath.Join(userTempRoot, fmt.Sprintf("workbook-%d", os.Getuid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	info, err := socketDirInfo(dir)
	if err != nil {
		return "", err
	}
	// Stricter than usableSocketDir, because this directory exists for exactly
	// one purpose and this process created it.
	if info.Mode().Perm() != 0o700 {
		return "", fmt.Errorf("%s is not private", dir)
	}
	return dir, nil
}

// repositorySocketDir is the last resort, for a repository whose temporary
// directories all produce oversized paths. It is created the way the pointer
// file's directory is, so the two never disagree about its mode.
func repositorySocketDir(commonGitDir string) (string, error) {
	dir := filepath.Join(commonGitDir, "workbook")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// usableSocketDir reports whether a socket in dir can be reached only by its
// owner. A directory another user can write to is unusable however carefully
// the socket itself is created: they can take the path first, and in a sticky
// directory this process cannot even unlink what they left behind.
func usableSocketDir(dir string) error {
	_, err := socketDirInfo(dir)
	return err
}

func socketDirInfo(dir string) (os.FileInfo, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s is writable by other users", dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return nil, fmt.Errorf("%s belongs to another user", dir)
	}
	return info, nil
}
