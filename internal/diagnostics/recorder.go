package diagnostics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	MaxEvents        = 200
	MaxDocumentBytes = 1 << 20
	MaxAge           = 7 * 24 * time.Hour
)

var ErrEventTooLarge = errors.New("diagnostics: event exceeds local history limit")

type historyDocument struct {
	SchemaVersion int     `json:"schema_version"`
	Events        []Event `json:"events"`
}

// Recorder owns one bounded local diagnostic history. Its methods serialize
// goroutines and take a short cross-process lock before replacing the file.
// Recorder has no uploader and never accepts raw messages or errors.
type Recorder struct {
	path string
	now  func() time.Time
	mu   sync.Mutex
}

func Open(path string) (*Recorder, error) {
	return open(path, time.Now)
}

func open(path string, now func() time.Time) (*Recorder, error) {
	if path == "" || now == nil {
		return nil, errors.New("diagnostics: invalid recorder configuration")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("diagnostics: absolute history path: %w", err)
	}
	if filepath.Base(absolute) == "." || filepath.Base(absolute) == string(filepath.Separator) {
		return nil, errors.New("diagnostics: invalid history path")
	}
	if err := ensurePrivateDirectory(filepath.Dir(absolute)); err != nil {
		return nil, fmt.Errorf("diagnostics: private history directory: %w", err)
	}
	r := &Recorder{path: absolute, now: now}
	// Validate or discard a corrupt/oversized history now. Such evidence is not
	// application authority and must never force startup recovery.
	if err := r.withLocked(func() error {
		events, err := r.loadLocked()
		if err != nil {
			return err
		}
		pruned := pruneByAge(events, r.now().UTC())
		if len(pruned) == len(events) {
			return nil
		}
		encoded, kept, err := encodeBounded(pruned)
		if err != nil {
			return err
		}
		return r.replaceLocked(encoded, len(kept) == 0)
	}); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Recorder) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

