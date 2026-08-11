//go:build !windows

package store

import (
	"fmt"
	"os"
	"sync"
	"syscall"
)

type stateLock struct {
	mu   sync.Mutex
	file *os.File
}

var releaseStateLock = func(lock *stateLock) error { return lock.Close() }

func acquireStateLock(path string) (*stateLock, error) {
	f, err := openPrivateLock(path)
	if err != nil {
		return nil, fmt.Errorf("store: open state lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("store: lock state: %w", err)
	}
	return &stateLock{file: f}, nil
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
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if err != nil {
		return err
	}
	closeErr := f.Close()
	if closeErr == nil {
		l.file = nil
	}
	return closeErr
}
