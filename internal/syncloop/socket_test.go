package syncloop

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
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
	open := filepath.Join(t.TempDir(), "open")
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

	gitDir := t.TempDir()
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

func TestUserTempDirRefusesADirectoryItDoesNotOwn(t *testing.T) {
	root := t.TempDir()
	useTempRoot(t, root)

	directory, err := userTempDir()
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
	if _, err := userTempDir(); err == nil {
		t.Fatal("userTempDir(loosened) error = nil, want a rejection")
	}
}

func TestSocketPathFallsBackWhenTheCandidateIsTooLong(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 90))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	t.Setenv("TMPDIR", long)
	useTempRoot(t, long)

	gitDir := t.TempDir()
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

// The socket is never world-connectable, at any instant. The chmod that
// follows the listen closes the window only after it has opened, so this
// asserts the mode the listen itself produced, under the umask 0 that made the
// window observable.
func TestListenPrivateCreatesASocketNobodyElseCanConnectTo(t *testing.T) {
	previous := syscall.Umask(0)
	defer syscall.Umask(previous)

	path := filepath.Join(t.TempDir(), "w.sock")
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

// --- helpers ---

func useTempRoot(t *testing.T, root string) {
	t.Helper()
	previous := userTempRoot
	userTempRoot = root
	t.Cleanup(func() { userTempRoot = previous })
}
