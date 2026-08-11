//go:build windows

package store

import (
	"os"
	"path/filepath"
	"testing"
)

// This runs in the native Windows matrix. It protects the local adapter's
// replacement contract independently of Unix directory fsync semantics.
func TestWindowsAtomicReplaceReplacesExistingState(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")
	temp := filepath.Join(dir, "state.tmp")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temp, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := (osAtomicReplacer{}).Replace(temp, target)
	if result.err != nil || result.committed || result.indeterminate {
		t.Fatalf("replace result = %#v", result)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "new" {
		t.Fatalf("replaced state = %q, %v", got, err)
	}
}
