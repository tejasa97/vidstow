//go:build darwin

package diagnostics

import (
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

func CurrentPlatform() Platform {
	major := "0"
	if version, err := unix.Sysctl("kern.osproductversion"); err == nil {
		if value := strings.SplitN(version, ".", 2)[0]; value != "" {
			major = value
		}
	}
	return Platform{OS: "macos", OSMajor: major, Architecture: runtime.GOARCH}
}
