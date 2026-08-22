//go:build !windows

package diagnostics

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type fileLock struct{ file *os.File }

func acquireFileLock(path string) (*fileLock, error) {
	if err := rejectSymlink(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	if err := validatePrivateFileInfo(f); err != nil {
		f.Close()
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return &fileLock{file: f}, nil
}

func (l *fileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := rejectSymlink(path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || int(stat.Uid) != os.Getuid() {
		return errors.New("diagnostics: unsafe history directory")
	}
	return nil
}

func openPrivateRead(path string) (*os.File, error) {
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if err := validatePrivateFileInfo(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func validatePrivateFileInfo(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || int(stat.Uid) != os.Getuid() {
		return errors.New("diagnostics: unsafe history file")
	}
	return nil
}

func removeHistoryFile(path string) error {
	if err := rejectSymlink(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func replaceFile(tempPath, targetPath string) error {
	if filepath.Clean(filepath.Dir(tempPath)) != filepath.Clean(filepath.Dir(targetPath)) {
		return errors.New("diagnostics: replacement crosses directories")
	}
	if err := rejectSymlink(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(targetPath))
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("diagnostics: symlink rejected: %s", filepath.Base(path))
	}
	return nil
}
