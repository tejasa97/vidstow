//go:build linux

package reservationfs

import (
	"fmt"

	"github.com/tejasa97/vidstow/internal/reservation"
	"golang.org/x/sys/unix"
)

// FS_CASEFOLD_FL is the Linux VFS directory flag used by ext4 and other
// filesystems that opt into Unicode case-insensitive lookup. It is not
// exposed by every x/sys release, so keep the kernel ABI value local.
const fsCasefoldFlag uint32 = 0x40000000

func detectPosixCaseSensitivity(fd int, _ string) (bool, error) {
	var fs unix.Statfs_t
	if err := unix.Fstatfs(fd, &fs); err != nil {
		return false, unsupportedError("query Unix filesystem", err)
	}
	// FS_CASEFOLD_FL is authoritative for directories on ext-family and F2FS
	// filesystems. It is not a generic VFS case-policy query: a filesystem can
	// implement case-insensitive lookup without this inode flag. Refuse unknown
	// filesystems instead of incorrectly declaring them case-sensitive.
	if !linuxCasefoldFlagIsAuthoritative(fs.Type) {
		return false, unsupportedError("query Linux directory case policy", fmt.Errorf("filesystem type %#x has no supported authoritative case-policy query", uint64(fs.Type)))
	}
	flags, err := unix.IoctlGetUint32(fd, unix.FS_IOC_GETFLAGS)
	if err != nil {
		return false, unsupportedError("query Linux directory case policy", fmt.Errorf("filesystem type %#x: %w", uint64(fs.Type), err))
	}
	return flags&fsCasefoldFlag == 0, nil
}

func linuxCasefoldFlagIsAuthoritative(fsType int64) bool {
	return fsType == unix.EXT4_SUPER_MAGIC || fsType == unix.F2FS_SUPER_MAGIC
}

func posixNameComparison(caseSensitive bool) reservation.NameComparison {
	if caseSensitive {
		return reservation.ExactNames{}
	}
	return ConservativeFoldedNames{}
}
