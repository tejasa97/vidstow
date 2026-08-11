//go:build !windows && !darwin && !linux

package reservationfs

import "errors"

// The adapter deliberately supports only Unix platforms whose no-follow,
// directory-relative primitives and case facts are implemented above, plus
// native Windows. Other targets compile but fail closed at OpenRoot.
func openPlatformRoot(path string) (platformRoot, error) {
	if _, err := normalizeRootPath(path); err != nil {
		return nil, err
	}
	return nil, unsupportedError("open root", errors.New("no authority-preserving reservation filesystem backend for this target"))
}
