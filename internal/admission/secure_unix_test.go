//go:build !windows

package admission

import (
	"os"
	"testing"
)

// State v2 intentionally rejects a state path below an insecure ancestor.
// Keep admission tests under a private home-directory root so the test
// harness exercises the transaction rather than failing on the host's /tmp
// permissions (which are commonly 01777 on Linux CI).
func TestMain(m *testing.M) {
	home, err := os.UserHomeDir()
	if err != nil {
		os.Exit(1)
	}
	tempRoot, err := os.MkdirTemp(home, ".vidstow-admission-tests-")
	if err != nil {
		os.Exit(1)
	}
	if err := os.Chmod(tempRoot, 0o700); err != nil {
		_ = os.RemoveAll(tempRoot)
		os.Exit(1)
	}
	if err := os.Setenv("TMPDIR", tempRoot); err != nil {
		_ = os.RemoveAll(tempRoot)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(tempRoot)
	os.Exit(code)
}
