//go:build darwin || linux

package reservationfs

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/tejasa97/vidstow/internal/reservation"
	"golang.org/x/sys/unix"
)

type posixRoot struct {
	fd              int
	path            string
	rootVolume      reservation.Volume
	isCaseSensitive bool
}

func openPlatformRoot(input string) (platformRoot, error) {
	path, err := normalizeRootPath(input)
	if err != nil {
		return nil, err
	}

	fd, err := openPosixDirectory(path)
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) && posixPathIsSymlink(path) {
			return nil, unsafeError("open root", fmt.Errorf("%w: %v", ErrSymlinkRoot, err))
		}
		return nil, unsafeError("open root", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = unix.Close(fd)
		}
	}()

	stat, err := posixStat(fd)
	if err != nil {
		return nil, unsafeError("stat root handle", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, unsafeError("validate root type", ErrInvalidRoot)
	}
	identity, err := posixIdentity(stat)
	if err != nil {
		return nil, err
	}
	caseSensitive, err := detectPosixCaseSensitivity(fd, path)
	if err != nil {
		return nil, err
	}

	volume := reservation.Volume{CanonicalPath: path, Identity: identity}
	if err := validateAdapterVolume(volume); err != nil {
		return nil, err
	}
	keep = true
	return &posixRoot{fd: fd, path: path, rootVolume: volume, isCaseSensitive: caseSensitive}, nil
}

func posixPathIsSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func openPosixDirectory(path string) (int, error) {
	return unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

func posixStat(fd int) (*unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	return &stat, nil
}

func posixIdentity(stat *unix.Stat_t) (string, error) {
	if stat == nil || stat.Ino == 0 {
		return "", unsupportedError("directory identity", errors.New("filesystem returned an empty inode"))
	}
	// st_dev plus st_ino identifies the opened directory within the mounted
	// Unix filesystem. Preserve the complete device number: Linux exposes a
	// 64-bit dev_t, and truncating it could make different mounts share an
	// identity. Keep the representation short enough for reservation's durable
	// Volume bound.
	identity := fmt.Sprintf("posix:%016x:%016x", stat.Dev, stat.Ino)
	if len(identity) > reservation.MaxVolumeIdentityBytes {
		return "", unsupportedError("directory identity", errors.New("directory identity exceeds reservation bound"))
	}
	return identity, nil
}

func (p *posixRoot) volume() reservation.Volume { return p.rootVolume }

func (p *posixRoot) nameComparison() reservation.NameComparison {
	return posixNameComparison(p.isCaseSensitive)
}

func (p *posixRoot) volumeComparison() reservation.VolumeComparison {
	return reservation.CanonicalVolumes{}
}

func (p *posixRoot) caseSensitive() bool { return p.isCaseSensitive }

func (p *posixRoot) probe(ctx context.Context, volume reservation.Volume, basename string) (reservation.Availability, error) {
	if err := ctx.Err(); err != nil {
		return reservation.Occupied, err
	}
	if volume != p.rootVolume {
		return reservation.Occupied, unsafeError("probe volume", ErrVolumeMismatch)
	}
	if err := p.verifyNamedRoot(); err != nil {
		return reservation.Occupied, err
	}

	var child unix.Stat_t
	err := unix.Fstatat(p.fd, basename, &child, unix.AT_SYMLINK_NOFOLLOW)
	availability := reservation.Available
	switch {
	case err == nil:
		// Fstatat with AT_SYMLINK_NOFOLLOW reports every existing directory
		// entry, including regular files, directories, symlinks, and special
		// filesystem entries. No child is opened or followed.
		availability = reservation.Occupied
	case errors.Is(err, unix.ENOENT):
		availability = reservation.Available
	default:
		return reservation.Occupied, unsafeError("inspect root child", err)
	}

	if err := ctx.Err(); err != nil {
		return reservation.Occupied, err
	}
	if err := p.verifyNamedRoot(); err != nil {
		return reservation.Occupied, err
	}
	return availability, nil
}

func (p *posixRoot) verifyNamedRoot() error {
	fd, err := openPosixDirectory(p.path)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return unsafeError("verify root identity", fmt.Errorf("%w: %v", ErrRootChanged, ErrSymlinkRoot))
		}
		return unsafeError("verify root identity", fmt.Errorf("%w: %v", ErrRootChanged, err))
	}
	stat, statErr := posixStat(fd)
	if statErr != nil {
		_ = unix.Close(fd)
		return unsafeError("verify root identity", fmt.Errorf("%w: %v", ErrRootChanged, statErr))
	}
	identity, identityErr := posixIdentity(stat)
	caseSensitive, caseErr := detectPosixCaseSensitivity(fd, p.path)
	closeErr := unix.Close(fd)
	if identityErr != nil {
		return identityErr
	}
	if caseErr != nil {
		return unsafeError("verify root case policy", fmt.Errorf("%w: %v", ErrRootChanged, caseErr))
	}
	if closeErr != nil {
		return unsafeError("close root verification handle", fmt.Errorf("%w: %v", ErrRootChanged, closeErr))
	}
	if identity != p.rootVolume.Identity || caseSensitive != p.isCaseSensitive {
		return unsafeError("verify root identity", ErrRootChanged)
	}
	return nil
}

func (p *posixRoot) close() error {
	if err := unix.Close(p.fd); err != nil {
		return unsafeError("close root", err)
	}
	return nil
}
