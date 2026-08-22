package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
		{"degraded outcome", func(event *Event) { event.Problem.Outcome = "degraded" }},
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

func TestPublishedSchemaMatchesStageCategoryContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "diagnostics", "event-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Definitions struct {
			Problem struct {
				AllOf []struct {
					If struct {
						Properties struct {
							Stage struct {
								Const string `json:"const"`
							} `json:"stage"`
						} `json:"properties"`
					} `json:"if"`
					Then struct {
						Properties struct {
							Category struct {
								Enum []string `json:"enum"`
							} `json:"category"`
						} `json:"properties"`
					} `json:"then"`
				} `json:"allOf"`
			} `json:"problem"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	actual := make(map[string][]string, len(schema.Definitions.Problem.AllOf))
	for _, rule := range schema.Definitions.Problem.AllOf {
		stage := rule.If.Properties.Stage.Const
		if stage == "" || len(rule.Then.Properties.Category.Enum) == 0 {
			t.Fatalf("invalid schema stage/category rule: %#v", rule)
		}
		actual[stage] = append([]string(nil), rule.Then.Properties.Category.Enum...)
		sort.Strings(actual[stage])
	}
	expected := make(map[string][]string, len(stages))
	for category, validStages := range categoryStages {
		for stage := range validStages {
			expected[stage] = append(expected[stage], category)
		}
	}
	for stage := range expected {
		sort.Strings(expected[stage])
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("published stage/category rules = %#v, want %#v", actual, expected)
	}
}

func TestEveryAllowlistedStageCategoryPairValidates(t *testing.T) {
	count := 0
	for category, validStages := range categoryStages {
		for stage := range validStages {
			count++
			event := testProblemEvent(t, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
			event.Problem = &Problem{Stage: stage, Category: category, Outcome: "terminal", RetryBucket: "none"}
			if err := event.Validate(); err != nil {
				t.Fatalf("%s/%s: %v", stage, category, err)
			}
		}
	}
	if count == 0 {
		t.Fatal("no allowlisted stage/category pairs")
	}
}

func TestCrossWiredStageCategoryPairsAreRejected(t *testing.T) {
	rejected := 0
	for stage := range stages {
		for category := range categories {
			if validStageCategory(stage, category) {
				continue
			}
			rejected++
			event := testProblemEvent(t, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
			event.Problem = &Problem{Stage: stage, Category: category, Outcome: "terminal", RetryBucket: "none"}
			if err := event.Validate(); err == nil {
				t.Fatalf("accepted invalid pair %s/%s", stage, category)
			}
		}
	}
	if rejected == 0 {
		t.Fatal("expected invalid stage/category combinations")
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
