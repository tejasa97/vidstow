//go:build windows

package store

import "os"

// Windows MoveFileEx (used by os.Rename) is the local replacement primitive.
// Directory handles cannot be synced through os.File on Windows, so successful
// replacement remains the documented commit point.
func replaceLocal(tempPath, targetPath string) error { return os.Rename(tempPath, targetPath) }

func syncParent(path string) error { return nil }
