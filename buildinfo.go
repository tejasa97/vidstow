package main

import (
	"runtime"
	"runtime/debug"
)

const (
	appVersion          = "0.1.0-beta.2"
	engineModulePath    = "github.com/tejasa97/youtube_dlp"
	pinnedEngineVersion = "v0.2.2"
)

// BuildInfo is the single backend-owned release identity exposed to the UI
// and support diagnostics. It avoids a second frontend version constant.
type BuildInfo struct {
	Version       string `json:"version"`
	EngineVersion string `json:"engineVersion"`
	OS            string `json:"os"`
	Architecture  string `json:"architecture"`
	GoVersion     string `json:"goVersion"`
}

func currentBuildInfo() BuildInfo {
	info := BuildInfo{
		Version:       appVersion,
		EngineVersion: pinnedEngineVersion,
		OS:            runtime.GOOS,
		Architecture:  runtime.GOARCH,
		GoVersion:     runtime.Version(),
	}
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if build.GoVersion != "" {
		info.GoVersion = build.GoVersion
	}
	for _, dependency := range build.Deps {
		if dependency.Path != engineModulePath {
			continue
		}
		if dependency.Version != "" && dependency.Version != "(devel)" {
			info.EngineVersion = dependency.Version
		}
		if dependency.Replace != nil && dependency.Replace.Version != "" && dependency.Replace.Version != "(devel)" {
			info.EngineVersion = dependency.Replace.Version
		}
		break
	}
	return info
}
