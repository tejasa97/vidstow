//go:build windows

package diagnostics

import (
	"runtime"
	"strconv"

	"golang.org/x/sys/windows"
)

func CurrentPlatform() Platform {
	major := "0"
	if version := windows.RtlGetVersion(); version != nil {
		major = strconv.FormatUint(uint64(version.MajorVersion), 10)
	}
	return Platform{OS: "windows", OSMajor: major, Architecture: runtime.GOARCH}
}
