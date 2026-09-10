//go:build !windows

package historyvalidation

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// acquireInitializationLock takes the exclusive advisory lock that serialises
// first-time construction of the validation cache, so that two commands
// starting at once build it once rather than twice.
//
// flock(2) is the Unix mechanism, and it is advisory: it binds only processes
// that ask for it, which is every process that reaches this function. The lock
// is released when the file is closed, so a crashed holder never wedges the
// cache for the next run.
func acquireInitializationLock(ctx context.Context, path string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, cacheError("open validation cache initialization lock", err)
	}
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = lock.Close()
			return nil, cacheError("acquire validation cache initialization lock", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = lock.Close()
			return nil, cacheError("acquire validation cache initialization lock", ctx.Err())
		case <-timer.C:
		}
	}
}

func releaseInitializationLock(lock *os.File) {
	if lock == nil {
		return
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_ = lock.Close()
}
