package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localdiagnostics "github.com/tejasa97/vidstow/internal/diagnostics"
	"github.com/tejasa97/vidstow/internal/ffmpegdetect"
	"github.com/tejasa97/vidstow/internal/jobs"
	"github.com/tejasa97/vidstow/internal/outputplan"
	"github.com/tejasa97/ytdlp-go/engine"
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

func TestClassifyAnalysisProblemMatrix(t *testing.T) {
	private := errors.New("cookie=secret https://example.test/watch?v=private")
	tests := []struct {
		name         string
		err          error
		wantOK       bool
		wantCategory string
	}{
		{name: "deadline", err: context.DeadlineExceeded, wantOK: true, wantCategory: "network_timeout"},
		{name: "canceled", err: context.Canceled},
		{name: "typed cancelled", err: &engine.Error{Category: engine.ErrorCancelled, Err: private}},
		{name: "authentication", err: &engine.Error{Category: engine.ErrorAuthentication, Err: private}, wantOK: true, wantCategory: "authentication_required"},
		{name: "unsupported", err: &engine.Error{Category: engine.ErrorUnsupported, Err: private}, wantOK: true, wantCategory: "unsupported_resource"},
		{name: "network without a finer typed fact", err: &engine.Error{Category: engine.ErrorNetwork, Err: private}, wantOK: true, wantCategory: "extractor_failed"},
		{name: "invalid input", err: &engine.Error{Category: engine.ErrorInvalidInput, Err: private}, wantOK: true, wantCategory: "extractor_failed"},
		{name: "security", err: &engine.Error{Category: engine.ErrorSecurity, Err: private}, wantOK: true, wantCategory: "extractor_failed"},
		{name: "internal", err: &engine.Error{Category: engine.ErrorInternal, Err: private}, wantOK: true, wantCategory: "extractor_failed"},
		{name: "untyped private error", err: private, wantOK: true, wantCategory: "extractor_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			problem, ok := classifyAnalysisProblem(test.err, 1500*time.Millisecond)
			if ok != test.wantOK {
				t.Fatalf("ok = %t, want %t; problem = %#v", ok, test.wantOK, problem)
			}
			if !test.wantOK {
				return
			}
			if problem.Stage != "extraction" || problem.Category != test.wantCategory || problem.Outcome != "terminal" || problem.RetryBucket != "none" || problem.DurationBucket != "500_1999ms" {
				t.Fatalf("classification = %#v, want extraction/%s with duration 500_1999ms", problem, test.wantCategory)
			}
			encoded, err := json.Marshal(problem)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "cookie") || strings.Contains(string(encoded), "https://") {
				t.Fatalf("classification leaked private error text: %s", encoded)
			}
		})
	}
}

func TestDiagnosticDurationBuckets(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{0, "lt_100ms"},
		{99 * time.Millisecond, "lt_100ms"},
		{100 * time.Millisecond, "100_499ms"},
		{499 * time.Millisecond, "100_499ms"},
		{500 * time.Millisecond, "500_1999ms"},
		{1999 * time.Millisecond, "500_1999ms"},
		{2 * time.Second, "2_9s"},
		{9 * time.Second, "2_9s"},
		{10 * time.Second, "10_29s"},
		{29 * time.Second, "10_29s"},
		{30 * time.Second, "30_59s"},
		{59 * time.Second, "30_59s"},
		{time.Minute, "gte_60s"},
	}
	for _, test := range tests {
		if got := diagnosticDurationBucket(test.duration); got != test.want {
			t.Fatalf("diagnosticDurationBucket(%s) = %q, want %q", test.duration, got, test.want)
		}
	}
}

func TestDiagnosticPolicyCapsSessionEvents(t *testing.T) {
	app := NewApp()
	app.openLocalDiagnostics(t.TempDir())
	if app.diagnostics == nil {
		t.Fatal("diagnostics recorder was not opened")
	}
	categories := []localdiagnostics.Problem{
		{Stage: "extraction", Category: "extractor_failed", Outcome: "terminal", RetryBucket: "none"},
		{Stage: "extraction", Category: "authentication_required", Outcome: "terminal", RetryBucket: "none"},
		{Stage: "extraction", Category: "unsupported_resource", Outcome: "terminal", RetryBucket: "none"},
		{Stage: "media_transfer", Category: "transfer_failed", Outcome: "terminal", RetryBucket: "none"},
		{Stage: "media_transfer", Category: "http_403", Outcome: "terminal", RetryBucket: "none"},
		{Stage: "media_transfer", Category: "http_429", Outcome: "terminal", RetryBucket: "none"},
		{Stage: "postprocessing", Category: "ffmpeg_failed", Outcome: "terminal", RetryBucket: "none"},
		{Stage: "filesystem", Category: "permission_denied", Outcome: "terminal", RetryBucket: "none"},
		{Stage: "frontend", Category: "frontend_unhandled", Outcome: "terminal", RetryBucket: "none"},
		{Stage: "internal", Category: "unexpected_internal", Outcome: "terminal", RetryBucket: "none"},
		{Stage: "startup", Category: "state_unavailable", Outcome: "terminal", RetryBucket: "none"},
	}
	for index, problem := range categories {
		app.recordDiagnosticProblem("session-op-"+string(rune('a'+index)), problem)
	}
	events, err := app.diagnostics.Recent()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 10 {
		t.Fatalf("recorded %d events, want session cap of 10", len(events))
	}
	for _, event := range events {
		if event.Problem != nil && event.Problem.Stage == "startup" {
			t.Fatalf("session cap recorded the 11th event: %#v", events)
		}
	}
}

