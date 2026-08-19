package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewUUIDProducesVersionFourIdentifiers(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		value, err := NewUUID()
		if err != nil {
			t.Fatal(err)
		}
		if !validUUID4(value) {
			t.Fatalf("invalid UUIDv4 %q", value)
		}
		if seen[value] {
			t.Fatalf("duplicate UUID %q", value)
		}
		seen[value] = true
	}
}

func TestCurrentPlatformMatchesEventContract(t *testing.T) {
	if err := CurrentPlatform().validate(); err != nil {
		t.Fatalf("CurrentPlatform = %#v: %v", CurrentPlatform(), err)
	}
}

func TestEventValidationRejectsUnsafeOrDeprecatedFields(t *testing.T) {
	base := testProblemEvent(t, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	tests := []struct {
		name   string
		mutate func(*Event)
	}{
		{"non-v4 event ID", func(event *Event) { event.EventID = "not-an-id" }},
		{"URL in version", func(event *Event) { event.AppVersion = "https://example.test/secret" }},
		{"unknown category", func(event *Event) { event.Problem.Category = "raw error: cookie=secret" }},
		{"recovered outcome", func(event *Event) { event.Problem.Outcome = "recovered" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := base
			problem := *base.Problem
			event.Problem = &problem
			test.mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatal("Validate accepted unsafe event")
			}
		})
	}
}

func TestEventContainsNoIdentifiers(t *testing.T) {
	event := testProblemEvent(t, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate valid event: %v", err)
	}
	encodedBytes, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(encodedBytes)
	for _, forbidden := range []string{"resource_id", "resource_type", "operation_id", "https://", "filename", "path"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("event leaked forbidden field/value %q: %s", forbidden, encoded)
		}
	}
}

func testProblemEvent(t *testing.T, occurredAt time.Time) Event {
	t.Helper()
	eventID, err := NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	return Event{
		SchemaVersion: SchemaVersion, EventID: eventID, SessionID: sessionID, OccurredAt: occurredAt.UTC(),
		AppVersion: "0.1.0-beta.3", EngineVersion: "0.2.3", Platform: Platform{OS: "macos", OSMajor: "15", Architecture: "arm64"},
		Type: TypeProblemObserved, Problem: &Problem{Stage: "media_transfer", Category: "http_403", Outcome: "terminal", RetryBucket: "one"},
	}
}