func (r *Recorder) Record(event Event) error {
	if r == nil {
		return errors.New("diagnostics: nil recorder")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	event.OccurredAt = event.OccurredAt.UTC()
	now := r.now().UTC()
	if event.OccurredAt.After(now) {
		return errors.New("diagnostics: occurrence time is in the future")
	}
	return r.withLocked(func() error {
		events, err := r.loadLocked()
		if err != nil {
			return err
		}
		for _, existing := range events {
			if existing.EventID == event.EventID {
				if equalEvent(existing, event) {
					return nil
				}
				return errors.New("diagnostics: conflicting event identifier")
			}
		}
		events = append(events, event)
		events = pruneByAge(events, now)
		sort.SliceStable(events, func(i, j int) bool {
			if events[i].OccurredAt.Equal(events[j].OccurredAt) {
				return events[i].EventID < events[j].EventID
			}
			return events[i].OccurredAt.Before(events[j].OccurredAt)
		})
		if len(events) > MaxEvents {
			events = events[len(events)-MaxEvents:]
		}
		encoded, events, err := encodeBounded(events)
		if err != nil {
			return err
		}
		return r.replaceLocked(encoded, len(events) == 0)
	})
}

func (r *Recorder) Recent() ([]Event, error) {
	if r == nil {
		return nil, errors.New("diagnostics: nil recorder")
	}
	var result []Event
	err := r.withLocked(func() error {
		events, err := r.loadLocked()
		if err != nil {
			return err
		}
		pruned := pruneByAge(events, r.now().UTC())
		if len(pruned) != len(events) {
			encoded, kept, err := encodeBounded(pruned)
			if err != nil {
				return err
			}
			if err := r.replaceLocked(encoded, len(kept) == 0); err != nil {
				return err
			}
			pruned = kept
		}
		result = append([]Event(nil), pruned...)
		return nil
	})
	return result, err
}

func (r *Recorder) Clear() error {
	if r == nil {
		return errors.New("diagnostics: nil recorder")
	}
	return r.withLocked(func() error { return r.replaceLocked(nil, true) })
}

func (r *Recorder) withLocked(action func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock, err := acquireFileLock(r.path + ".lock")
	if err != nil {
		return fmt.Errorf("diagnostics: acquire history lock: %w", err)
	}
	actionErr := action()
	closeErr := lock.Close()
	if actionErr != nil {
		return actionErr
	}
	if closeErr != nil {
		return fmt.Errorf("diagnostics: release history lock: %w", closeErr)
	}
	return nil
}

func (r *Recorder) loadLocked() ([]Event, error) {
	f, err := openPrivateRead(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("diagnostics: open history: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("diagnostics: inspect history: %w", err)
	}
	if info.Size() > MaxDocumentBytes {
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("diagnostics: close oversized history: %w", err)
		}
		if err := removeHistoryFile(r.path); err != nil {
			return nil, fmt.Errorf("diagnostics: discard oversized history: %w", err)
		}
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxDocumentBytes+1))
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("diagnostics: read history: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("diagnostics: close history: %w", err)
	}
	var document historyDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || document.SchemaVersion != SchemaVersion || len(document.Events) > MaxEvents {
		if removeErr := removeHistoryFile(r.path); removeErr != nil {
			return nil, fmt.Errorf("diagnostics: discard corrupt history: %w", removeErr)
		}
		return nil, nil
	}
	if err := ensureJSONEOF(decoder); err != nil {
		if removeErr := removeHistoryFile(r.path); removeErr != nil {
			return nil, fmt.Errorf("diagnostics: discard corrupt history: %w", removeErr)
		}
		return nil, nil
	}
	seen := make(map[string]bool, len(document.Events))
	for _, event := range document.Events {
		if err := event.Validate(); err != nil || seen[event.EventID] {
			if removeErr := removeHistoryFile(r.path); removeErr != nil {
				return nil, fmt.Errorf("diagnostics: discard invalid history: %w", removeErr)
			}
			return nil, nil
		}
		seen[event.EventID] = true
	}
	sort.SliceStable(document.Events, func(i, j int) bool {
		if document.Events[i].OccurredAt.Equal(document.Events[j].OccurredAt) {
			return document.Events[i].EventID < document.Events[j].EventID
		}
		return document.Events[i].OccurredAt.Before(document.Events[j].OccurredAt)
	})
	return document.Events, nil
}

func (r *Recorder) replaceLocked(encoded []byte, remove bool) error {
	if remove {
		if err := removeHistoryFile(r.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("diagnostics: clear history: %w", err)
		}
		return nil
	}
	temp, err := os.CreateTemp(filepath.Dir(r.path), ".diagnostics-v1-")
	if err != nil {
		return fmt.Errorf("diagnostics: create history temp: %w", err)
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return fmt.Errorf("diagnostics: protect history temp: %w", err)
	}
	if _, err := temp.Write(encoded); err != nil {
		return fmt.Errorf("diagnostics: write history temp: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("diagnostics: sync history temp: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("diagnostics: close history temp: %w", err)
	}
	if err := replaceFile(tempPath, r.path); err != nil {
		return fmt.Errorf("diagnostics: replace history: %w", err)
	}
	committed = true
	return nil
}

func encodeBounded(events []Event) ([]byte, []Event, error) {
	for {
		encoded, err := json.Marshal(historyDocument{SchemaVersion: SchemaVersion, Events: events})
		if err != nil {
			return nil, nil, fmt.Errorf("diagnostics: encode history: %w", err)
		}
		if len(encoded) <= MaxDocumentBytes {
			return encoded, events, nil
		}
		if len(events) <= 1 {
			return nil, nil, ErrEventTooLarge
		}
		events = events[1:]
	}
}

func pruneByAge(events []Event, now time.Time) []Event {
	cutoff := now.Add(-MaxAge)
	kept := make([]Event, 0, len(events))
	for _, event := range events {
		if !event.OccurredAt.Before(cutoff) && !event.OccurredAt.After(now) {
			kept = append(kept, event)
		}
	}
	return kept
}

func equalEvent(left, right Event) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else {
		return errors.New("diagnostics: trailing history data")
	}
}
