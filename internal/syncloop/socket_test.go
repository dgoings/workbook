package syncloop

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The socket name is derived from the repository path, so it is guessable. The
// only thing standing between that and another local user pre-binding it is the
// directory, which is why the chosen one must be writable by nobody else. On
// Linux os.TempDir() is world-writable /tmp, so this is the whole fix.
func TestSocketPathChoosesADirectoryOnlyThisUserCanWrite(t *testing.T) {
	path, err := socketPath(t.TempDir())
	if err != nil {
		t.Fatalf("socketPath() error = %v", err)
	}
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatalf("Lstat(%q) error = %v", directory, err)
	}
	if info.Mode().Perm()&0o002 != 0 {
		t.Fatalf("socketPath() = %q, whose directory is world-writable (%v)", path, info.Mode().Perm())
	}
	if info.Mode().Perm()&0o020 != 0 {
		t.Fatalf("socketPath() = %q, whose directory is group-writable (%v)", path, info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("Lstat(%q) returned no unix metadata", directory)
	}
	if int(stat.Uid) != os.Getuid() {
		t.Fatalf("socketPath() = %q, whose directory belongs to uid %d, not %d", path, stat.Uid, os.Getuid())
	}
}

// A world-writable temporary directory is skipped rather than trusted, even
// when the path fits comfortably.
func TestSocketPathSkipsAWorldWritableTemporaryDirectory(t *testing.T) {
	open := filepath.Join(shortTempDir(t), "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Setenv("TMPDIR", open)
	// Deny the private per-user directory as well, so the world-writable
	// candidate is the one the old ordering would have taken.
	useTempRoot(t, filepath.Join(open, "not-a-directory"))
	if err := os.WriteFile(filepath.Join(open, "not-a-directory"), nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	gitDir := shortTempDir(t)
	path, err := socketPath(gitDir)
	if err != nil {
		t.Fatalf("socketPath() error = %v", err)
	}
	if want := filepath.Join(gitDir, "workbook", socketFilename); path != want {
		t.Fatalf("socketPath() = %q, want the repository fallback %q", path, want)
	}
}

func TestUsableSocketDirRejectsDirectoriesOtherUsersCanWrite(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	readable := filepath.Join(root, "readable")
	if err := os.Mkdir(readable, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Chmod(readable, 0o755); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	for _, mode := range []os.FileMode{0o777, 0o733, 0o770} {
		hostile := filepath.Join(root, "hostile")
		if err := os.Mkdir(hostile, 0o700); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		if err := os.Chmod(hostile, mode); err != nil {
			t.Fatalf("Chmod() error = %v", err)
		}
		if err := usableSocketDir(hostile); err == nil {
			t.Fatalf("usableSocketDir(%v) error = nil, want a rejection", mode)
		}
		if err := os.Remove(hostile); err != nil {
			t.Fatalf("Remove() error = %v", err)
		}
	}

	if err := usableSocketDir(link); err == nil {
		t.Fatal("usableSocketDir(symlink) error = nil, want a rejection")
	}
	if err := usableSocketDir(filepath.Join(root, "absent")); err == nil {
		t.Fatal("usableSocketDir(absent) error = nil, want a rejection")
	}
	if err := usableSocketDir(private); err != nil {
		t.Fatalf("usableSocketDir(0700) error = %v", err)
	}
	if err := usableSocketDir(readable); err != nil {
		t.Fatalf("usableSocketDir(0755) error = %v", err)
	}
}

// The owner comparison is the only check that stands between the fix and the
// attack this hardening exists to stop: another local user who pre-creates the
// directory 0700 passes the symlink, is-a-directory, and group-or-other-write
// checks, and is caught by nothing else. No test can chown a directory to a
// second user, so the reported uid is what moves instead.
func TestUsableSocketDirRefusesADirectoryItDoesNotOwn(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := usableSocketDir(directory); err != nil {
		t.Fatalf("usableSocketDir(owned) error = %v", err)
	}

	useUID(t, os.Getuid()+1)
	err := usableSocketDir(directory)
	if err == nil {
		t.Fatal("usableSocketDir(owned by another user) error = nil, want a rejection")
	}
	if !strings.Contains(err.Error(), "another user") {
		t.Fatalf("usableSocketDir(owned by another user) error = %v, want it to name the owner", err)
	}
}

// The same check guards the private directory, where it matters most: a 0700
// directory somebody else created first is indistinguishable from ours by mode
// alone, and MkdirAll accepts it.
func TestUserTempDirRefusesADirectoryItDoesNotOwn(t *testing.T) {
	useTempRoot(t, t.TempDir())
	useUID(t, os.Getuid()+1)

	if _, err := userTempDir(""); err == nil {
		t.Fatal("userTempDir(owned by another user) error = nil, want a rejection")
	}
}

func TestUserTempDirRefusesADirectoryThatIsNotPrivate(t *testing.T) {
	root := t.TempDir()
	useTempRoot(t, root)

	directory, err := userTempDir("")
	if err != nil {
		t.Fatalf("userTempDir() error = %v", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatalf("Lstat() error = %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("userTempDir() created %v, want 0700", info.Mode().Perm())
	}

	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if _, err := userTempDir(""); err == nil {
		t.Fatal("userTempDir(loosened) error = nil, want a rejection")
	}
}

// One squatted name must not be the end of it. /tmp/workbook-<uid> is derived
// from nothing but the uid, so a local user can take it before this user's
// first watcher ever runs and keep it: a sticky /tmp refuses this process the
// rmdir. With os.TempDir() being /tmp itself on Linux and the repository
// fallback over 100 bytes on a deep checkout, that one fixed name would
// otherwise deny the watcher permanently.
func TestSocketPathTriesASecondPrivateDirectoryWhenTheFirstIsTaken(t *testing.T) {
	root := shortTempDir(t)
	useTempRoot(t, root)
	taken := filepath.Join(root, "workbook-"+strconv.Itoa(os.Getuid()))
	if err := os.Mkdir(taken, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	// Loose permissions stand in for a foreign owner: userTempDir refuses both,
	// and only one of them a test can create.
	if err := os.Chmod(taken, 0o777); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	open := shortTempDir(t)
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Setenv("TMPDIR", open)

	path, err := socketPath(t.TempDir())
	if err != nil {
		t.Fatalf("socketPath() error = %v", err)
	}
	directory := filepath.Dir(path)
	if directory == taken {
		t.Fatalf("socketPath() = %q, want a directory other than the squatted %q", path, taken)
	}
	if parent := filepath.Dir(directory); parent != root {
		t.Fatalf("socketPath() = %q, want a second private directory under %q", path, root)
	}
	if err := usableSocketDir(directory); err != nil {
		t.Fatalf("socketPath() chose a directory another user can interfere with: %v", err)
	}
	if len(path) > maxSocketPath {
		t.Fatalf("socketPath() = %q (%d bytes), want at most %d", path, len(path), maxSocketPath)
	}
}

func TestSocketPathFallsBackWhenTheCandidateIsTooLong(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 90))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	t.Setenv("TMPDIR", long)
	useTempRoot(t, long)

	gitDir := shortTempDir(t)
	path, err := socketPath(gitDir)
	if err != nil {
		t.Fatalf("socketPath() error = %v", err)
	}
	if len(path) > maxSocketPath {
		t.Fatalf("socketPath() = %q (%d bytes), want at most %d", path, len(path), maxSocketPath)
	}
	if strings.HasPrefix(path, long) {
		t.Fatalf("socketPath() = %q, want a fallback outside the oversized temporary directory", path)
	}
	if want := filepath.Join(gitDir, "workbook", socketFilename); path != want {
		t.Fatalf("socketPath() = %q, want the repository fallback %q", path, want)
	}
}

// The socket is never world-connectable, at any instant. It is bound under a
// name nobody dials and renamed into place already restricted, so the mode
// asserted here is the only one the advertised path has ever had — under the
// umask 0 that would otherwise have left it open.
func TestListenPrivateCreatesASocketNobodyElseCanConnectTo(t *testing.T) {
	previous := syscall.Umask(0)
	defer syscall.Umask(previous)

	path := filepath.Join(shortTempDir(t), "w.sock")
	listener, err := listenPrivate(path)
	if err != nil {
		t.Fatalf("listenPrivate() error = %v", err)
	}
	defer listener.Close()

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(%q) error = %v", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("listenPrivate() created %q with mode %v, want no access for other users", path, info.Mode().Perm())
	}
	if got := syscall.Umask(0); got != 0 {
		t.Fatalf("listenPrivate() left the process umask at %#o, want it restored", got)
	}
}

// A bind must not change what any other file this process creates comes out as.
// It used to: a umask of 0177 was set around the listen and restored after, and
// syscall.Umask is process-wide. `workbook serve` binds this socket on its
// watcher goroutine while the board runs git on another, so a `git hash-object
// -w --stdin` that landed in the window created its loose-object fan-out
// directory with mode 0600 — mkdir asks for 0777 and the umask took the owner's
// search bit along with everyone else's. Git could not then write into the
// directory it had just made, and every later object sharing those two hex
// digits failed with "insufficient permission for adding an object to
// repository database" in a repository that stayed broken after the watcher
// exited, because the directory did.
//
// The window is where beforeRename runs, so the directory is created exactly
// where the umask used to be in force rather than racing for it.
func TestListenPrivateDoesNotRestrictWhatTheRestOfTheProcessCreates(t *testing.T) {
	previous := syscall.Umask(0)
	defer syscall.Umask(previous)

	directory := filepath.Join(t.TempDir(), "objects-fanout")
	restore := beforeRename
	beforeRename = func() {
		if err := os.Mkdir(directory, 0o777); err != nil {
			t.Errorf("Mkdir() during the bind: %v", err)
		}
	}
	defer func() { beforeRename = restore }()

	path := filepath.Join(shortTempDir(t), "w.sock")
	listener, err := listenPrivate(path)
	if err != nil {
		t.Fatalf("listenPrivate() error = %v", err)
	}
	defer listener.Close()

	info, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", directory, err)
	}
	// The search bit is the one that matters. Without it the directory can be
	// created and never written to again, which is the failure git reports.
	if mode := info.Mode().Perm(); mode&0o700 != 0o700 {
		t.Fatalf("a directory created during the bind has mode %v, want the owner's own rwx intact", mode)
	}
}

// The socket serves at the path clients dial, not at the one it was bound
// under. Renaming a bound socket keeps the listener working because the
// listener holds the inode rather than the name, and nothing is left behind at
// the temporary name for the next bind to trip over.
func TestListenPrivateServesAtTheRenamedPath(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "w.sock")
	listener, err := listenPrivate(path)
	if err != nil {
		t.Fatalf("listenPrivate() error = %v", err)
	}
	defer listener.Close()

	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer conn.Close()
		_, err = conn.Write([]byte("ok\n"))
		served <- err
	}()

	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("Dial(%q) after the rename: %v", path, err)
	}
	defer conn.Close()
	if err := <-served; err != nil {
		t.Fatalf("serving the renamed socket: %v", err)
	}

	if _, err := os.Lstat(path + bindSuffix); !os.IsNotExist(err) {
		t.Fatalf("Lstat(%q) error = %v, want the temporary bind name to be gone", path+bindSuffix, err)
	}
}

