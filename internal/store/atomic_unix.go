//go:build !windows

package store

import (
	"errors"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var errDifferentStateDirectory = errors.New("store: temp and state directories differ")

func replaceLocal(tempPath, targetPath string) replaceResult {
	dir, base, err := secureParent(targetPath)
	if err != nil {
		return replaceResult{err: err}
	}
	defer dir.Close()
	if filepath.Clean(filepath.Dir(tempPath)) != filepath.Clean(filepath.Dir(targetPath)) {
		return replaceResult{err: errDifferentStateDirectory}
	}
	if err := unix.Renameat(int(dir.Fd()), filepath.Base(tempPath), int(dir.Fd()), base); err != nil {
		return replaceResult{err: err}
	}
	return replaceResult{}
}

func syncParent(path string) error {
	dir, err := openSecureDirectory(path, false)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := validateUnixDirectoryFD(int(dir.Fd()), true); err != nil {
		return err
	}
	return dir.Sync()
}
