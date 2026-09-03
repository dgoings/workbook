package syncloop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

	// bindSuffix names the socket while it is being bound, before it is renamed
	// onto the path clients dial. See listenPrivate for why the bind happens
	// somewhere else at all. The suffixed name is the longest path this package
	// ever hands to the kernel, so it is the one that has to fit sun_path.
	bindSuffix = ".bind"
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
	digest := hex.EncodeToString(sum[:8])
	name := "wb-" + digest + ".sock"

	// Each candidate resolves its directory lazily, so neither the second
	// private directory nor the repository fallback is created unless the one
	// before it has already been ruled out.
	//
	// The second private directory exists because the first one's name is
	// derived from nothing but the uid: it is a single fixed target, and a local
	// user who creates /tmp/workbook-<uid> as their own before this user's first
	// watcher runs disqualifies it forever, since a sticky /tmp also refuses
	// this process the rmdir. os.TempDir() is then /tmp itself on Linux, which
	// is refused too, and the repository fallback is len(commonGitDir)+22 bytes,
	// so a checkout deeper than 78 bytes leaves no path at all and the watcher
	// refuses to start. Naming the second one after the repository as well puts
	// the squatter back where they started: they have to guess the repository
	// path, which is the assumption the socket name already rests on.
	candidates := []struct {
		directory func() (string, error)
		name      string
	}{
		{directory: func() (string, error) { return userTempDir("") }, name: name},
		{directory: func() (string, error) { return userTempDir(digest) }, name: name},
		{directory: func() (string, error) { return os.TempDir(), nil }, name: name},
		{directory: func() (string, error) { return repositorySocketDir(commonGitDir) }, name: socketFilename},
	}

	for _, candidate := range candidates {
		directory, err := candidate.directory()
		if err != nil {
			continue
		}
		path := filepath.Join(directory, candidate.name)
		if len(path)+len(bindSuffix) > maxSocketPath {
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
		maxSocketPath-len(bindSuffix),
	)
}

// beforeRename runs in the one window listenPrivate has, between the bind and
// the rename. It is a variable only so a test can create a file inside that
// window and prove the bind did not change the mode it comes out with; nothing
// in production replaces it.
var beforeRename = func() {}

// listenPrivate binds the watcher socket so that no instant exists in which
// another user could connect to it. A socket anyone can connect to is a socket
// anyone can read the repository's status through, or silently drop a recorded
// conflict into.
//
// This used to set a umask of 0177 around the bind and restore it after. That
// closed the window, but syscall.Umask is process-wide and this process forks
// git: `workbook serve` binds this socket on its watcher goroutine while the
// board's own goroutine runs `git hash-object -w --stdin` for a mutation. A git
// run that inherited 0177 and had to create a loose-object fan-out directory
// created it with mode 0600, because mkdir asks for 0777 and the umask takes
// the owner's search bit with everyone else's. Git then cannot create a file in
// the directory it just made:
//
//	error: insufficient permission for adding an object to repository database .git/objects
//
// and every later object whose hash begins with those two hex digits fails the
// same way, in a repository that stays broken long after the watcher exits,
// because the directory outlives it. A serialized umask does not help: the
// mutex only holds off another bind, not the rest of the process.
//
// So the mode is applied to a name nobody dials. The socket is bound as
// path+bindSuffix, chmodded there, and renamed onto path. Renaming a bound
// socket keeps the listener serving — the listener holds the inode, not the
// name — the advertised path never exists in a mode another user could connect
// through, and no global process state is touched at all.
func listenPrivate(path string) (net.Listener, error) {
	temporary := path + bindSuffix
	// A watcher killed between the bind and the rename leaves this name behind,
	// and a leftover socket file fails the next bind with "address already in
	// use". Clearing it first is the same courtesy bind() already does for the
	// advertised path, and exclusivity is decided there rather than here.
	if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", temporary)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	beforeRename()
	if err := os.Rename(temporary, path); err != nil {
		_ = listener.Close()
		_ = os.Remove(temporary)
		return nil, err
	}
	return listener, nil
}

// userTempRoot is where the private per-user directory is created. It is a
// variable only so a test can point it at a root it controls.
var userTempRoot = "/tmp"

// currentUID reports the user a socket directory must belong to. It is a
// variable only so a test can exercise the foreign-owner rejection, which is
// the one check no fixture can otherwise reach: a test process cannot chown a
// directory to a second user, so the reported uid is what has to move.
var currentUID = os.Getuid

// userTempDir returns a private per-user directory under userTempRoot,
// refusing one that is not exactly what it should be. The root is
// world-writable, so a symlink, a foreign owner, or loose permissions means
// somebody else could place the socket a command then trusts.
//
// suffix distinguishes the second candidate from the first, so one squatted
// name is not the end of it. Empty names the shared per-user directory.
func userTempDir(suffix string) (string, error) {
	name := fmt.Sprintf("workbook-%d", currentUID())
	if suffix != "" {
		name += "-" + suffix
	}
	dir := filepath.Join(userTempRoot, name)
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
	if !ok || int(stat.Uid) != currentUID() {
		return nil, fmt.Errorf("%s belongs to another user", dir)
	}
	return info, nil
}
