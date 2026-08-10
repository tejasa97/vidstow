package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreOpenCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if s.Settings().DownloadFolder == "" {
		t.Fatalf("default settings must include a download folder")
	}
}

func TestDefaultPathUsesVidStowIdentity(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if got := filepath.Base(path); got != "state.json" {
		t.Fatalf("state file = %q; want state.json", got)
	}
	if got := filepath.Base(filepath.Dir(path)); got != "vidstow" {
		t.Fatalf("config directory = %q; want vidstow", got)
	}
}

func TestStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	settings := s.Settings()
	settings.DownloadFolder = filepath.Join(dir, "downloads")
	settings.FFmpegPath = "/opt/local/bin/ffmpeg"
	if err := s.SetSettings(settings); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}

	mediaPath := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(mediaPath, []byte("fixture"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	entry := HistoryEntry{
		ID:           "abc",
		Title:        "Test",
		Channel:      "Ch",
		Quality:      "1080p",
		Container:    "MP4",
		VideoCodec:   "avc1",
		AudioCodec:   "mp4a",
		Filename:     "video.mp4",
		AbsolutePath: mediaPath,
		SizeBytes:    12345,
		CompletedAt:  "2025-01-01T00:00:00Z",
	}
	if err := s.AppendHistory(entry); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}

	// Open a second instance from the same path and confirm we read
	// the persisted state.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	if got := s2.Settings().DownloadFolder; got != settings.DownloadFolder {
		t.Fatalf("DownloadFolder = %q, want %q", got, settings.DownloadFolder)
	}
	if got := s2.Settings().FFmpegPath; got != settings.FFmpegPath {
		t.Fatalf("FFmpegPath = %q, want %q", got, settings.FFmpegPath)
	}
	if got := len(s2.History()); got != 1 {
		t.Fatalf("history length = %d, want 1", got)
	}
	if got := s2.History()[0].Title; got != "Test" {
		t.Fatalf("history title = %q, want Test", got)
	}
	if got := s2.History()[0].Container; got != "MP4" {
		t.Fatalf("history container = %q, want MP4", got)
	}
	if got := s2.History()[0].VideoCodec; got != "avc1" {
		t.Fatalf("history videoCodec = %q, want avc1", got)
	}
	if got := s2.History()[0].AudioCodec; got != "mp4a" {
		t.Fatalf("history audioCodec = %q, want mp4a", got)
	}
	if s2.History()[0].FileMissing {
		t.Fatal("expected existing history file to report FileMissing=false")
	}
}

func TestStoreHistoryReportsMissingFilesAndDeletesMedia(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	missingPath := filepath.Join(dir, "gone.mp4")
	if err := s.AppendHistory(HistoryEntry{
		ID:           "missing",
		Title:        "Missing",
		Quality:      "720p",
		Container:    "MP4",
		AbsolutePath: missingPath,
		CompletedAt:  "2025-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("AppendHistory missing: %v", err)
	}
	history := s.History()
	if len(history) != 1 || !history[0].FileMissing {
		t.Fatalf("missing history = %#v; want FileMissing=true", history)
	}

	existingPath := filepath.Join(dir, "keep.mp4")
	if err := os.WriteFile(existingPath, []byte("payload"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := s.AppendHistory(HistoryEntry{
		ID:           "present",
		Title:        "Present",
		Quality:      "1080p",
		Container:    "MP4",
		AbsolutePath: existingPath,
		CompletedAt:  "2025-01-02T00:00:00Z",
	}); err != nil {
		t.Fatalf("AppendHistory present: %v", err)
	}
	history = s.History()
	if len(history) != 2 || history[0].ID != "present" || history[0].FileMissing {
		t.Fatalf("present history = %#v; want present first and FileMissing=false", history)
	}

	deleted, err := s.DeleteHistoryFile("present")
	if err != nil || !deleted {
		t.Fatalf("DeleteHistoryFile: deleted=%v err=%v", deleted, err)
	}
	if _, err := os.Stat(existingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected media file removed, got %v", err)
	}
	if got := len(s.History()); got != 1 || s.History()[0].ID != "missing" {
		t.Fatalf("history after delete = %#v; want only missing entry", s.History())
	}
}

func TestStoreHistoryNewestFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.AppendHistory(HistoryEntry{ID: "old", Title: "Old", CompletedAt: "2024-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	if err := s.AppendHistory(HistoryEntry{ID: "new", Title: "New", CompletedAt: "2025-06-01T00:00:00Z"}); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	got := s.History()
	if len(got) != 2 {
		t.Fatalf("history length = %d, want 2", len(got))
	}
	if got[0].ID != "new" {
		t.Fatalf("history[0] = %q, want new", got[0].ID)
	}
}

func TestStoreRemoveAndClear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = s.AppendHistory(HistoryEntry{ID: "a"})
	_ = s.AppendHistory(HistoryEntry{ID: "b"})
	if ok, err := s.RemoveHistory("a"); err != nil || !ok {
		t.Fatalf("RemoveHistory: ok=%v err=%v", ok, err)
	}
	if got := len(s.History()); got != 1 {
		t.Fatalf("after remove length = %d, want 1", got)
	}
	if err := s.ClearHistory(); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}
	if got := len(s.History()); got != 0 {
		t.Fatalf("after clear length = %d, want 0", got)
	}
}

func TestEphemeralStoreSupportsMutationsWithoutDisk(t *testing.T) {
	store := NewEphemeral()
	settings := store.Settings()
	settings.DownloadFolder = filepath.Join(t.TempDir(), "downloads")
	if err := store.SetSettings(settings); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	if err := store.AppendHistory(HistoryEntry{ID: "ephemeral", Title: "Temporary"}); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	if got := store.Settings().DownloadFolder; got != settings.DownloadFolder {
		t.Fatalf("DownloadFolder = %q, want %q", got, settings.DownloadFolder)
	}
	if got := len(store.History()); got != 1 {
		t.Fatalf("history length = %d, want 1", got)
	}
}