func TestRecordFrontendFailureEmitsClosedEvent(t *testing.T) {
	app := NewApp()
	app.openLocalDiagnostics(t.TempDir())
	app.RecordFrontendFailure()
	app.RecordFrontendFailure()
	events, err := app.diagnostics.Recent()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Problem == nil || events[0].Problem.Stage != "frontend" || events[0].Problem.Category != "frontend_unhandled" {
		t.Fatalf("frontend diagnostic = %#v", events)
	}
}

func TestAnalyzeURLRejectsMalformedInputWithoutDiagnostic(t *testing.T) {
	restore := installAppTestSeams(t)
	defer restore()
	app := NewApp()
	app.startupAt(context.Background(), filepath.Join(secureAppTempDir(t), "state.json"))
	if app.diagnostics == nil {
		t.Fatal("diagnostics recorder was not opened")
	}
	defer func() {
		app.stopCleanup(context.Background())
		_ = app.jobs.Close(context.Background())
		_ = app.store.Close()
	}()
	for _, raw := range []string{"", "not-a-url", "https://example.com/watch?v=dQw4w9WgXcQ", "https://www.youtube.com/playlist?list=PL1234567890"} {
		if _, err := app.AnalyzeURL(raw); err == nil {
			t.Fatalf("AnalyzeURL(%q) succeeded", raw)
		}
	}
	if _, err := app.AnalyzePlaylist("https://www.youtube.com/watch?v=dQw4w9WgXcQ"); err == nil {
		t.Fatal("AnalyzePlaylist accepted a single-video URL")
	}
	events, err := app.diagnostics.Recent()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("pre-admission analysis recorded diagnostics: %#v", events)
	}
}

func TestStartDownloadRecordsFFmpegMissingWithoutFilesystemSideEffects(t *testing.T) {
	restore := installAppTestSeams(t)
	defer restore()
	app := NewApp()
	app.startupAt(context.Background(), filepath.Join(secureAppTempDir(t), "state.json"))
	if app.jobs == nil || app.diagnostics == nil {
		t.Fatalf("startup did not initialize app: %#v", app)
	}
	defer func() {
		app.stopCleanup(context.Background())
		_ = app.jobs.Close(context.Background())
		_ = app.store.Close()
	}()
	app.lastFFmpeg = ffmpegdetect.Status{Available: false, Message: "ffmpeg missing"}
	resolveDownloadPlan = func(_ *jobs.Manager, videoID, planID string) (outputplan.Plan, error) {
		if videoID != "dQw4w9WgXcQ" || planID != "mp3-192" {
			return outputplan.Plan{}, errors.New("unexpected output plan request")
		}
		return outputplan.Plan{
			ID: "mp3-192", Kind: outputplan.KindAudio, Label: "MP3", Container: "MP3",
			RequiresFFmpeg: true, AudioBitrateKbps: 192, Available: true, Selector: "140",
		}, nil
	}
	outputDir := filepath.Join(secureAppTempDir(t), "must-not-exist")
	if _, err := app.StartDownload(jobs.Request{
		URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", VideoID: "dQw4w9WgXcQ",
		Title: "Demo", PlanID: "mp3-192", OutputDir: outputDir,
	}); err == nil {
		t.Fatal("StartDownload accepted a plan that requires missing FFmpeg")
	}
	if _, err := os.Stat(outputDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("FFmpeg rejection touched output directory: %v", err)
	}
	if jobs := app.jobs.List(); len(jobs) != 0 {
		t.Fatalf("FFmpeg rejection added queue jobs: %#v", jobs)
	}
	if jobs := app.store.Snapshot().Jobs; len(jobs) != 0 {
		t.Fatalf("FFmpeg rejection persisted jobs: %#v", jobs)
	}
	events, err := app.diagnostics.Recent()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Problem == nil || events[0].Problem.Stage != "postprocessing" || events[0].Problem.Category != "ffmpeg_missing" {
		t.Fatalf("ffmpeg diagnostic = %#v", events)
	}
	encoded, err := json.Marshal(events[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "dQw4w9WgXcQ") || strings.Contains(string(encoded), "operation_id") {
		t.Fatalf("ffmpeg diagnostic leaked identifiers: %s", encoded)
	}
}

func TestStartDownloadExpiredPlanDoesNotRecordDiagnostic(t *testing.T) {
	restore := installAppTestSeams(t)
	defer restore()
	app := NewApp()
	app.startupAt(context.Background(), filepath.Join(secureAppTempDir(t), "state.json"))
	if app.diagnostics == nil {
		t.Fatal("diagnostics recorder was not opened")
	}
	defer func() {
		app.stopCleanup(context.Background())
		_ = app.jobs.Close(context.Background())
		_ = app.store.Close()
	}()
	if _, err := app.StartDownload(jobs.Request{
		URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", VideoID: "dQw4w9WgXcQ",
		Title: "Demo", PlanID: "expired-plan",
	}); err == nil {
		t.Fatal("StartDownload accepted an expired output plan")
	}
	events, err := app.diagnostics.Recent()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("expired plan recorded diagnostics: %#v", events)
	}
}
