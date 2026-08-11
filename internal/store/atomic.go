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
	if err := replaceLocal(tempPath, targetPath); err != nil {
		return replaceResult{err: fmt.Errorf("store: atomic replace: %w", err)}
	}
	if err := syncParent(filepath.Dir(targetPath)); err != nil {
		return replaceResult{err: fmt.Errorf("store: sync state parent: %w", err), committed: true}
	}
	return replaceResult{}
}
