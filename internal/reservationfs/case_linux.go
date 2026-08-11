//go:build linux

package reservationfs

import (
	"fmt"

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
	flags, err := unix.IoctlGetUint32(fd, unix.FS_IOC_GETFLAGS)
	if err != nil {
		return false, unsupportedError("query Linux directory case policy", fmt.Errorf("filesystem type %#x: %w", uint64(fs.Type), err))
	}
	return flags&fsCasefoldFlag == 0, nil
}
