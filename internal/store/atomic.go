package store

import (
	"fmt"
	"path/filepath"
)

// osAtomicReplacer is deliberately app-owned. Its result makes the commit
// point explicit: rename success means the new image is authoritative even if
// the subsequent directory durability sync reports an error.
type osAtomicReplacer struct{}

func (osAtomicReplacer) Replace(tempPath, targetPath string) replaceResult {
	result := replaceLocal(tempPath, targetPath)
	if result.err != nil {
		result.err = fmt.Errorf("store: atomic replace: %w", result.err)
		return result
	}
	if err := syncParent(filepath.Dir(targetPath)); err != nil {
		return replaceResult{err: fmt.Errorf("store: sync state parent: %w", err), committed: true}
	}
	return replaceResult{}
}
