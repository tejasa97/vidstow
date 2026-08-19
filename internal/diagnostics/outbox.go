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
	MaxOutboxEvents        = 100
	MaxOutboxDocumentBytes = 512 << 10
	MaxUploadEvents        = 20
	MaxUploadBytes         = 64 << 10
)

var ErrOutboxEventTooLarge = errors.New("diagnostics: event exceeds outbox limit")

type outboxDocument struct {
	SchemaVersion int     `json:"schema_version"`
	Events        []Event `json:"events"`
}

// Outbox owns the separate, bounded queue used only for consented automatic
// transmission. Local diagnostic history remains independent.
type Outbox struct {
	path string
	now  func() time.Time
	mu   sync.Mutex
}

func OpenOutbox(path string) (*Outbox, error) { return openOutbox(path, time.Now) }

func openOutbox(path string, now func() time.Time) (*Outbox, error) {
	if path == "" || now == nil {
		return nil, errors.New("diagnostics: invalid outbox configuration")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("diagnostics: absolute outbox path: %w", err)
	}
	if filepath.Base(absolute) == "." || filepath.Base(absolute) == string(filepath.Separator) {
		return nil, errors.New("diagnostics: invalid outbox path")
	}
	if err := ensurePrivateDirectory(filepath.Dir(absolute)); err != nil {
		return nil, fmt.Errorf("diagnostics: private outbox directory: %w", err)
	}
	o := &Outbox{path: absolute, now: now}
	if err := o.withLocked(func() error {
		events, err := o.loadLocked()
		if err != nil {
			return err
		}
		pruned := pruneByAge(events, o.now().UTC())
		if len(pruned) == len(events) {
			return nil
		}
		encoded, kept, err := encodeOutboxBounded(pruned)
		if err != nil {
			return err
		}
		return o.replaceLocked(encoded, len(kept) == 0)
	}); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *Outbox) Enqueue(event Event) error {
	if o == nil {
		return errors.New("diagnostics: nil outbox")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	event.OccurredAt = event.OccurredAt.UTC()
	now := o.now().UTC()
	if event.OccurredAt.After(now) {
		return errors.New("diagnostics: occurrence time is in the future")
	}
	return o.withLocked(func() error {
		events, err := o.loadLocked()
		if err != nil {
			return err
		}
		for _, existing := range events {
			if existing.EventID != event.EventID {
				continue
			}
			if equalEvent(existing, event) {
				return nil
			}
			return errors.New("diagnostics: conflicting event identifier")
		}
		events = append(events, event)
		events = pruneByAge(events, now)
		sortEvents(events)
		if len(events) > MaxOutboxEvents {
			events = events[len(events)-MaxOutboxEvents:]
		}
		encoded, kept, err := encodeOutboxBounded(events)
		if err != nil {
			return err
		}
		return o.replaceLocked(encoded, len(kept) == 0)
	})
}

// Batch returns the oldest events that fit both upload bounds.
func (o *Outbox) Batch() ([]Event, error) {
	if o == nil {
		return nil, errors.New("diagnostics: nil outbox")
	}
	var batch []Event
	err := o.withLocked(func() error {
		events, err := o.loadAndPruneLocked()
		if err != nil {
			return err
		}
		for _, event := range events {
			candidate := append(append([]Event(nil), batch...), event)
			encoded, err := json.Marshal(EventsRequest{SchemaVersion: SchemaVersion, Events: candidate})
			if err != nil {
				return fmt.Errorf("diagnostics: encode upload batch: %w", err)
			}
			if len(candidate) > MaxUploadEvents || len(encoded) > MaxUploadBytes {
				break
			}
			batch = candidate
		}
		return nil
	})
	return batch, err
}

// Remove deletes only acknowledged or permanently rejected event IDs.
func (o *Outbox) Remove(ids []string) error {
	if o == nil {
		return errors.New("diagnostics: nil outbox")
	}
	remove := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !validUUID4(id) {
			return errors.New("diagnostics: invalid outbox removal identifier")
		}
		remove[id] = true
	}
	if len(remove) == 0 {
		return nil
	}
	return o.withLocked(func() error {
		events, err := o.loadLocked()
		if err != nil {
			return err
		}
		kept := events[:0]
		for _, event := range events {
			if !remove[event.EventID] {
				kept = append(kept, event)
			}
		}
		encoded, kept, err := encodeOutboxBounded(kept)
		if err != nil {
			return err
		}
		return o.replaceLocked(encoded, len(kept) == 0)
	})
}

