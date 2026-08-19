//go:build !windows

package diagnostics

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecorderUsesOwnerOnlyPermissionsAndRejectsUnsafeFile(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	dir := filepath.Join(t.TempDir(), "diagnostics")
	path := filepath.Join(dir, "diagnostics-v1.json")
	recorder, err := open(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(testProblemEvent(t, now)); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory permissions = %#o, want 0700", got)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("file permissions = %#o, want 0600", got)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := open(path, func() time.Time { return now }); err == nil {
		t.Fatal("Open accepted group/world-readable history")
	}
}

func TestRecorderRejectsSymlinkHistory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(`{"schema_version":1,"events":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "diagnostics-v1.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("Open accepted symlink history")
	}
}
