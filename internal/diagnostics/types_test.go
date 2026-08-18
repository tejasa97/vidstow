package diagnostics

import (
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

func TestEventValidationRejectsPrivacyUnsafeOrUnboundedValues(t *testing.T) {
	base := testProblemEvent(t, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	tests := []struct {
		name   string
		mutate func(*Event)
	}{
		{"non-v4 event ID", func(event *Event) { event.EventID = "not-an-id" }},
		{"URL in version", func(event *Event) { event.AppVersion = "https://example.test/secret" }},
		{"unknown category", func(event *Event) { event.Problem.Category = "raw error: cookie=secret" }},
		{"raw resource URL", func(event *Event) { event.Resource.ResourceID = "https://youtube.com/watch?v=secret" }},
		{"health carries resource", func(event *Event) {
			event.Type = TypeHealthSummary
			event.OperationID = ""
			event.Health = &Health{PeriodStartedAt: event.OccurredAt, PeriodEndedAt: event.OccurredAt}
			event.Problem = nil
		}},
		{"panic fingerprint on transfer", func(event *Event) { event.Problem.PanicFingerprint = strings.Repeat("a", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := base
			problem := *base.Problem
			resource := *base.Resource
			event.Problem = &problem
			event.Resource = &resource
			test.mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatal("Validate accepted unsafe event")
			}
		})
	}
}

func TestResourceValidationRequiresCanonicalProviderIdentifiers(t *testing.T) {
	valid := []Resource{
		{Provider: "youtube", ResourceType: "video", ResourceID: "abc123_DEF9"},
		{Provider: "youtube", ResourceType: "playlist", ResourceID: "PL1234567890"},
		{Provider: "youtube", ResourceType: "channel", ResourceID: "UCabcdefghijklmnopqrstuv"},
	}
	for _, resource := range valid {
		if err := resource.validate(); err != nil {
			t.Fatalf("validate canonical resource %#v: %v", resource, err)
		}
	}
	invalid := []Resource{
		{Provider: "youtube", ResourceType: "video", ResourceID: "abc123_DEF-9"},
		{Provider: "youtube", ResourceType: "playlist", ResourceID: "x"},
		{Provider: "youtube", ResourceType: "channel", ResourceID: "abcdefghijklmnopqrstuvwx"},
	}
	for _, resource := range invalid {
		if err := resource.validate(); err == nil {
			t.Fatalf("accepted non-canonical resource %#v", resource)
		}
	}
}

func TestHealthValidationRequiresNonOverlappingShape(t *testing.T) {
	event := testProblemEvent(t, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	event.Type = TypeHealthSummary
	event.OperationID = ""
	event.Resource = nil
	event.Problem = nil
	event.Health = &Health{
		PeriodStartedAt: event.OccurredAt.Add(time.Minute),
		PeriodEndedAt:   event.OccurredAt,
	}
	if err := event.Validate(); err == nil {
		t.Fatal("Validate accepted reversed health interval")
	}
	event.Health.PeriodStartedAt = event.OccurredAt
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate valid health event: %v", err)
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
	operationID, err := NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	return Event{
		SchemaVersion: SchemaVersion,
		EventID:       eventID,
		SessionID:     sessionID,
		OperationID:   operationID,
		OccurredAt:    occurredAt.UTC(),
		AppVersion:    "0.1.0-beta.3",
		EngineVersion: "0.2.3",
		Platform:      Platform{OS: "macos", OSMajor: "15", Architecture: "arm64"},
		Type:          TypeProblemObserved,
		Problem:       &Problem{Stage: "media_transfer", Category: "http_403", Outcome: "terminal", RetryBucket: "one"},
		Resource:      &Resource{Provider: "youtube", ResourceType: "video", ResourceID: "abc123_DEF9"},
	}
}
