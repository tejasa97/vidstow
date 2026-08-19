//go:build linux

package diagnostics

import (
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

func CurrentPlatform() Platform {
	major := "0"
	var name unix.Utsname
	if err := unix.Uname(&name); err == nil {
		var release strings.Builder
		for _, value := range name.Release {
			if value == 0 {
				break
			}
			release.WriteByte(byte(value))
		}
		if value := strings.SplitN(release.String(), ".", 2)[0]; value != "" {
			major = value
		}
	}
	return Platform{OS: "linux", OSMajor: major, Architecture: runtime.GOARCH}
}
