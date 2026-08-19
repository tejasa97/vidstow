package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestUploaderRemovesAcceptedAndPermanentRejections(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	outbox, err := openOutbox(filepath.Join(t.TempDir(), "outbox-v1.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	accepted := testProblemEvent(t, now)
	rejected := testProblemEvent(t, now)
	pending := testProblemEvent(t, now)
	for _, event := range []Event{accepted, rejected, pending} {
		if err := outbox.Enqueue(event); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request method=%s content-type=%q", request.Method, request.Header.Get("Content-Type"))
		}
		var body EventsRequest
		decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, MaxUploadBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil || body.SchemaVersion != SchemaVersion || len(body.Events) != 3 {
			t.Fatalf("request body=%#v err=%v", body, err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(EventsResponse{
			AcceptedEventIDs: []string{accepted.EventID},
			Rejected:         []EventRejection{{EventID: rejected.EventID, Code: "invalid_event"}},
		})
	}))
	defer server.Close()
	uploader, err := NewUploader(outbox, server.URL+"/v1/events", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	worked, err := uploader.UploadOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	batch, err := outbox.Batch()
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].EventID != pending.EventID {
		t.Fatalf("remaining batch=%#v", batch)
	}
}

func TestUploaderRetries429AndInvalidAcknowledgements(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   any
	}{
		{name: "rate limited", status: http.StatusTooManyRequests},
		{name: "server failure", status: http.StatusServiceUnavailable},
		{name: "unknown acknowledgement", status: http.StatusOK, body: EventsResponse{AcceptedEventIDs: []string{"4c2ad36f-7db8-4c58-8cb9-a4e401b52b99"}}},
		{name: "empty acknowledgement", status: http.StatusOK, body: EventsResponse{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
			outbox, err := openOutbox(filepath.Join(t.TempDir(), "outbox-v1.json"), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			event := testProblemEvent(t, now)
			if err := outbox.Enqueue(event); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				if test.body != nil {
					_ = json.NewEncoder(writer).Encode(test.body)
				}
			}))
			defer server.Close()
			uploader, err := NewUploader(outbox, server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if worked, err := uploader.UploadOnce(context.Background()); worked || !errors.Is(err, ErrUploadRetryable) {
				t.Fatalf("worked=%v err=%v", worked, err)
			}
			batch, err := outbox.Batch()
			if err != nil || len(batch) != 1 || batch[0].EventID != event.EventID {
				t.Fatalf("event was removed: batch=%#v err=%v", batch, err)
			}
		})
	}
}

func TestUploaderPermanentlyDropsNon429ClientErrors(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	outbox, err := openOutbox(filepath.Join(t.TempDir(), "outbox-v1.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.Enqueue(testProblemEvent(t, now)); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	uploader, err := NewUploader(outbox, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	worked, err := uploader.UploadOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	batch, err := outbox.Batch()
	if err != nil || len(batch) != 0 {
		t.Fatalf("batch=%#v err=%v", batch, err)
	}
}

func TestUploaderRequiresPlainHTTPS(t *testing.T) {
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"", "http://example.com/v1/events", "https://user@example.com/v1/events", "https://example.com/v1/events?token=secret"} {
		if _, err := NewUploader(outbox, endpoint, http.DefaultClient); err == nil {
			t.Fatalf("accepted endpoint %q", endpoint)
		}
	}
}
