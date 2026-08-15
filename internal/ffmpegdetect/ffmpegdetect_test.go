package ffmpegdetect

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestProbeFFmpeg(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	status := Probe(context.Background(), "")
	if status.Available && status.Version == "" {
		t.Fatalf("Available ffmpeg must report a version")
	}
}

func TestConfigureRejectsInvalidPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	status := ConfigurePath(context.Background(), "/definitely/not/a/real/path/ffmpeg")
	if status.Available {
		t.Fatalf("ConfigurePath returned Available=true for an invalid path: %+v", status)
	}
	if status.Message == "" {
		t.Fatalf("expected an explanatory message")
	}
}

func TestConfigureUsesSiblingFFprobe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test binaries")
	}
	binDir := fakeToolDir(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Exercise the configured-pair behavior directly with a generous test
	// deadline. ConfigurePath's production four-second UI timeout remains
	// covered by its own boundary; sharing it across two subprocesses made this
	// fixture scheduler-sensitive under aggregate race load.
	status := probeConfigured(ctx, filepath.Join(binDir, "ffmpeg"))
	if !status.Available {
		t.Fatalf("configured pair should be available: %+v", status)
	}
	if status.Path != filepath.Join(binDir, "ffmpeg") {
		t.Fatalf("ffmpeg path = %q; want configured path", status.Path)
	}
	if status.FFprobePath != filepath.Join(binDir, "ffprobe") {
		t.Fatalf("ffprobe path = %q; want sibling path", status.FFprobePath)
	}
}

func TestConfigureRejectsMissingSiblingFFprobe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test binaries")
	}
	binDir := fakeToolDir(t, false)
	status := ConfigurePath(context.Background(), filepath.Join(binDir, "ffmpeg"))
	if status.Available {
		t.Fatalf("missing sibling ffprobe should be unavailable: %+v", status)
	}
	if status.FFprobePath != filepath.Join(binDir, "ffprobe") {
		t.Fatalf("ffprobe path = %q; want sibling path", status.FFprobePath)
	}
	if status.Message == "" {
		t.Fatalf("expected an explanatory message")
	}
}

func TestProbeUsesConfiguredPATHPair(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test binaries")
	}
	binDir := fakeToolDir(t, true)
	t.Setenv("PATH", binDir)
	status := Probe(context.Background(), "")
	if !status.Available {
		t.Fatalf("PATH pair should be available: %+v", status)
	}
	if status.Path != filepath.Join(binDir, "ffmpeg") || status.FFprobePath != filepath.Join(binDir, "ffprobe") {
		t.Fatalf("resolved tools = %q / %q; want PATH pair", status.Path, status.FFprobePath)
	}
}

func TestProbeFallsBackToWellKnownDirWhenPATHMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test binaries")
	}
	binDir := fakeToolDir(t, true)
	t.Setenv("PATH", t.TempDir())
	withExtraBinDirs(t, []string{binDir})
	status := Probe(context.Background(), "")
	if !status.Available {
		t.Fatalf("well-known pair should be available: %+v", status)
	}
	if status.Path != filepath.Join(binDir, "ffmpeg") || status.FFprobePath != filepath.Join(binDir, "ffprobe") {
		t.Fatalf("resolved tools = %q / %q; want well-known pair", status.Path, status.FFprobePath)
	}
	if status.Message != "ffmpeg detected" {
		t.Fatalf("message = %q; want ffmpeg detected", status.Message)
	}
}

func TestProbePrefersPATHOverWellKnownDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test binaries")
	}
	pathDir := fakeToolDir(t, true)
	extraDir := fakeToolDir(t, true)
	t.Setenv("PATH", pathDir)
	withExtraBinDirs(t, []string{extraDir})
	status := Probe(context.Background(), "")
	if !status.Available {
		t.Fatalf("PATH pair should be available: %+v", status)
	}
	if status.Path != filepath.Join(pathDir, "ffmpeg") {
		t.Fatalf("ffmpeg path = %q; want PATH pair over well-known dir", status.Path)
	}
}

