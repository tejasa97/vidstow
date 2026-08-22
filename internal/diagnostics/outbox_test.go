package diagnostics

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOutboxPersistsBoundsBatchesAndClears(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "private", "outbox-v1.json")
	outbox, err := openOutbox(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < MaxOutboxEvents+5; index++ {
		event := testProblemEvent(t, now.Add(-time.Duration(MaxOutboxEvents+5-index)*time.Millisecond))
		if err := outbox.Enqueue(event); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := outbox.Batch()
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != MaxUploadEvents {
		t.Fatalf("batch length = %d, want %d", len(batch), MaxUploadEvents)
	}
	if err := outbox.Remove([]string{batch[0].EventID, batch[1].EventID}); err != nil {
		t.Fatal(err)
	}
	reopened, err := openOutbox(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	next, err := reopened.Batch()
	if err != nil {
		t.Fatal(err)
	}
	if len(next) == 0 || next[0].EventID == batch[0].EventID || next[0].EventID == batch[1].EventID {
		t.Fatalf("acknowledged events remain: %#v", next)
	}
	if err := reopened.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outbox remains after clear: %v", err)
	}
}

func TestOutboxIsIdempotentAndDropsExpiredEvents(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	outbox, err := openOutbox(filepath.Join(t.TempDir(), "outbox-v1.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	expired := testProblemEvent(t, now.Add(-MaxAge-time.Second))
	if err := outbox.Enqueue(expired); err != nil {
		t.Fatal(err)
	}
	event := testProblemEvent(t, now)
	if err := outbox.Enqueue(event); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Enqueue(event); err != nil {
		t.Fatalf("idempotent enqueue: %v", err)
	}
	conflict := event
	problem := *event.Problem
	problem.Outcome = "recovered"
	conflict.Problem = &problem
	if err := outbox.Enqueue(conflict); err == nil {
		t.Fatal("accepted conflicting event ID")
	}
	batch, err := outbox.Batch()
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].EventID != event.EventID {
		t.Fatalf("batch = %#v", batch)
	}
}

func TestOutboxDiscardsCorruptOrOversizedFiles(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "corrupt", data: []byte(`{"schema_version":1,"events":[`)},
		{name: "unknown", data: []byte(`{"schema_version":1,"events":[],"secret":"no"}`)},
		{name: "oversized", data: make([]byte, MaxOutboxDocumentBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "outbox-v1.json")
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			outbox, err := OpenOutbox(path)
			if err != nil {
				t.Fatal(err)
			}
			batch, err := outbox.Batch()
			if err != nil || len(batch) != 0 {
				t.Fatalf("batch=%#v err=%v", batch, err)
			}
		})
	}
}
