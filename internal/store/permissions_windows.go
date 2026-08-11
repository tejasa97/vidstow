//go:build windows

package store

import (
	"errors"
	"os"
)

var errUnsafePermissions = errors.New("store: unsafe state permissions")

// ACL ownership must be validated by the native Windows integration layer.
// V0 still rejects non-regular and reparse-point files and creates state/lock
// files with owner-only requested permissions.
func validatePrivateRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errUnsafePermissions
	}
	return nil
}

func validateStateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errUnsafePermissions
	}
	return nil
}