func TestProbeFallsBackToWellKnownPairWhenPATHIsIncomplete(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test binaries")
	}
	pathDir := fakeToolDir(t, false)
	extraDir := fakeToolDir(t, true)
	t.Setenv("PATH", pathDir)
	withExtraBinDirs(t, []string{extraDir})
	status := Probe(context.Background(), "")
	if !status.Available {
		t.Fatalf("well-known pair should replace incomplete PATH pair: %+v", status)
	}
	if status.Path != filepath.Join(extraDir, "ffmpeg") || status.FFprobePath != filepath.Join(extraDir, "ffprobe") {
		t.Fatalf("resolved tools = %q / %q; want complete well-known pair", status.Path, status.FFprobePath)
	}
}

func TestProbeWellKnownDirRequiresSiblingFFprobe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test binaries")
	}
	binDir := fakeToolDir(t, false)
	t.Setenv("PATH", t.TempDir())
	withExtraBinDirs(t, []string{binDir})
	status := Probe(context.Background(), "")
	if status.Available {
		t.Fatalf("missing sibling ffprobe should be unavailable: %+v", status)
	}
	if status.Message != "ffmpeg was not found on PATH" {
		t.Fatalf("message = %q; want PATH miss after unusable well-known dir", status.Message)
	}
}

func TestProbeSkipsUnusableWellKnownDirThenUsesNext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test binaries")
	}
	badDir := fakeToolDir(t, false)
	goodDir := fakeToolDir(t, true)
	t.Setenv("PATH", t.TempDir())
	withExtraBinDirs(t, []string{badDir, goodDir})
	status := Probe(context.Background(), "")
	if !status.Available {
		t.Fatalf("second well-known pair should be available: %+v", status)
	}
	if status.Path != filepath.Join(goodDir, "ffmpeg") {
		t.Fatalf("ffmpeg path = %q; want second well-known pair", status.Path)
	}
}

func TestProbeConfiguredPathIgnoresWellKnownDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test binaries")
	}
	configured := fakeToolDir(t, true)
	extraDir := fakeToolDir(t, true)
	t.Setenv("PATH", t.TempDir())
	withExtraBinDirs(t, []string{extraDir})
	status := Probe(context.Background(), filepath.Join(configured, "ffmpeg"))
	if !status.Available {
		t.Fatalf("configured pair should be available: %+v", status)
	}
	if status.Path != filepath.Join(configured, "ffmpeg") {
		t.Fatalf("ffmpeg path = %q; want configured path", status.Path)
	}
}

func withExtraBinDirs(t *testing.T, dirs []string) {
	t.Helper()
	original := extraBinDirs
	extraBinDirs = func() []string { return dirs }
	t.Cleanup(func() { extraBinDirs = original })
}

func TestDefaultExtraBinDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		if dirs := defaultExtraBinDirs(); dirs != nil {
			t.Fatalf("windows extra dirs = %v; want nil", dirs)
		}
		return
	}
	got := defaultExtraBinDirs()
	want := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("extra dirs = %v; want %v", got, want)
	}
}

func TestProbeFindsHostHomebrewWhenPATHEmpty(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Homebrew prefixes are a macOS fallback")
	}
	homebrew := filepath.Join("/opt/homebrew/bin", "ffmpeg")
	if _, err := os.Stat(homebrew); err != nil {
		homebrew = filepath.Join("/usr/local/bin", "ffmpeg")
		if _, err := os.Stat(homebrew); err != nil {
			t.Skip("host has no Homebrew ffmpeg")
		}
	}
	t.Setenv("PATH", t.TempDir())
	status := Probe(context.Background(), "")
	if !status.Available {
		t.Fatalf("expected Homebrew fallback: %+v", status)
	}
	if status.Path != homebrew {
		t.Fatalf("ffmpeg path = %q; want %s", status.Path, homebrew)
	}
	if status.FFprobePath != filepath.Join(filepath.Dir(homebrew), "ffprobe") {
		t.Fatalf("ffprobe path = %q; want sibling of %s", status.FFprobePath, homebrew)
	}
}

func fakeToolDir(t *testing.T, includeFFprobe bool) string {
	t.Helper()
	dir := t.TempDir()
	script := []byte("#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then\n  echo \"ffmpeg version desktop-test\"\nfi\n")
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	if includeFFprobe {
		probeScript := []byte("#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then\n  echo \"ffprobe version desktop-test\"\nfi\n")
		if err := os.WriteFile(filepath.Join(dir, "ffprobe"), probeScript, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
