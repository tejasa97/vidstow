//go:build linux

package reservationfs

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxUnknownFilesystemCasePolicyFailsClosed(t *testing.T) {
	fd, err := unix.Open(t.TempDir(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)

	var fs unix.Statfs_t
	if err := unix.Fstatfs(fd, &fs); err != nil {
		t.Fatal(err)
	}
	if linuxCasefoldFlagIsAuthoritative(fs.Type) {
		t.Skip("test filesystem has an authoritative per-directory casefold flag")
	}
	if _, err := detectPosixCaseSensitivity(fd, ""); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("detectPosixCaseSensitivity() error = %v, want ErrUnsupported", err)
	}
}

func TestLinuxCasefoldFlagAuthorityIsExplicitlyBounded(t *testing.T) {
	for _, fsType := range []int64{unix.EXT4_SUPER_MAGIC, unix.F2FS_SUPER_MAGIC} {
		if !linuxCasefoldFlagIsAuthoritative(fsType) {
			t.Errorf("filesystem type %#x should support the casefold flag", fsType)
		}
	}
	if linuxCasefoldFlagIsAuthoritative(0x12345678) {
		t.Fatal("unknown filesystem type unexpectedly supports the casefold flag")
	}
}
