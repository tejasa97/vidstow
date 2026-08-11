//go:build windows

package reservationfs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"github.com/tejasa97/vidstow/internal/reservation"
	"golang.org/x/sys/windows"
)

type windowsRoot struct {
	handle          windows.Handle
	path            string
	rootVolume      reservation.Volume
	isCaseSensitive bool
}

// These layouts match FILE_ID_INFO, FILE_CASE_SENSITIVE_INFORMATION, and
// FILE_STANDARD_INFORMATION from the Windows SDK. x/sys exposes the query
// calls and constants but not all three result structs.
type fileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

type fileCaseSensitiveInfo struct {
	Flags uint32
}

type fileStandardInfo struct {
	AllocationSize int64
	EndOfFile      int64
	NumberOfLinks  uint32
	DeletePending  uint8
	Directory      uint8
	_              [2]byte
}

func openPlatformRoot(input string) (platformRoot, error) {
	path, err := normalizeRootPath(input)
	if err != nil {
		return nil, err
	}
	handle, err := openWindowsDirectory(path)
	if err != nil {
		return nil, unsafeError("open root", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = windows.CloseHandle(handle)
		}
	}()

	if err := validateWindowsDirectoryHandle(handle); err != nil {
		return nil, err
	}
	identity, err := windowsDirectoryIdentity(handle)
	if err != nil {
		return nil, err
	}
	caseSensitive, err := windowsCaseSensitivity(handle)
	if err != nil {
		return nil, err
	}
	volume := reservation.Volume{CanonicalPath: path, Identity: identity}
	if err := validateAdapterVolume(volume); err != nil {
		return nil, err
	}
	keep = true
	return &windowsRoot{handle: handle, path: path, rootVolume: volume, isCaseSensitive: caseSensitive}, nil
}

