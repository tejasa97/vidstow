package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/tejasa97/vidstow/internal/jobmodel"
)

// Durable reports that State v2 is an on-disk authority. It implements the
// manager capability used to describe persistence health without exposing the
// store's private path or lock implementation.
func (s *V2Store) Durable() bool { return s != nil }

// DataPath returns the owner-only State v2 path for native diagnostics and the
// data-folder action. Callers must not persist or expose this value as queue
// state; it is only a local app integration seam.
func (s *V2Store) DataPath() string {
	if s == nil {
		return ""
	}
	return s.path
}

// DataDirectory returns the directory containing State v2 and its recovery
// evidence. It is intentionally derived from the canonical store path.
func (s *V2Store) DataDirectory() string {
	if s == nil {
		return ""
	}
	return filepath.Dir(s.path)
}

// Settings projects the durable State v2 settings into the desktop shape.
func (s *V2Store) Settings() Settings {
	if s == nil || !s.Status().Healthy() {
		return Settings{}
	}
	state := s.Snapshot()
	return Settings{
		DownloadFolder:        state.Settings.DownloadFolder,
		FFmpegPath:            state.Settings.FFmpegPath,
		WindowWidth:           state.Settings.WindowWidth,
		WindowHeight:          state.Settings.WindowHeight,
		DownloadConcurrency:   state.Settings.DownloadConcurrency,
		PerVideoSubfolder:     state.Settings.PerVideoSubfolder,
		ConfirmBeforeDownload: state.Settings.ConfirmBeforeDownload,
		OutputOptions:         state.Settings.OutputOptions.Clone(),
		AutomaticDiagnostics:  state.Settings.AutomaticDiagnostics,
	}
}

// SetSettings writes only fields represented by the State v2 schema.
func (s *V2Store) SetSettings(next Settings) error {
	if s == nil {
		return errors.New("store: nil v2 store")
	}
	if err := next.OutputOptions.Validate(); err != nil {
		return fmt.Errorf("store: invalid output options: %w", err)
	}
	settings := jobmodel.Settings{
		DownloadFolder:        next.DownloadFolder,
		FFmpegPath:            next.FFmpegPath,
		WindowWidth:           next.WindowWidth,
		WindowHeight:          next.WindowHeight,
		DownloadConcurrency:   next.DownloadConcurrency,
		PerVideoSubfolder:     next.PerVideoSubfolder,
		ConfirmBeforeDownload: next.ConfirmBeforeDownload,
		OutputOptions:         next.OutputOptions.Clone(),
		AutomaticDiagnostics:  next.AutomaticDiagnostics,
	}
	if settings.DownloadFolder == "" {
		settings.DownloadFolder = defaultStateV2().Settings.DownloadFolder
	}
	if settings.WindowWidth <= 0 {
		settings.WindowWidth = defaultStateV2().Settings.WindowWidth
	}
	if settings.WindowHeight <= 0 {
		settings.WindowHeight = defaultStateV2().Settings.WindowHeight
	}
	if settings.DownloadConcurrency <= 0 {
		settings.DownloadConcurrency = defaultStateV2().Settings.DownloadConcurrency
	}
	return s.Transaction(nil, func(state *jobmodel.State) error {
		state.Settings = settings
		return nil
	})
}

// History returns the State v2 completion history in newest-first order. File
// presence is derived without following symlinks and is never written back to
// State v2.
func (s *V2Store) History() []HistoryEntry {
	if s == nil || !s.Status().Healthy() {
		return nil
	}
	state := s.Snapshot()
	result := make([]HistoryEntry, 0, len(state.History))
	for _, entry := range state.History {
		result = append(result, historyEntryFromV2(entry))
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].CompletedAt > result[j].CompletedAt })
	for i := range result {
		result[i].FileMissing = !regularFileNoFollow(result[i].AbsolutePath)
	}
	return result
}

// AppendHistory is retained for compatibility with non-manager callers, but
// the V2 Manager records completion history in its terminal State transaction.
func (s *V2Store) AppendHistory(entry HistoryEntry) error {
	if s == nil {
		return errors.New("store: nil v2 store")
	}
	if entry.CompletedAt == "" {
		entry.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return s.Transaction(nil, func(state *jobmodel.State) error {
		candidate := historyEntryToV2(entry)
		for _, existing := range state.History {
			if existing.ID == candidate.ID {
				return nil
			}
		}
		state.History = append([]jobmodel.HistoryEntry{candidate}, state.History...)
		if len(state.History) > MaxHistoryItems {
			state.History = state.History[:MaxHistoryItems]
		}
		return nil
	})
}

// RemoveHistory drops a history entry by ID. It is a single State v2
// transaction, so it cannot race a manager completion into a second writer.
func (s *V2Store) RemoveHistory(id string) (bool, error) {
	if s == nil {
		return false, errors.New("store: nil v2 store")
	}
	found := false
	err := s.Transaction(nil, func(state *jobmodel.State) error {
		for i, entry := range state.History {
			if entry.ID != id {
				continue
			}
			state.History = append(state.History[:i], state.History[i+1:]...)
			found = true
			break
		}
		return nil
	})
	return found, err
}

// DeleteHistoryFile removes one owner-selected regular media file without
// following a symlink, then removes its history row transactionally.
func (s *V2Store) DeleteHistoryFile(id string) (bool, error) {
	if s == nil {
		return false, errors.New("store: nil v2 store")
	}
	var entry *HistoryEntry
	for _, candidate := range s.History() {
		if candidate.ID == id {
			copy := candidate
			entry = &copy
			break
		}
	}
	if entry == nil {
		return false, nil
	}
	if entry.AbsolutePath != "" {
		info, err := os.Lstat(entry.AbsolutePath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		if err == nil {
			if !info.Mode().IsRegular() {
				return false, errors.New("store: history target is not a regular media file")
			}
			if err := os.Remove(entry.AbsolutePath); err != nil {
				return false, err
			}
		}
	}
	removed, err := s.RemoveHistory(id)
	return removed, err
}

// ClearHistory removes all terminal history in one State v2 transaction.
func (s *V2Store) ClearHistory() error {
	if s == nil {
		return errors.New("store: nil v2 store")
	}
	return s.Transaction(nil, func(state *jobmodel.State) error {
		state.History = nil
		return nil
	})
}

func historyEntryFromV2(entry jobmodel.HistoryEntry) HistoryEntry {
	return HistoryEntry{
		ID:            entry.ID,
		VideoID:       entry.VideoID,
		Title:         entry.Title,
		Channel:       entry.Channel,
		Quality:       entry.Quality,
		Container:     entry.Container,
		VideoCodec:    entry.VideoCodec,
		AudioCodec:    entry.AudioCodec,
		Filename:      entry.Filename,
		AbsolutePath:  entry.AbsolutePath,
		SizeBytes:     entry.SizeBytes,
		CompletedAt:   entry.CompletedAt,
		DurationLabel: entry.DurationLabel,
	}
}

func historyEntryToV2(entry HistoryEntry) jobmodel.HistoryEntry {
	return jobmodel.HistoryEntry{
		ID:            entry.ID,
		VideoID:       entry.VideoID,
		Title:         entry.Title,
		Channel:       entry.Channel,
		Quality:       entry.Quality,
		Container:     entry.Container,
		VideoCodec:    entry.VideoCodec,
		AudioCodec:    entry.AudioCodec,
		Filename:      entry.Filename,
		AbsolutePath:  entry.AbsolutePath,
		SizeBytes:     entry.SizeBytes,
		CompletedAt:   entry.CompletedAt,
		DurationLabel: entry.DurationLabel,
	}
}

func regularFileNoFollow(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}
