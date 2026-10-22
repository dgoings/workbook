package testenv

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Linux refuses to exec a file while any process holds it open for writing,
// failing with ETXTBSY ("text file busy"). os.WriteFile followed by an exec
// looks safe, because the descriptor is closed before the exec, but in a test
// binary other goroutines are forking at the same time: parallel tests, and
// the commands this one runs. A fork between the open and the close gives the
// child a copy of the write descriptor. Go opens every file with O_CLOEXEC, so
// that copy is closed when the child reaches its own exec -- not before. Until
// then the file still has a writer, and an exec of it fails (golang/go#22315).
//
// Writing to a temporary name and renaming it into place does not help on its
// own: the leaked descriptor refers to the file, not to its name, so the file
// renamed into place is the same one the child is still holding open.
//
// What does help is syscall.ForkLock. Every fork in the Go runtime --
// os/exec, os.StartProcess, syscall.ForkExec -- holds it for writing
// (syscall.acquireForkLock; concurrent forks may share that hold, but a reader
// never gets in while any of them is in progress), so holding it for reading
// keeps every fork out. The standard library does not do this for os.Open,
// which relies on O_CLOEXEC and, as the ForkLock doc puts it, lives with the
// race. WriteFile holds it for reading from the open to the close, so no
// child can be forked while the write descriptor exists: a child forked before
// the open never had it, and one forked after the close never sees it. Only a
// fork in this process can inherit the descriptor, and the lock covers every
// one.
//
// The rename is still worth having for a different reason: replacing a helper
// that some process may be running writes a new file beside it instead of
// opening the running one for writing, which Linux also refuses with ETXTBSY.

// WriteExecutable writes contents to path as an executable the test may run
// straight away, even while other tests fork, and fails the test if it cannot.
// Every test that writes a script or fake binary and then runs it, directly or
// through a program such as Git, should write it with this rather than
// os.WriteFile; TestNoSourceWritesAnExecutableWithoutTheForkLock enforces it.
func WriteExecutable(t testing.TB, path string, contents []byte) {
	t.Helper()
	if err := WriteFile(path, contents, 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

// WriteFile is os.WriteFile for a file that may be executed as soon as it is
// written, for a helper that reports an error rather than taking a
// *testing.T. Unlike os.WriteFile it sets perm exactly, without the umask and
// even when name already exists, and it replaces name atomically.
func WriteFile(name string, data []byte, perm fs.FileMode) error {
	temporaryPath, err := writeTemporary(filepath.Dir(name), filepath.Base(name), data, perm)
	if err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, name); err != nil {
		return errors.Join(err, os.Remove(temporaryPath))
	}
	return nil
}

// writeTemporary writes data to a new file in directory and returns its path.
// The file is open for writing only while ForkLock is held for reading.
func writeTemporary(directory, base string, data []byte, perm fs.FileMode) (string, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()

	file, err := os.CreateTemp(directory, "."+base+".tmp-*")
	if err != nil {
		return "", err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Chmod(perm)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", errors.Join(err, os.Remove(file.Name()))
	}
	return file.Name(), nil
}
