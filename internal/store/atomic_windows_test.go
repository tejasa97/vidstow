//go:build windows

package store

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
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

func TestWindowsReplaceFileMapsAmbiguousErrorsToIndeterminate(t *testing.T) {
	old := replaceFileCall
	defer func() { replaceFileCall = old }()
	for _, fault := range []error{windows.ERROR_UNABLE_TO_REMOVE_REPLACED, windows.ERROR_UNABLE_TO_MOVE_REPLACEMENT, windows.ERROR_UNABLE_TO_MOVE_REPLACEMENT_2} {
		replaceFileCall = func(*uint16, *uint16) error { return fault }
		result := replaceLocal(`C:\\state.tmp`, `C:\\state.json`)
		if !result.indeterminate || result.committed || result.err != fault {
			t.Fatalf("fault %v -> %#v", fault, result)
		}
	}
	replaceFileCall = func(*uint16, *uint16) error { return windows.ERROR_ACCESS_DENIED }
	if result := replaceLocal(`C:\\state.tmp`, `C:\\state.json`); result.indeterminate || result.committed || result.err != windows.ERROR_ACCESS_DENIED {
		t.Fatalf("pre-commit fault -> %#v", result)
	}
}

func TestWindowsInstallNewStateUsesMoveFileExWithoutReplace(t *testing.T) {
	oldReplace, oldInstall := replaceFileCall, installFileCall
	defer func() { replaceFileCall, installFileCall = oldReplace, oldInstall }()
	replaceFileCall = func(*uint16, *uint16) error { return windows.ERROR_FILE_NOT_FOUND }
	called := false
	installFileCall = func(*uint16, *uint16) error { called = true; return nil }
	if result := replaceLocal(`C:\\state.tmp`, `C:\\state.json`); result.err != nil || result.committed || result.indeterminate || !called {
		t.Fatalf("install-new result = %#v called=%v", result, called)
	}
}

func TestWindowsInstallNewFaultVectors(t *testing.T) {
	oldReplace, oldInstall := replaceFileCall, installFileCall
	defer func() { replaceFileCall, installFileCall = oldReplace, oldInstall }()
	replaceFileCall = func(*uint16, *uint16) error { return windows.ERROR_FILE_NOT_FOUND }
	for _, fault := range []error{windows.ERROR_UNABLE_TO_REMOVE_REPLACED, windows.ERROR_UNABLE_TO_MOVE_REPLACEMENT, windows.ERROR_UNABLE_TO_MOVE_REPLACEMENT_2} {
		installFileCall = func(*uint16, *uint16) error { return fault }
		if result := replaceLocal(`C:\\state.tmp`, `C:\\state.json`); !result.indeterminate || result.committed || result.err != fault {
			t.Fatalf("install fault %v -> %#v", fault, result)
		}
	}
}