// A watcher killed between the bind and the rename leaves the temporary name
// behind. A leftover socket file fails the next bind with "address already in
// use", so the next bind clears it rather than refusing to start.
func TestListenPrivateBindsOverALeftoverTemporaryName(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "w.sock")
	abandoned, err := net.Listen("unix", path+bindSuffix)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	// Closing normally would unlink the file, which is the opposite of the
	// state under test: a killed watcher leaves the socket on disk.
	abandoned.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := abandoned.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	listener, err := listenPrivate(path)
	if err != nil {
		t.Fatalf("listenPrivate() over a leftover %q: %v", path+bindSuffix, err)
	}
	defer listener.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("Lstat(%q) error = %v, want the socket at the advertised path", path, err)
	}
}

func TestBindCreatesASocketOnlyTheOwnerCanConnectTo(t *testing.T) {
	previous := syscall.Umask(0)
	defer syscall.Umask(previous)

	listener, path, err := bind(t.TempDir())
	if err != nil {
		t.Fatalf("bind() error = %v", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(path)
	}()

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(%q) error = %v", path, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bind() created %q with mode %v, want 0600", path, info.Mode().Perm())
	}
	if err := usableSocketDir(filepath.Dir(path)); err != nil {
		t.Fatalf("bind() chose a directory another user can interfere with: %v", err)
	}
}

