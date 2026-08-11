//go:build !windows

package store

import "os"

func replaceLocal(tempPath, targetPath string) error { return os.Rename(tempPath, targetPath) }

func syncParent(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
