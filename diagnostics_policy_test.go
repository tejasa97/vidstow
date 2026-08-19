package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	localdiagnostics "github.com/tejasa97/vidstow/internal/diagnostics"
	"github.com/tejasa97/youtube_dlp/engine"
)

func TestDiagnosticPolicyCapsAndDoesNotSerializeOperationIDs(t *testing.T) {
	app := NewApp()
	app.openLocalDiagnostics(t.TempDir())
	if app.diagnostics == nil {
		t.Fatal("diagnostics recorder was not opened")
	}
	problem := localdiagnostics.Problem{Stage: "media_transfer", Category: "transfer_failed", Outcome: "terminal", RetryBucket: "none"}
	app.recordDiagnosticProblem("private-operation-a", problem)
	// A second event in the same local operation/category is excluded before
	// the category cap is reached.
	app.recordDiagnosticProblem("private-operation-a", problem)
	for _, operationID := range []string{"private-operation-b", "private-operation-c", "private-operation-d"} {
		app.recordDiagnosticProblem(operationID, problem)
	}
	events, err := app.diagnostics.Recent()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("recorded %d events, want category cap of 3", len(events))
	}
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"private-operation", "operation_id", "resource_id", "resource_type", "https://"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("serialized event leaked %q: %s", forbidden, encoded)
			}
		}
	}
}

func TestClassifyAnalysisProblemSkipsCancellation(t *testing.T) {
	for _, err := range []error{context.Canceled, &engine.Error{Category: engine.ErrorCancelled, Err: errors.New("private error")}} {
		if problem, ok := classifyAnalysisProblem(err, time.Second); ok {
			t.Fatalf("classifyAnalysisProblem(%v) = %#v, true; want no diagnostic", err, problem)
		}
	}
}
