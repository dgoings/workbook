//go:build windows

package historyvalidation

import (
	"context"
	"os"
	"syscall"
	"time"
)

// errSharingViolation is what Windows returns when a file is already open
// without the share mode a second opener asked for. It is this port's
// equivalent of EWOULDBLOCK from flock(2): somebody else holds the lock.
const errSharingViolation syscall.Errno = 32

// acquireInitializationLock takes the exclusive lock that serialises first-time
// construction of the validation cache, so that two commands starting at once
// build it once rather than twice.
//
// Windows has no flock(2). It has something stronger: a file opened with a
// share mode of zero cannot be opened by anyone else at all, and the kernel
// releases that claim when the handle closes — including when the holder
// crashes, which is the property the Unix path relies on. So the lock is the
// open itself, and the retry loop waits for a sharing violation to clear rather
// than for a lock call to succeed.
func acquireInitializationLock(ctx context.Context, path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, cacheError("open validation cache initialization lock", err)
	}
	for {
		handle, err := syscall.CreateFile(
			name,
			syscall.GENERIC_READ|syscall.GENERIC_WRITE,
			0, // no sharing: this is the lock
			nil,
			syscall.OPEN_ALWAYS,
			syscall.FILE_ATTRIBUTE_NORMAL,
			0,
		)
		if err == nil {
			return os.NewFile(uintptr(handle), path), nil
		}
		if err != errSharingViolation {
			return nil, cacheError("acquire validation cache initialization lock", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, cacheError("acquire validation cache initialization lock", ctx.Err())
		case <-timer.C:
		}
	}
}

// releaseInitializationLock closes the handle, which is what drops the claim.
func releaseInitializationLock(lock *os.File) {
	if lock == nil {
		return
	}
	_ = lock.Close()
}
