//go:build windows

package store

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

type stateLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

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
	l := &stateLock{file: f}
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &l.overlapped); err != nil {
		f.Close()
		return nil, fmt.Errorf("store: lock state: %w", err)
	}
	return l, nil
}

func (l *stateLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	closeErr := l.file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