// A watcher that already owns the repository keeps it even when it listens
// somewhere this version would never choose. Moving the preferred path into a
// private directory would otherwise let a new watcher start beside a
// pre-upgrade one, overwrite its pointer, and run a second loop against the
// same refs — with README still promising that an external watcher keeps
// ownership.
func TestBindRefusesWhenTheRecordedSocketAnswersElsewhere(t *testing.T) {
	directory := t.TempDir()
	legacy := filepath.Join(shortTempDir(t), "wb-legacy.sock")
	listener, err := net.Listen("unix", legacy)
	if err != nil {
		t.Fatalf("Listen(%q) error = %v", legacy, err)
	}
	defer listener.Close()

	published := pointer{Format: PointerFormat, Version: PointerVersion, Socket: legacy, PID: os.Getpid()}
	if err := writePointer(directory, published); err != nil {
		t.Fatalf("writePointer() error = %v", err)
	}

	chosen, err := socketPath(directory)
	if err != nil {
		t.Fatalf("socketPath() error = %v", err)
	}
	if chosen == legacy {
		t.Fatalf("socketPath() = %q, want a path other than the recorded one", chosen)
	}
	if _, _, err := bind(directory); !errors.Is(err, ErrWatcherLive) {
		t.Fatalf("bind() error = %v, want ErrWatcherLive", err)
	}
}

