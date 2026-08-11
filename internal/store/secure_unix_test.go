//go:build !windows

package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	home, err := os.UserHomeDir()
	if err != nil {
		os.Exit(1)
	}
	tempRoot, err := os.MkdirTemp(home, ".vidstow-store-tests-")
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

func TestCreatePrivateExclusiveClosesParentWhenPostOpenValidationFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "marker")
	want := errors.New("injected parent validation failure")
	old := validateUnixCreateParent
	defer func() { validateUnixCreateParent = old }()
	var seenFD int
	validateUnixCreateParent = func(fd int) error {
		seenFD = fd
		return want
	}

	f, err := createPrivateExclusive(path)
	if f != nil || !errors.Is(err, want) {
		t.Fatalf("createPrivateExclusive = %v, %v", f, err)
	}
	if seenFD == 0 {
		t.Fatal("post-open validation seam was not called")
	}
	if _, err := unix.FcntlInt(uintptr(seenFD), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatalf("parent fd %d remained open: %v", seenFD, err)
	}
}