func (o *Outbox) Clear() error {
	if o == nil {
		return errors.New("diagnostics: nil outbox")
	}
	return o.withLocked(func() error { return o.replaceLocked(nil, true) })
}

func (o *Outbox) withLocked(action func() error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	lock, err := acquireFileLock(o.path + ".lock")
	if err != nil {
		return fmt.Errorf("diagnostics: acquire outbox lock: %w", err)
	}
	actionErr := action()
	closeErr := lock.Close()
	if actionErr != nil {
		return actionErr
	}
	if closeErr != nil {
		return fmt.Errorf("diagnostics: release outbox lock: %w", closeErr)
	}
	return nil
}

func (o *Outbox) loadAndPruneLocked() ([]Event, error) {
	events, err := o.loadLocked()
	if err != nil {
		return nil, err
	}
	pruned := pruneByAge(events, o.now().UTC())
	if len(pruned) == len(events) {
		return pruned, nil
	}
	encoded, kept, err := encodeOutboxBounded(pruned)
	if err != nil {
		return nil, err
	}
	if err := o.replaceLocked(encoded, len(kept) == 0); err != nil {
		return nil, err
	}
	return kept, nil
}

func (o *Outbox) loadLocked() ([]Event, error) {
	f, err := openPrivateRead(o.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("diagnostics: open outbox: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("diagnostics: inspect outbox: %w", err)
	}
	if info.Size() > MaxOutboxDocumentBytes {
		_ = f.Close()
		if err := removeHistoryFile(o.path); err != nil {
			return nil, fmt.Errorf("diagnostics: discard oversized outbox: %w", err)
		}
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxOutboxDocumentBytes+1))
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("diagnostics: read outbox: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("diagnostics: close outbox: %w", err)
	}
	var document outboxDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || ensureJSONEOF(decoder) != nil || document.SchemaVersion != SchemaVersion || len(document.Events) > MaxOutboxEvents {
		if removeErr := removeHistoryFile(o.path); removeErr != nil {
			return nil, fmt.Errorf("diagnostics: discard corrupt outbox: %w", removeErr)
		}
		return nil, nil
	}
	seen := make(map[string]bool, len(document.Events))
	for _, event := range document.Events {
		if err := event.Validate(); err != nil || seen[event.EventID] {
			if removeErr := removeHistoryFile(o.path); removeErr != nil {
				return nil, fmt.Errorf("diagnostics: discard invalid outbox: %w", removeErr)
			}
			return nil, nil
		}
		seen[event.EventID] = true
	}
	sortEvents(document.Events)
	return document.Events, nil
}

func (o *Outbox) replaceLocked(encoded []byte, remove bool) error {
	if remove {
		if err := removeHistoryFile(o.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("diagnostics: clear outbox: %w", err)
		}
		return nil
	}
	temp, err := os.CreateTemp(filepath.Dir(o.path), ".diagnostics-outbox-v1-")
	if err != nil {
		return fmt.Errorf("diagnostics: create outbox temp: %w", err)
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return fmt.Errorf("diagnostics: protect outbox temp: %w", err)
	}
	if _, err := temp.Write(encoded); err != nil {
		return fmt.Errorf("diagnostics: write outbox temp: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("diagnostics: sync outbox temp: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("diagnostics: close outbox temp: %w", err)
	}
	if err := replaceFile(tempPath, o.path); err != nil {
		return fmt.Errorf("diagnostics: replace outbox: %w", err)
	}
	committed = true
	return nil
}

func encodeOutboxBounded(events []Event) ([]byte, []Event, error) {
	for {
		encoded, err := json.Marshal(outboxDocument{SchemaVersion: SchemaVersion, Events: events})
		if err != nil {
			return nil, nil, fmt.Errorf("diagnostics: encode outbox: %w", err)
		}
		if len(encoded) <= MaxOutboxDocumentBytes {
			return encoded, events, nil
		}
		if len(events) <= 1 {
			return nil, nil, ErrOutboxEventTooLarge
		}
		events = events[1:]
	}
}

func sortEvents(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].OccurredAt.Equal(events[j].OccurredAt) {
			return events[i].EventID < events[j].EventID
		}
		return events[i].OccurredAt.Before(events[j].OccurredAt)
	})
}
