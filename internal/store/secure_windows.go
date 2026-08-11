//go:build windows

package store

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var errUnsafePermissions = errors.New("store: unsafe state permissions")
var syncPrivateParent = syncParent

func canonicalizeStatePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	h, err := openWindows(filepath.Dir(abs), true, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.OPEN_EXISTING, false)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 512)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", err
		}
		if n < uint32(len(buf)-1) {
			final := windows.UTF16ToString(buf[:n])
			if strings.HasPrefix(final, `\\?\UNC\`) {
				final = `\\` + strings.TrimPrefix(final, `\\?\UNC\`)
			} else {
				final = strings.TrimPrefix(final, `\\?\`)
			}
			return filepath.Join(final, filepath.Base(abs)), nil
		}
		if len(buf) >= 32768 {
			return "", errors.New("store: canonical state path too long")
		}
		buf = make([]uint16, len(buf)*2)
	}
}

func ensureStateDirectory(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(abs)
	current := volume + `\`
	components := make([]string, 0)
	for _, component := range strings.Split(strings.TrimPrefix(strings.TrimPrefix(abs, volume), `\`), `\`) {
		if component != "" && component != "." {
			components = append(components, component)
		}
	}
	if len(components) == 0 {
		return errUnsafePermissions
	}
	for i, component := range components {
		current = filepath.Join(current, component)
		created := false
		if err := os.Mkdir(current, 0o700); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return err
			}
		} else {
			created = true
		}
		isFinal := i == len(components)-1
		access := uint32(windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL)
		if created || isFinal {
			access |= windows.WRITE_DAC
		}
		// A newly-created directory, or an existing final state directory, may
		// still carry inherited read ACEs until its protected DACL is installed.
		// Open with the intermediate policy, install the private DACL, then
		// validate the final handle strictly. Existing intermediate components
		// retain the less restrictive traversal policy.
		if h, err := openWindows(current, true, access, windows.OPEN_EXISTING, false); err != nil {
			return err
		} else {
			if created || isFinal {
				if err := installProtectedPrivateDACL(h, true); err != nil {
					windows.CloseHandle(h)
					return err
				}
				if err := validateWindowsHandle(h, true, true); err != nil {
					windows.CloseHandle(h)
					return err
				}
			}
			windows.CloseHandle(h)
		}
	}
	return nil
}

func validateStateDirectory(path string) error {
	h, err := openWindows(path, true, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.OPEN_EXISTING, true)
	if h != 0 {
		windows.CloseHandle(h)
	}
	return err
}
func validatePrivateRegularFile(path string) error {
	h, err := openWindows(path, false, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.OPEN_EXISTING, true)
	if h != 0 {
		windows.CloseHandle(h)
	}
	return err
}

func removePrivate(path string) error {
	parent, err := openWindows(filepath.Dir(path), true, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.OPEN_EXISTING, true)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	file, err := openWindows(path, false, windows.DELETE|windows.READ_CONTROL, windows.OPEN_EXISTING, true)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(file)
	// Delete through the already handle-validated object. DeleteFile(path)
	// would re-resolve the final name after validation and reintroduce a
	// symlink/reparse TOCTOU window.
	deleteFile := byte(1)
	if err := windows.SetFileInformationByHandle(file, windows.FileDispositionInfo, &deleteFile, uint32(unsafe.Sizeof(deleteFile))); err != nil {
		return err
	}
	return syncPrivateParent(filepath.Dir(path))
}

// syncParent intentionally has no directory-handle flush on Windows. The
// documented FlushFileBuffers contract covers file handles, not directory
// metadata handles, so treating a directory call as a durability primitive
// would turn every successful commit into a false recovery outcome. Private
// file contents are flushed with File.Sync, while ReplaceFileW and
// MoveFileExW(MOVEFILE_WRITE_THROUGH) provide the documented replacement
// durability available to this adapter. Directory-entry durability remains a
// platform limitation that is not separately observable through this adapter;
// a successful native replacement is therefore the Windows commit result
// rather than an invented directory-flush error.
func syncParent(string) error { return nil }

func openPrivateRead(path string) (*os.File, error) {
	return openPrivateWindowsFile(path, false, windows.GENERIC_READ|windows.READ_CONTROL, windows.OPEN_EXISTING)
}
func openPrivateLock(path string) (*os.File, error) {
	access := uint32(windows.GENERIC_READ | windows.GENERIC_WRITE | windows.READ_CONTROL | windows.WRITE_DAC)
	f, err := openPrivateWindowsFile(path, false, access, windows.CREATE_NEW)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	return openPrivateWindowsFile(path, false, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL, windows.OPEN_EXISTING)
}
func createPrivateExclusive(path string) (*os.File, error) {
	return openPrivateWindowsFile(path, false, windows.GENERIC_WRITE|windows.READ_CONTROL|windows.WRITE_DAC, windows.CREATE_NEW)
}

func openPrivateWindowsFile(path string, directory bool, access, disposition uint32) (*os.File, error) {
	h, err := openWindows(path, directory, access, disposition, true)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func openWindows(path string, directory bool, access, disposition uint32, strictOwner bool) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	attrs := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		attrs |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	h, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, disposition, attrs, 0)
	if err != nil {
		return 0, err
	}
	if disposition == windows.CREATE_NEW {
		if err := installProtectedPrivateDACL(h, directory); err != nil {
			windows.CloseHandle(h)
			return 0, err
		}
	}
	if err := validateWindowsHandle(h, directory, strictOwner); err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	return h, nil
}

func installProtectedPrivateDACL(h windows.Handle, directory bool) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return errUnsafePermissions
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return errUnsafePermissions
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return errUnsafePermissions
	}
	mask := privateOwnerAccessMask(directory)
	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, 3)
	for _, principal := range []struct {
		sid         *windows.SID
		trusteeType windows.TRUSTEE_TYPE
	}{
		{sid: user.User.Sid, trusteeType: windows.TRUSTEE_IS_USER},
		{sid: system, trusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP},
		{sid: administrators, trusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP},
	} {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.ACCESS_MASK(mask),
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  principal.trusteeType,
				TrusteeValue: windows.TrusteeValueFromSID(principal.sid),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return errUnsafePermissions
	}
	securityInfo := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if err := windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, securityInfo, nil, nil, acl, nil); err != nil {
		return errUnsafePermissions
	}
	return nil
}

func privateOwnerAccessMask(directory bool) uint32 {
	mask := uint32(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER)
	if directory {
		mask |= windows.FILE_LIST_DIRECTORY | 0x40 // FILE_DELETE_CHILD
	}
	return mask
}

func createPrivateTemp(dirPath, prefix string) (*os.File, error) {
	if err := validateStateDirectory(dirPath); err != nil {
		return nil, err
	}
	for i := 0; i < 32; i++ {
		var token [12]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, err
		}
		f, err := createPrivateExclusive(filepath.Join(dirPath, fmt.Sprintf("%s%x", prefix, token)))
		if errors.Is(err, windows.ERROR_FILE_EXISTS) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	return nil, errors.New("store: exhausted private temp names")
}

func validateWindowsHandle(h windows.Handle, directory, strictOwner bool) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return errUnsafePermissions
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		return errUnsafePermissions
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return errUnsafePermissions
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || (strictOwner && !owner.Equals(user.User.Sid)) {
		return errUnsafePermissions
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return errUnsafePermissions
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return errUnsafePermissions
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errUnsafePermissions
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if rejectWindowsAllowedACE(uint32(ace.Mask), directory, strictOwner, trustedWindowsACE(aceSID, user.User.Sid)) {
			return errUnsafePermissions
		}
	}
	return nil
}

func trustedWindowsACE(aceSID, currentUser *windows.SID) bool {
	return currentUser != nil && aceSID != nil && (currentUser.Equals(aceSID) || aceSID.IsWellKnown(windows.WinLocalSystemSid) || aceSID.IsWellKnown(windows.WinBuiltinAdministratorsSid))
}

func rejectWindowsAllowedACE(mask uint32, directory, strictOwner, trusted bool) bool {
	if trusted {
		return false
	}
	if strictOwner {
		return mask&(privateReadMask(directory)|privateWriteMask(directory)) != 0
	}
	return mask&privateWriteMask(directory) != 0
}

func privateReadMask(directory bool) uint32 {
	mask := uint32(windows.FILE_GENERIC_READ | windows.GENERIC_READ)
	if directory {
		mask |= windows.FILE_LIST_DIRECTORY
	}
	return mask
}

func privateWriteMask(directory bool) uint32 {
	mask := uint32(windows.FILE_GENERIC_WRITE | windows.GENERIC_WRITE | windows.GENERIC_ALL | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER)
	if directory {
		mask |= 0x40
	} // FILE_DELETE_CHILD
	return mask
}
