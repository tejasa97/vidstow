//go:build linux

package reservationfs

import (
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPosixIdentityRetainsFullLinuxDeviceNumber(t *testing.T) {
	identity, err := posixIdentity(&unix.Stat_t{Dev: uint64(0x100000001), Ino: 7})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(identity, "0000000100000001") {
		t.Fatalf("identity = %q, want complete device number", identity)
	}
}
