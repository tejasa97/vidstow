//go:build windows

package store

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")
var moveFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("MoveFileExW")
var replaceFileCall = func(target, replacement *uint16) error {
	r1, _, callErr := replaceFileW.Call(uintptr(unsafe.Pointer(target)), uintptr(unsafe.Pointer(replacement)), 0, 0, 0, 0)
	if r1 != 0 {
		return nil
	}
	if callErr == nil {
		return syscall.EINVAL
	}
	return callErr
}
var installFileCall = func(target, replacement *uint16) error {
	r1, _, callErr := moveFileW.Call(uintptr(unsafe.Pointer(replacement)), uintptr(unsafe.Pointer(target)), uintptr(windows.MOVEFILE_WRITE_THROUGH), 0)
	if r1 != 0 {
		return nil
	}
	if callErr == nil {
		return syscall.EINVAL
	}
	return callErr
}

// replaceLocal uses ReplaceFileW for replace-existing targets and MoveFileExW
// only for the missing-target install-new case. Its three ambiguous errors
// mean Windows cannot prove which image has authority, so they quarantine.
func replaceLocal(tempPath, targetPath string) replaceResult {
	target, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		return replaceResult{err: err}
	}
	temp, err := windows.UTF16PtrFromString(tempPath)
	if err != nil {
		return replaceResult{err: err}
	}
	callErr := replaceFileCall(target, temp)
	if callErr == nil {
		return replaceResult{}
	}
	if callErr == windows.ERROR_FILE_NOT_FOUND || callErr == windows.ERROR_PATH_NOT_FOUND {
		installErr := installFileCall(target, temp)
		if installErr == nil {
			return replaceResult{}
		}
		switch installErr {
		case windows.ERROR_UNABLE_TO_REMOVE_REPLACED, windows.ERROR_UNABLE_TO_MOVE_REPLACEMENT, windows.ERROR_UNABLE_TO_MOVE_REPLACEMENT_2:
			return replaceResult{err: installErr, indeterminate: true}
		default:
			return replaceResult{err: installErr}
		}
	}
	switch callErr {
	case windows.ERROR_UNABLE_TO_REMOVE_REPLACED, windows.ERROR_UNABLE_TO_MOVE_REPLACEMENT, windows.ERROR_UNABLE_TO_MOVE_REPLACEMENT_2:
		return replaceResult{err: callErr, indeterminate: true}
	default:
		return replaceResult{err: callErr}
	}
}
