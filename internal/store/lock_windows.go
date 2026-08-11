//go:build windows

package store

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

type stateLock struct {
	mu         sync.Mutex
	file       *os.File
	overlapped windows.Overlapped
}

var releaseStateLock = func(lock *stateLock) error { return lock.Close() }

func acquireStateLock(path string) (*stateLock, error) {
	f, err := openPrivateLock(path)
	if err != nil {
		return nil, fmt.Errorf("store: open state lock: %w", err)
	}
	l := &stateLock{file: f}
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &l.overlapped); err != nil {
		f.Close()
		return nil, fmt.Errorf("store: lock state: %w", err)
	}
	return l, nil
}

func (l *stateLock) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	f := l.file
	err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &l.overlapped)
	if err != nil {
		return err
	}
	closeErr := f.Close()
	if closeErr == nil {
		l.file = nil
	}
	return closeErr
}
