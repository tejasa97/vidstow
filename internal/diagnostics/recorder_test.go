package diagnostics

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRecorderPersistsOrdersAndClearsHistory(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "private", "diagnostics-v1.json")
	recorder, err := open(path, func() time.Time { return now.Add(2 * time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	later := testProblemEvent(t, now.Add(time.Minute))
	earlier := testProblemEvent(t, now)
	if err := recorder.Record(later); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(earlier); err != nil {
		t.Fatal(err)
	}

	reopened, err := open(path, func() time.Time { return now.Add(2 * time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	events, err := reopened.Recent()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].EventID != earlier.EventID || events[1].EventID != later.EventID {
		t.Fatalf("unexpected persisted order: %#v", events)
	}
	if err := reopened.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("history remains after Clear: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("lock file should remain reusable: %v", err)
	}
}

func TestRecorderIsIdempotentAndRejectsConflictingEventID(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	recorder, err := open(filepath.Join(t.TempDir(), "diagnostics-v1.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	event := testProblemEvent(t, now)
	if err := recorder.Record(event); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(event); err != nil {
		t.Fatalf("idempotent Record failed: %v", err)
	}
	conflict := event
	problem := *event.Problem
	problem.Outcome = "recovered"
	conflict.Problem = &problem
	if err := recorder.Record(conflict); err == nil {
		t.Fatal("Record accepted conflicting payload for one event ID")
	}
}

func TestRecorderAppliesAgeAndCountBounds(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	recorder, err := open(filepath.Join(t.TempDir(), "diagnostics-v1.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	old := testProblemEvent(t, now.Add(-MaxAge-time.Second))
	if err := recorder.Record(old); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < MaxEvents+5; index++ {
		event := testProblemEvent(t, now.Add(-time.Duration(MaxEvents+5-index)*time.Millisecond))
		if err := recorder.Record(event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := recorder.Recent()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != MaxEvents {
		t.Fatalf("event count = %d, want %d", len(events), MaxEvents)
	}
	for _, event := range events {
		if event.EventID == old.EventID {
			t.Fatal("expired event survived")
		}
	}
	info, err := os.Stat(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > MaxDocumentBytes {
		t.Fatalf("history size = %d, limit = %d", info.Size(), MaxDocumentBytes)
	}
}

func TestRecorderRejectsAndDiscardsFutureDatedEvents(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "diagnostics-v1.json")
	recorder, err := open(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(testProblemEvent(t, now.Add(time.Nanosecond))); err == nil {
		t.Fatal("Record accepted a future-dated event")
	}

	future := testProblemEvent(t, now.Add(time.Hour))
	encoded, _, err := encodeBounded([]Event{future})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := open(path, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("future-dated history remains after Open: %v", err)
	}
}

func TestRecorderDiscardsCorruptUnknownAndOversizedDocuments(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"malformed", []byte(`{"schema_version":1`)},
		{"unknown field", []byte(`{"schema_version":1,"events":[],"raw_log":"cookie=secret"}`)},
		{"oversized", make([]byte, MaxDocumentBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "diagnostics-v1.json")
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			recorder, err := open(path, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			events, err := recorder.Recent()
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 0 {
				t.Fatalf("corrupt history returned %d events", len(events))
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("corrupt file not discarded: %v", err)
			}
		})
	}
}

func TestRecorderSerializesConcurrentWriters(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "diagnostics-v1.json")
	first, err := open(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	second, err := open(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	const count = 40
	testEvents := make([]Event, count)
	for index := range testEvents {
		testEvents[index] = testProblemEvent(t, now.Add(-time.Duration(count-index)*time.Millisecond))
	}
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for index := 0; index < count; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			recorder := first
			if index%2 == 0 {
				recorder = second
			}
			errs <- recorder.Record(testEvents[index])
		}(index)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := first.Recent()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != count {
		t.Fatalf("event count = %d, want %d", len(events), count)
	}
}

func TestHistoryEncodingIsClosed(t *testing.T) {
	event := testProblemEvent(t, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	encoded, _, err := encodeBounded([]Event{event})
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	if len(value) != 2 {
		t.Fatalf("history envelope has unexpected fields: %#v", value)
	}
}
