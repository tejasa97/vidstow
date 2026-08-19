//go:build windows

package diagnostics

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileLock struct {
	mu         sync.Mutex
	file       *os.File
	overlapped windows.Overlapped
}

func acquireFileLock(path string) (*fileLock, error) {
	if err := rejectSymlink(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	lock := &fileLock{file: f}
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &lock.overlapped); err != nil {
		f.Close()
		return nil, err
	}
	return lock, nil
}

func (l *fileLock) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	f := l.file
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &l.overlapped); err != nil {
		return err
	}
	err := f.Close()
	if err == nil {
		l.file = nil
	}
	return err
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return rejectDirectoryReparsePoint(path)
}

func openPrivateRead(path string) (*os.File, error) {
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	return os.Open(path)
}

func removeHistoryFile(path string) error {
	if err := rejectSymlink(path); err != nil {
		return err
	}
	return os.Remove(path)
}

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")
var moveFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("MoveFileExW")

func replaceFile(tempPath, targetPath string) error {
	if filepath.Clean(filepath.Dir(tempPath)) != filepath.Clean(filepath.Dir(targetPath)) {
		return errors.New("diagnostics: replacement crosses directories")
	}
	if err := rejectSymlink(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	target, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		return err
	}
	temp, err := windows.UTF16PtrFromString(tempPath)
	if err != nil {
		return err
	}
	if result, _, callErr := replaceFileW.Call(uintptr(unsafe.Pointer(target)), uintptr(unsafe.Pointer(temp)), 0, 0, 0, 0); result != 0 {
		return nil
	} else if callErr != windows.ERROR_FILE_NOT_FOUND && callErr != windows.ERROR_PATH_NOT_FOUND {
		if callErr == nil {
			return syscall.EINVAL
		}
		return callErr
	}
	if result, _, callErr := moveFileW.Call(uintptr(unsafe.Pointer(temp)), uintptr(unsafe.Pointer(target)), uintptr(windows.MOVEFILE_WRITE_THROUGH), 0); result != 0 {
		return nil
	} else if callErr == nil {
		return syscall.EINVAL
	} else {
		return callErr
	}
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeType != 0 && !info.Mode().IsRegular() {
		return errors.New("diagnostics: reparse point or non-regular file rejected")
	}
	return nil
}

func rejectDirectoryReparsePoint(path string) error {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("diagnostics: unsafe history directory")
	}
	return nil
}
