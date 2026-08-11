//go:build !windows

package store

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var errUnsafePermissions = errors.New("store: unsafe state permissions")
var syncPrivateParent = syncParent
var validateUnixCreateParent = func(fd int) error { return validateUnixDirectoryFD(fd, true) }

func ensureStateDirectory(path string) error {
	d, err := openSecureDirectory(path, true)
	if err == nil {
		if chmodErr := d.Chmod(0o700); chmodErr != nil {
			err = chmodErr
		} else {
			err = validateUnixDirectoryFD(int(d.Fd()), true)
		}
	}
	if d != nil {
		d.Close()
	}
	return err
}

func canonicalizeStatePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(abs)), nil
}
func validateStateDirectory(path string) error {
	d, err := openSecureDirectory(path, false)
	if err == nil {
		err = validateUnixDirectoryFD(int(d.Fd()), true)
	}
	if d != nil {
		d.Close()
	}
	return err
}

func openSecureDirectory(path string, create bool) (*os.File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	clean := filepath.Clean(abs)
	if resolved, resolveErr := filepath.EvalSymlinks(clean); resolveErr == nil {
		clean = filepath.Clean(resolved)
	} else if create {
		parent, parentErr := filepath.EvalSymlinks(filepath.Dir(clean))
		if parentErr != nil {
			return nil, parentErr
		}
		clean = filepath.Join(parent, filepath.Base(clean))
	} else {
		return nil, resolveErr
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			if err := unix.Mkdirat(fd, component, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				unix.Close(fd)
				return nil, err
			}
			next, openErr = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		unix.Close(fd)
		if openErr != nil {
			return nil, fmt.Errorf("store: open state directory: %w", openErr)
		}
		fd = next
		if err := validateUnixDirectoryFD(fd, false); err != nil {
			unix.Close(fd)
			return nil, err
		}
	}
	return os.NewFile(uintptr(fd), clean), nil
}

func secureParent(path string) (*os.File, string, error) {
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) || strings.ContainsAny(base, `\\/`) {
		return nil, "", errUnsafePermissions
	}
	dir, err := openSecureDirectory(filepath.Dir(path), false)
	if err == nil {
		err = validateUnixDirectoryFD(int(dir.Fd()), true)
	}
	if err != nil && dir != nil {
		dir.Close()
		dir = nil
	}
	return dir, base, err
}

func openPrivateRead(path string) (*os.File, error) { return openPrivate(path, unix.O_RDONLY, false) }
func openPrivateLock(path string) (*os.File, error) { return openPrivate(path, unix.O_RDWR, true) }

func openPrivate(path string, flags int, create bool) (*os.File, error) {
	dir, base, err := secureParent(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), base, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) && create {
		fd, err = unix.Openat(int(dir.Fd()), base, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_CREAT|unix.O_EXCL, 0o600)
	}
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, errUnsafePermissions
		}
		return nil, fmt.Errorf("store: open private file: %w", err)
	}
	if err := validateUnixPrivateFD(fd); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(dir.Name(), base)), nil
}

func createPrivateExclusive(path string) (*os.File, error) {
	dir, base, err := secureParent(path)
	if err != nil {
		return nil, err
	}
	if err := validateUnixCreateParent(int(dir.Fd())); err != nil {
		dir.Close()
		return nil, err
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), base, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: create private file: %w", err)
	}
	if err := validateUnixPrivateFD(fd); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(dir.Name(), base)), nil
}

func createPrivateTemp(dirPath, prefix string) (*os.File, error) {
	dir, err := openSecureDirectory(dirPath, false)
	if err != nil {
		return nil, err
	}
	if err := validateUnixDirectoryFD(int(dir.Fd()), true); err != nil {
		dir.Close()
		return nil, err
	}
	defer dir.Close()
	for i := 0; i < 32; i++ {
		var token [12]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, err
		}
		name := fmt.Sprintf("%s%x", prefix, token)
		fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := validateUnixPrivateFD(fd); err != nil {
			unix.Close(fd)
			return nil, err
		}
		return os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name)), nil
	}
	return nil, errors.New("store: exhausted private temp names")
}

func validatePrivateRegularFile(path string) error {
	f, err := openPrivateRead(path)
	if f != nil {
		f.Close()
	}
	return err
}

func removePrivate(path string) error {
	dir, base, err := secureParent(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := unix.Unlinkat(int(dir.Fd()), base, 0); err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		return err
	}
	return syncPrivateParent(dir.Name())
}

func validateUnixPrivateFD(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o077 != 0 || int(stat.Uid) != os.Getuid() {
		return errUnsafePermissions
	}
	return nil
}

func validateUnixDirectoryFD(fd int, strict bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || (strict && int(stat.Uid) != os.Getuid()) || (strict && stat.Mode&0o077 != 0) || (!strict && stat.Mode&0o022 != 0) {
		return errUnsafePermissions
	}
	return nil
}