func openWindowsDirectory(path string) (windows.Handle, error) {
	pathp, err := windows.UTF16PtrFromString(windowsAPIPath(path))
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(
		pathp,
		windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.FILE_TRAVERSE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}

func windowsAPIPath(path string) string {
	if strings.HasPrefix(path, `\\?\`) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(path, `\\`)
	}
	if len(path) >= 2 && path[1] == ':' {
		return `\\?\` + path
	}
	return path
}

func validateWindowsDirectoryHandle(handle windows.Handle) error {
	var attributes windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &attributes); err != nil {
		return unsafeError("stat root handle", err)
	}
	if attributes.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return unsafeError("validate root handle", ErrSymlinkRoot)
	}
	var standard fileStandardInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileStandardInfo,
		(*byte)(unsafe.Pointer(&standard)),
		uint32(unsafe.Sizeof(standard)),
	); err != nil {
		return unsupportedError("query Windows root type", err)
	}
	if standard.Directory == 0 {
		return unsafeError("validate root type", ErrInvalidRoot)
	}
	return nil
}

func windowsDirectoryIdentity(handle windows.Handle) (string, error) {
	var info fileIDInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileIdInfo,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		return "", unsupportedError("query Windows directory identity", err)
	}
	zero := true
	for _, b := range info.FileID {
		if b != 0 {
			zero = false
			break
		}
	}
	if zero {
		return "", unsupportedError("query Windows directory identity", errors.New("filesystem returned an empty file ID"))
	}
	identity := fmt.Sprintf("windows:%016x:%x", info.VolumeSerialNumber, info.FileID)
	if len(identity) > reservation.MaxVolumeIdentityBytes {
		return "", unsupportedError("query Windows directory identity", errors.New("directory identity exceeds reservation bound"))
	}
	return identity, nil
}

func windowsCaseSensitivity(handle windows.Handle) (bool, error) {
	var info fileCaseSensitiveInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileCaseSensitiveInfo,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		return false, unsupportedError("query Windows directory case policy", err)
	}
	return info.Flags&windows.FILE_CS_FLAG_CASE_SENSITIVE_DIR != 0, nil
}

func (w *windowsRoot) volume() reservation.Volume { return w.rootVolume }

func (w *windowsRoot) nameComparison() reservation.NameComparison {
	if w.isCaseSensitive {
		return reservation.ExactNames{}
	}
	return ConservativeFoldedNames{}
}

func (w *windowsRoot) volumeComparison() reservation.VolumeComparison {
	return reservation.CanonicalVolumes{}
}

func (w *windowsRoot) caseSensitive() bool { return w.isCaseSensitive }

func (w *windowsRoot) probe(ctx context.Context, volume reservation.Volume, basename string) (reservation.Availability, error) {
	if err := ctx.Err(); err != nil {
		return reservation.Occupied, err
	}
	if volume != w.rootVolume {
		return reservation.Occupied, unsafeError("probe volume", ErrVolumeMismatch)
	}
	if err := w.verifyNamedRoot(); err != nil {
		return reservation.Occupied, err
	}

	child, err := w.openRelativeChild(basename)
	availability := reservation.Available
	switch {
	case err == nil:
		if closeErr := windows.CloseHandle(child); closeErr != nil {
			return reservation.Occupied, unsafeError("close inspected child", closeErr)
		}
		availability = reservation.Occupied
	case isWindowsMissingStatus(err):
		availability = reservation.Available
	case isWindowsReparseStatus(err):
		// OBJ_DONT_REPARSE can report a reparse point instead of returning
		// its handle. The name is still occupied, and must not be followed.
		availability = reservation.Occupied
	default:
		return reservation.Occupied, unsafeError("inspect root child", err)
	}

	if err := ctx.Err(); err != nil {
		return reservation.Occupied, err
	}
	if err := w.verifyNamedRoot(); err != nil {
		return reservation.Occupied, err
	}
	return availability, nil
}

func (w *windowsRoot) openRelativeChild(basename string) (windows.Handle, error) {
	name, err := windows.UTF16FromString(basename)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if len(name) < 2 {
		return windows.InvalidHandle, unsafeError("encode child basename", ErrInvalidProbe)
	}
	name = name[:len(name)-1]
	nameString := windows.NTUnicodeString{
		Length:        uint16(len(name) * 2),
		MaximumLength: uint16(len(name)*2 + 2),
		Buffer:        &name[0],
	}
	attributes := uint32(windows.OBJ_DONT_REPARSE)
	if !w.isCaseSensitive {
		attributes |= windows.OBJ_CASE_INSENSITIVE
	}
	objectAttributes := windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: w.handle,
		ObjectName:    &nameString,
		Attributes:    attributes,
	}
	var statusBlock windows.IO_STATUS_BLOCK
	var handle windows.Handle
	status := windows.NtCreateFile(
		&handle,
		windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		&objectAttributes,
		&statusBlock,
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if status != nil {
		return windows.InvalidHandle, status
	}
	return handle, nil
}

func isWindowsMissingStatus(err error) bool {
	return ntStatusIs(err, windows.STATUS_NO_SUCH_FILE) || ntStatusIs(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) || ntStatusIs(err, windows.STATUS_OBJECT_PATH_NOT_FOUND)
}

func isWindowsReparseStatus(err error) bool {
	return ntStatusIs(err, windows.STATUS_REPARSE_POINT_ENCOUNTERED)
}

func ntStatusIs(err error, want windows.NTStatus) bool {
	var status windows.NTStatus
	return errors.As(err, &status) && status == want
}

func (w *windowsRoot) verifyNamedRoot() error {
	handle, err := openWindowsDirectory(w.path)
	if err != nil {
		return unsafeError("verify root identity", fmt.Errorf("%w: %v", ErrRootChanged, err))
	}
	validErr := validateWindowsDirectoryHandle(handle)
	identity, identityErr := windowsDirectoryIdentity(handle)
	caseSensitive, caseErr := windowsCaseSensitivity(handle)
	closeErr := windows.CloseHandle(handle)
	if validErr != nil {
		return unsafeError("verify root identity", fmt.Errorf("%w: %v", ErrRootChanged, validErr))
	}
	if identityErr != nil {
		return unsafeError("verify root identity", fmt.Errorf("%w: %v", ErrRootChanged, identityErr))
	}
	if caseErr != nil {
		return unsafeError("verify root case policy", fmt.Errorf("%w: %v", ErrRootChanged, caseErr))
	}
	if closeErr != nil {
		return unsafeError("close root verification handle", fmt.Errorf("%w: %v", ErrRootChanged, closeErr))
	}
	if identity != w.rootVolume.Identity || caseSensitive != w.isCaseSensitive {
		return unsafeError("verify root identity", ErrRootChanged)
	}
	return nil
}

func (w *windowsRoot) close() error {
	if err := windows.CloseHandle(w.handle); err != nil {
		return unsafeError("close root", err)
	}
	return nil
}
