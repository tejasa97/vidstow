//go:build windows

package store

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsSecurityValidationChecksCurrentOwnerAndReparseState(t *testing.T) {
	dir := t.TempDir()
	parentHandle, err := openWindows(filepath.Dir(dir), true, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.OPEN_EXISTING, false)
	if err != nil {
		t.Fatalf("intermediate parent read/list validation: %v", err)
	}
	windows.CloseHandle(parentHandle)
	if err := validateStateDirectory(dir); err != nil {
		t.Fatalf("directory ACL validation: %v", err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateRegularFile(path); err != nil {
		t.Fatalf("file ACL validation: %v", err)
	}
	link := filepath.Join(dir, "state-link.json")
	if err := os.Symlink(path, link); err == nil {
		if validatePrivateRegularFile(link) == nil {
			t.Fatal("reparse-point file accepted")
		}
	}
}

func TestWindowsACLPolicyAllowsIntermediateReadButRejectsUntrustedMutation(t *testing.T) {
	read := privateReadMask(true)
	write := privateWriteMask(true)
	if rejectWindowsAllowedACE(read, true, false, false) {
		t.Fatal("intermediate read/list ACE was rejected")
	}
	if !rejectWindowsAllowedACE(write, true, false, false) {
		t.Fatal("intermediate untrusted write ACE was accepted")
	}
	if !rejectWindowsAllowedACE(read, false, true, false) {
		t.Fatal("private-file untrusted read ACE was accepted")
	}
	if rejectWindowsAllowedACE(write, false, true, true) {
		t.Fatal("trusted writer ACE was rejected")
	}
}

func TestWindowsPrivateObjectsReplaceInheritedReadACLWithProtectedDACL(t *testing.T) {
	parent := t.TempDir()
	if err := addInheritedWorldACE(parent, uint32(windows.FILE_GENERIC_READ)); err != nil {
		t.Fatalf("inheritable read fixture: %v", err)
	}
	stateDir := filepath.Join(parent, "private-state")
	if err := ensureStateDirectory(stateDir); err != nil {
		t.Fatalf("private state directory: %v", err)
	}
	assertProtectedPrivateObject(t, stateDir, true)

	paths := make([]string, 0, 5)
	state, err := createPrivateExclusive(filepath.Join(stateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, state.Name())
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := openPrivateLock(filepath.Join(stateDir, "state.json.lock"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, lock.Name())
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	temp, err := createPrivateTemp(stateDir, ".state-v2-")
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, temp.Name())
	if err := temp.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.json.pre-v2.bak", "state.json.recovery"} {
		file, err := createPrivateExclusive(filepath.Join(stateDir, name))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, file.Name())
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range paths {
		assertProtectedPrivateObject(t, path, false)
	}

	mutationParent := filepath.Join(parent, "mutation-parent")
	if err := os.Mkdir(mutationParent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := addInheritedWorldACE(mutationParent, uint32(windows.FILE_GENERIC_WRITE)); err != nil {
		t.Fatalf("inheritable write fixture: %v", err)
	}
	if h, err := openWindows(mutationParent, true, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.OPEN_EXISTING, false); err == nil {
		windows.CloseHandle(h)
		t.Fatal("intermediate untrusted mutation ACL was accepted")
	}
}

func addInheritedWorldACE(path string, mask uint32) error {
	h, err := openWindows(path, true, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.WRITE_DAC, windows.OPEN_EXISTING, false)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.ACCESS_MASK(mask),
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(world),
		},
	}}, dacl)
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func assertProtectedPrivateObject(t *testing.T, path string, directory bool) {
	t.Helper()
	h, err := openWindows(path, directory, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.OPEN_EXISTING, true)
	if err != nil {
		t.Fatalf("private object %q validation: %v", path, err)
	}
	defer windows.CloseHandle(h)
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("private object %q security: %v", path, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("private object %q control: %v", path, err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("private object %q inherited an unprotected DACL", path)
	}
}