// A pointer left behind by SIGKILL names a socket nothing answers, which is the
// ordinary restart. It must not keep the next watcher out.
func TestBindProceedsWhenTheRecordedSocketIsDead(t *testing.T) {
	directory := t.TempDir()
	published := pointer{
		Format:  PointerFormat,
		Version: PointerVersion,
		Socket:  filepath.Join(directory, "absent.sock"),
		PID:     os.Getpid(),
	}
	if err := writePointer(directory, published); err != nil {
		t.Fatalf("writePointer() error = %v", err)
	}

	listener, path, err := bind(directory)
	if err != nil {
		t.Fatalf("bind() error = %v", err)
	}
	_ = listener.Close()
	_ = os.Remove(path)
}

// --- helpers ---

func useTempRoot(t *testing.T, root string) {
	t.Helper()
	previous := userTempRoot
	userTempRoot = root
	t.Cleanup(func() { userTempRoot = previous })
}

// shortTempDir is a temporary directory short enough to hold a bound socket.
// t.TempDir() names itself after the test, and sun_path is 104 bytes.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wb")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// useUID makes this process report a uid it does not have, which is how a test
// reaches the foreign-owner rejection without a second user account.
func useUID(t *testing.T, uid int) {
	t.Helper()
	previous := currentUID
	currentUID = func() int { return uid }
	t.Cleanup(func() { currentUID = previous })
}
