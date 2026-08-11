//go:build !windows

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

type stateLock struct{ file *os.File }

func acquireStateLock(path string) (*stateLock, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		f, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	}
	if err != nil {
		return nil, fmt.Errorf("store: open state lock: %w", err)
	}
	if err := validatePrivateRegularFile(path); err != nil {
		f.Close()
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("store: lock state: %w", err)
	}
	return &stateLock{file: f}, nil
}

func (l *stateLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
