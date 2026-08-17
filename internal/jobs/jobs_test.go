package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tejasa97/vidstow/internal/outputplan"
	"github.com/tejasa97/youtube_dlp/engine"
)

type memoryPersistence struct {
	mu   sync.Mutex
	jobs []PersistedJob
}

type failingPersistence struct {
	mu      sync.Mutex
	jobs    []PersistedJob
	saveErr error
}

type nonDurablePersistence struct{ memoryPersistence }

func (*nonDurablePersistence) Durable() bool { return false }

type controlledPersistence struct {
	mu          sync.Mutex
	jobs        []PersistedJob
	blockNext   bool
	saveStarted chan struct{}
	releaseSave chan struct{}
}

func (p *controlledPersistence) LoadJobs() ([]PersistedJob, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PersistedJob(nil), p.jobs...), nil
}

func (p *controlledPersistence) SaveJobs(jobs []PersistedJob) error {
	p.mu.Lock()
	block := p.blockNext
	if block {
		p.blockNext = false
	}
	started, release := p.saveStarted, p.releaseSave
	p.mu.Unlock()
	if block {
		close(started)
		<-release
	}
	p.mu.Lock()
	p.jobs = append([]PersistedJob(nil), jobs...)
	p.mu.Unlock()
	return nil
}

func (p *controlledPersistence) armBlockingSave() (<-chan struct{}, chan<- struct{}) {
	p.mu.Lock()
	p.blockNext = true
	p.saveStarted = make(chan struct{})
	p.releaseSave = make(chan struct{})
	started, release := p.saveStarted, p.releaseSave
	p.mu.Unlock()
	return started, release
}

func (p *failingPersistence) LoadJobs() ([]PersistedJob, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PersistedJob(nil), p.jobs...), nil
}

func (p *failingPersistence) SaveJobs(jobs []PersistedJob) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.saveErr != nil {
		return p.saveErr
	}
	p.jobs = append([]PersistedJob(nil), jobs...)
	return nil
}

func (p *memoryPersistence) LoadJobs() ([]PersistedJob, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PersistedJob(nil), p.jobs...), nil
}

func (p *memoryPersistence) SaveJobs(jobs []PersistedJob) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jobs = append([]PersistedJob(nil), jobs...)
	return nil
}

func TestSummarizePlaylistBuildsBoundedSelectableEntries(t *testing.T) {
	result := engine.Result{InfoJSON: json.RawMessage(`{"id":"PLfixture","title":"Course","uploader":"Teacher"}`), Entries: []engine.Result{
		{InfoJSON: json.RawMessage(`{"id":"aaaaaaaaaaa","title":"One","url":"https://www.youtube.com/watch?v=aaaaaaaaaaa","playlist_index":1}`)},
		{InfoJSON: json.RawMessage(`{"id":"","title":"Private video","url":"","playlist_index":2}`)},
	}}
	summary, err := summarizePlaylist(result, "https://www.youtube.com/playlist?list=PLfixture")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ID != "PLfixture" || summary.Available != 1 || summary.Unavailable != 1 || len(summary.Entries) != 2 {
		t.Fatalf("summary=%#v", summary)
	}
	if !summary.Entries[0].Available || summary.Entries[1].Available {
		t.Fatalf("availability=%#v", summary.Entries)
	}
	wantThumbnail := "https://i.ytimg.com/vi/aaaaaaaaaaa/hqdefault.jpg"
	if summary.Thumbnail != wantThumbnail || summary.Entries[0].Thumbnail != wantThumbnail {
		t.Fatalf("thumbnail fallback: summary=%q entry=%q", summary.Thumbnail, summary.Entries[0].Thumbnail)
	}
}

func TestSummarizePlaylistEnforcesBoundAndCanonicalChildIdentity(t *testing.T) {
	entries := make([]engine.Result, MaxPlaylistEntries+1)
	for index := range entries {
		id := fmt.Sprintf("%011d", index)
		childURL := "https://www.youtube.com/watch?v=" + id + "&index=7"
		if index == 1 {
			childURL = "https://www.youtube.com/watch?v=99999999999"
		}
		entries[index] = engine.Result{InfoJSON: json.RawMessage(fmt.Sprintf(`{"id":%q,"title":%q,"url":%q,"playlist_index":%d}`, id, "Entry", childURL, index+1))}
	}
	summary, err := summarizePlaylist(engine.Result{
		InfoJSON: json.RawMessage(`{"id":"PLfixture","title":"Course"}`),
		Entries:  entries,
	}, "https://www.youtube.com/playlist?list=PLfixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Entries) != MaxPlaylistEntries || summary.EntryCount != MaxPlaylistEntries {
		t.Fatalf("entry count = %d/%d, want %d", len(summary.Entries), summary.EntryCount, MaxPlaylistEntries)
	}
	if got := summary.Entries[0].URL; got != "https://www.youtube.com/watch?v=00000000000" {
		t.Fatalf("canonical child URL = %q", got)
	}
	if summary.Entries[1].Available || summary.Entries[1].URL != "" {
		t.Fatalf("mismatched video identity crossed the preview boundary: %#v", summary.Entries[1])
	}
}

func TestAnalyzePlaylistRejectsMetadataIdentityMismatch(t *testing.T) {
	manager := New(nil, nil)
	manager.runAnalyze = func(context.Context, engine.Request) (engine.Result, error) {
		return engine.Result{
			InfoJSON: json.RawMessage(`{"id":"PLdifferent","title":"Course"}`),
			Entries:  []engine.Result{{InfoJSON: json.RawMessage(`{"id":"aaaaaaaaaaa","title":"One","url":"https://www.youtube.com/watch?v=aaaaaaaaaaa"}`)}},
		}, nil
	}
	_, err := manager.AnalyzePlaylist(context.Background(), "https://www.youtube.com/playlist?list=PLexpected")
	if err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("AnalyzePlaylist() error = %v, want identity mismatch", err)
	}
}

func TestAnalyzePlaylistPrunesAndCapsPreviewCache(t *testing.T) {
	manager := New(nil, nil)
	now := time.Now()
	manager.playlistCache["expired"] = cachedPlaylist{expiresAt: now.Add(-time.Minute)}
	for index := 0; index < maxCachedPlaylistPreviews; index++ {
		id := fmt.Sprintf("cached-%02d", index)
		manager.playlistCache[id] = cachedPlaylist{expiresAt: now.Add(time.Duration(index+1) * time.Minute)}
	}
	manager.runAnalyze = func(context.Context, engine.Request) (engine.Result, error) {
		return engine.Result{
			InfoJSON: json.RawMessage(`{"id":"PLexpected","title":"Course"}`),
			Entries:  []engine.Result{{InfoJSON: json.RawMessage(`{"id":"aaaaaaaaaaa","title":"One","url":"https://www.youtube.com/watch?v=aaaaaaaaaaa"}`)}},
		}, nil
	}
	if _, err := manager.AnalyzePlaylist(context.Background(), "https://www.youtube.com/playlist?list=PLexpected"); err != nil {
		t.Fatal(err)
	}
	if _, exists := manager.playlistCache["expired"]; exists {
		t.Fatal("expired preview was not pruned")
	}
	if _, exists := manager.playlistCache["PLexpected"]; !exists {
		t.Fatal("new preview was not cached")
	}
	if len(manager.playlistCache) != maxCachedPlaylistPreviews {
		t.Fatalf("cache size = %d, want %d", len(manager.playlistCache), maxCachedPlaylistPreviews)
	}
}

func TestSummarizeAnalysisBuildsPublicCuratedPlans(t *testing.T) {
	raw := json.RawMessage(`{
		"id":"abc123","title":"Demo","uploader":"Creator","duration":90,
		"view_count":1234,"upload_date":"20260807","description":"Description","availability":"public",
		"formats":[
			{"format_id":"137","ext":"mp4","vcodec":"avc1.640028","acodec":"none","width":1920,"height":1080,"tbr":4000},
			{"format_id":"140","ext":"m4a","vcodec":"none","acodec":"mp4a.40.2","abr":128}
		]
	}`)
	summary, privatePlans, err := summarizeAnalysis(raw, "https://example.invalid/watch?v=abc123")
	if err != nil {
		t.Fatal(err)
	}
	if summary.DurationSeconds != 90 || summary.Duration != "1:30" || summary.ViewCount != 1234 {
		t.Fatalf("metadata = %#v; want duration and view count", summary)
	}
	if len(summary.Plans) == 0 || len(privatePlans) == 0 {
		t.Fatal("expected curated output plans")
	}
	if summary.Plans[0].Selector != "" || len(summary.Plans[0].SourceFormatIDs) != 0 {
		t.Fatal("public summary leaked private engine selector")
	}
	if !summary.Plans[0].Available {
		t.Fatal("returned public plan must be informationally available")
	}
	if summary.Access.Code != "public" || summary.Access.Label != "Publicly accessible" {
		t.Fatalf("access = %#v; want public extraction metadata", summary.Access)
	}
	if summary.MediaType != "" {
		t.Fatalf("media type = %q; want empty when the engine omits media_type", summary.MediaType)
	}
	if privatePlans[0].Selector != "137+140" {
		t.Fatalf("private selector = %q; want 137+140", privatePlans[0].Selector)
	}
}

func TestSummarizeAnalysisCopiesEngineMediaType(t *testing.T) {
	raw := json.RawMessage(`{"id":"abc123","title":"Demo","media_type":"Short"}`)
	summary, _, err := summarizeAnalysis(raw, "https://www.youtube.com/watch?v=abc123")
	if err != nil {
		t.Fatal(err)
	}
	if summary.MediaType != "short" {
		t.Fatalf("media type = %q; want short", summary.MediaType)
	}
}

func TestSummarizeAccessUsesNeutralFallback(t *testing.T) {
	for _, metadata := range []map[string]any{
		nil,
		{"availability": "not-a-provider-contract"},
	} {
		access := summarizeAccess(metadata)
		if access.Code != "unknown" || access.Label != "Access status not reported" {
			t.Fatalf("access = %#v; want neutral fallback", access)
		}
	}
}

func TestPlanSubmissionUsesCachedPrivateSelectorAndMP3Postprocessor(t *testing.T) {
	manager := New(nil, nil)
	manager.cachePlans("abc123", []outputplan.Plan{{
		ID: "audio-mp3-192", Kind: outputplan.KindAudio, Label: "MP3 192 kbps",
		Container: "MP3", RequiresFFmpeg: true, AudioBitrateKbps: 192, Selector: "140",
	}})
	started := make(chan engine.Request, 1)
	manager.runDownload = func(_ context.Context, req engine.Request, _ engine.EventHandler) (engine.Result, error) {
		started <- req
		return engine.Result{}, nil
	}

	_, err := manager.Submit(Request{
		URL: "https://example.invalid/watch?v=abc123", VideoID: "abc123",
		PlanID: "audio-mp3-192", OutputDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-started:
		if req.Format != "140" {
			t.Fatalf("format = %q; want cached selector 140", req.Format)
		}
		if len(req.Postprocessors) != 1 || req.Postprocessors[0].ExtractAudio == nil {
			t.Fatal("expected one extract-audio postprocessor")
		}
		if got := req.Postprocessors[0].ExtractAudio.Bitrate; got != "192k" {
			t.Fatalf("bitrate = %q; want 192k", got)
		}
	case <-time.After(time.Second):
		t.Fatal("download runner did not receive a request")
	}
}

func TestResolvePlanRejectsExpiredAndUnknownPlans(t *testing.T) {
	manager := New(nil, nil)
	manager.planCache["expired"] = cachedPlans{
		plans: []outputplan.Plan{{ID: "video-1080-mp4"}}, expiresAt: time.Now().Add(-time.Second),
	}
	if _, err := manager.ResolvePlan("expired", "video-1080-mp4"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired ResolvePlan() error = %v", err)
	}
	manager.cachePlans("current", []outputplan.Plan{{ID: "video-1080-mp4"}})
	if _, err := manager.ResolvePlan("current", "made-up"); err == nil || !strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("unknown ResolvePlan() error = %v", err)
	}
}

func TestHumanErrorYouTubeChallengeTimeout(t *testing.T) {
	typed := &engine.Error{
		Category: engine.ErrorUnsupported,
		Op:       "youtube extraction",
		Err: errors.New(
			"JavaScript challenge solver unavailable: EJS helper timeout: JavaScript execution timed out",
		),
	}
	err := fmt.Errorf("download video: %w", typed)

	if got := humanError(err); got != "YouTube challenge timed out — retry" {
		t.Fatalf("humanError() = %q; want challenge-timeout message", got)
	}
	if got := errorReason(err); got != retryCodeYouTubeChallengePreTransfer {
		t.Fatalf("errorReason() = %q; want pre-transfer marker", got)
	}
}

func TestHumanErrorOtherUnsupportedIsUnchanged(t *testing.T) {
	err := &engine.Error{
		Category: engine.ErrorUnsupported,
		Op:       "youtube extraction",
		Err:      errors.New("video unavailable"),
	}

	if got := humanError(err); got != "This link is not supported" {
		t.Fatalf("humanError() = %q; want ordinary unsupported message", got)
	}
}

func TestHandleEventUsesPublicDownloadEventKinds(t *testing.T) {
	manager := New(nil, nil)
	state := &jobState{snap: JobSnapshot{Status: StatusActive}}

	manager.handleEvent(state, engine.Event{
		Kind:  engine.EventDownloadProgress,
		Bytes: 25,
		Total: 100,
	})

	if state.snap.Bytes != 25 || state.snap.Total != 100 {
		t.Fatalf("progress bytes = %d/%d; want 25/100", state.snap.Bytes, state.snap.Total)
	}
	if math.Abs(state.snap.Progress-0.25) > 0.0001 {
		t.Fatalf("progress = %f; want 0.25", state.snap.Progress)
	}
	if state.snap.Message != "Downloading" {
		t.Fatalf("message = %q; want Downloading", state.snap.Message)
	}

	state.startBps = time.Now().Add(-time.Second)
	state.startByt = 25
	manager.handleEvent(state, engine.Event{
		Kind:  engine.EventDownloadProgress,
		Bytes: 75,
		Total: 100,
	})
	if state.snap.SpeedBps <= 0 || state.snap.ETASeconds <= 0 {
		t.Fatalf("speed/eta = %f/%f; want positive rolling estimates", state.snap.SpeedBps, state.snap.ETASeconds)
	}
}

func TestHandleEventMapsLifecycleCopy(t *testing.T) {
	manager := New(nil, nil)
	state := &jobState{snap: JobSnapshot{Status: StatusActive}}

	tests := []struct {
		kind string
		want string
	}{
		{engine.EventDownloadStarting, "Starting download"},
		{engine.EventDownloadRetry, "Retrying"},
		{engine.EventPostprocessStarting, "Finalising"},
		{engine.EventDownloadCancelled, "Canceled"},
	}
	for _, test := range tests {
		manager.handleEvent(state, engine.Event{Kind: test.kind})
		if state.snap.Message != test.want {
			t.Fatalf("kind %q message = %q; want %q", test.kind, state.snap.Message, test.want)
		}
	}
}

func TestCancelActiveKeepsFIFOSingleActive(t *testing.T) {
	manager := New(nil, nil)
	manager.SetConcurrency(1)
	started := make(chan engine.Request, 2)
	release := make(chan struct{})
	manager.runDownload = func(ctx context.Context, req engine.Request, _ engine.EventHandler) (engine.Result, error) {
		started <- req
		<-ctx.Done()
		<-release
		return engine.Result{}, ctx.Err()
	}

	first, err := manager.Submit(Request{URL: "https://example.invalid/first", OutputDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first job did not start")
	}
	second, err := manager.Submit(Request{URL: "https://example.invalid/second", OutputDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	manager.Cancel(first)
	select {
	case <-started:
		t.Fatal("next FIFO job started before canceled worker exited")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("next FIFO job did not start after canceled worker exited")
	}
	manager.Cancel(second)
}

func TestDownloadConcurrencyDefaultsToTwoAndClampsToSupportedRange(t *testing.T) {
	manager := New(nil, nil)
	if got := manager.Concurrency(); got != 2 {
		t.Fatalf("default concurrency = %d; want 2", got)
	}
	if got := manager.SetConcurrency(0); got != 1 {
		t.Fatalf("low concurrency clamp = %d; want 1", got)
	}
	if got := manager.SetConcurrency(99); got != 10 {
		t.Fatalf("high concurrency clamp = %d; want 10", got)
	}
}

func TestMediaProcessingSchedulerCapsConcurrentFFmpegStagesAtThree(t *testing.T) {
	manager := New(nil, nil)
	manager.SetConcurrency(4)
	entered := make(chan struct{}, 4)
	release := make(chan struct{}, 4)
	manager.runDownload = func(ctx context.Context, _ engine.Request, handler engine.EventHandler) (engine.Result, error) {
		if err := handler(ctx, engine.Event{Kind: engine.EventPostprocessStarting}); err != nil {
			return engine.Result{}, err
		}
		entered <- struct{}{}
		<-release
		if err := handler(ctx, engine.Event{Kind: engine.EventPostprocessCompleted}); err != nil {
			return engine.Result{}, err
		}
		return engine.Result{}, nil
	}

	for index := 0; index < 4; index++ {
		if _, err := manager.Submit(Request{
			URL: fmt.Sprintf("https://example.invalid/%d", index), OutputDir: t.TempDir(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 3; index++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("one of the first three processing stages did not start")
		}
	}
	select {
	case <-entered:
		t.Fatal("fourth processing stage exceeded the hard cap of three")
	case <-time.After(50 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fourth processing stage did not start when a slot was released")
	}
	for index := 0; index < 3; index++ {
		release <- struct{}{}
	}
}

func TestPauseAndResumeActiveDownloadPreservesProgress(t *testing.T) {
	manager := New(nil, nil)
	manager.SetConcurrency(1)
	started := make(chan struct{}, 2)
	progressRecorded := make(chan struct{}, 2)
	manager.runDownload = func(ctx context.Context, _ engine.Request, handler engine.EventHandler) (engine.Result, error) {
		started <- struct{}{}
		if err := handler(ctx, engine.Event{Kind: engine.EventDownloadProgress, Bytes: 50, Total: 100}); err != nil {
			return engine.Result{}, err
		}
		progressRecorded <- struct{}{}
		<-ctx.Done()
		return engine.Result{}, ctx.Err()
	}
	id, err := manager.Submit(Request{URL: "https://example.invalid/video", OutputDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	<-progressRecorded
	if err := manager.Pause(id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		snap, _ := manager.Find(id)
		if snap.Status == StatusPaused {
			if snap.Bytes != 50 || snap.Progress != 0.5 {
				t.Fatalf("paused progress = %d/%f; want 50/0.5", snap.Bytes, snap.Progress)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not pause; final status %q", snap.Status)
		}
		time.Sleep(time.Millisecond)
	}
	if err := manager.Resume(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("resumed job did not restart the engine request")
	}
	manager.Cancel(id)
}

func TestPersistenceFailureIsReportedAndRecoveryClearsIt(t *testing.T) {
	events := make(chan Event, 2)
	persistence := &failingPersistence{saveErr: errors.New("write /private/queue.json: permission denied")}
	manager := New(nil, func(event Event) {
		if event.Name == EventPersistence {
			events <- event
		}
	})
	if err := manager.SetPersistence(persistence, false); err == nil {
		t.Fatal("SetPersistence() error = nil; want initial save error")
	}
	status := manager.PersistenceStatus()
	if !status.Available || status.Healthy || status.Message != persistenceFailureMessage {
		t.Fatalf("failure status = %#v", status)
	}
	select {
	case event := <-events:
		if event.Persistence == nil || event.Persistence.Message != persistenceFailureMessage {
			t.Fatalf("event = %#v; want safe persistence failure", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for persistence failure event")
	}

	persistence.mu.Lock()
	persistence.saveErr = nil
	persistence.mu.Unlock()
	if err := manager.FlushPersistence(); err != nil {
		t.Fatalf("FlushPersistence() recovery error = %v", err)
	}
	status = manager.PersistenceStatus()
	if !status.Available || !status.Healthy || status.Message != "" {
		t.Fatalf("recovered status = %#v", status)
	}
	select {
	case event := <-events:
		if event.Persistence == nil || !event.Persistence.Healthy || event.Persistence.Message != "" {
			t.Fatalf("recovery event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for persistence recovery event")
	}
}

func TestCloseReturnsSafeFinalPersistenceFailure(t *testing.T) {
	persistence := &failingPersistence{}
	manager := New(nil, nil)
	if err := manager.SetPersistence(persistence, false); err != nil {
		t.Fatalf("SetPersistence() = %v", err)
	}
	persistence.mu.Lock()
	persistence.saveErr = errors.New("write /private/state.json: no space left")
	persistence.mu.Unlock()
	if err := manager.Close(); err == nil || err.Error() != persistenceFailureMessage {
		t.Fatalf("Close() error = %v; want safe persistence error", err)
	}
}

func TestNonDurablePersistenceReportsTemporaryQueueStorage(t *testing.T) {
	events := make(chan Event, 1)
	persistence := &nonDurablePersistence{}
	manager := New(nil, func(event Event) {
		if event.Name == EventPersistence {
			events <- event
		}
	})
	if err := manager.SetPersistence(persistence, false); err != nil {
		t.Fatalf("SetPersistence() = %v", err)
	}
	status := manager.PersistenceStatus()
	if status.Available || !status.Healthy || status.Message != persistenceUnavailableMessage {
		t.Fatalf("non-durable status = %#v", status)
	}
	select {
	case event := <-events:
		if event.Persistence == nil || event.Persistence.Available || event.Persistence.Message != persistenceUnavailableMessage {
			t.Fatalf("event = %#v; want safe temporary-storage status", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for temporary-storage status")
	}
	if err := manager.FlushPersistence(); err != nil {
		t.Fatalf("FlushPersistence() = %v", err)
	}
	if got := manager.PersistenceStatus(); got.Available || got.Message != persistenceUnavailableMessage {
		t.Fatalf("status after in-memory save = %#v; must remain non-durable", got)
	}
}

func TestConcurrentFlushesPersistNewestSnapshotAndStatus(t *testing.T) {
	persistence := &controlledPersistence{}
	manager := New(nil, nil)
	if err := manager.SetPersistence(persistence, false); err != nil {
		t.Fatalf("SetPersistence() = %v", err)
	}

	manager.mu.Lock()
	manager.all["old"] = &jobState{snap: JobSnapshot{ID: "old", Status: StatusPaused, CreatedAt: "1"}}
	manager.mu.Unlock()
	started, release := persistence.armBlockingSave()
	firstDone := make(chan error, 1)
	go func() { firstDone <- manager.FlushPersistence() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first save did not start")
	}

	manager.mu.Lock()
	delete(manager.all, "old")
	manager.all["new"] = &jobState{snap: JobSnapshot{ID: "new", Status: StatusPaused, CreatedAt: "2"}}
	manager.mu.Unlock()
	secondDone := make(chan error, 1)
	go func() { secondDone <- manager.FlushPersistence() }()
	close(release)
	for index, done := range []<-chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("flush %d error = %v", index+1, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("flush %d did not complete", index+1)
		}
	}
	persistence.mu.Lock()
	stored := append([]PersistedJob(nil), persistence.jobs...)
	persistence.mu.Unlock()
	if len(stored) != 1 || stored[0].Snapshot.ID != "new" {
		t.Fatalf("final persisted jobs = %#v; want newest snapshot", stored)
	}
	if status := manager.PersistenceStatus(); !status.Available || !status.Healthy || status.Message != "" {
		t.Fatalf("final status = %#v; want newest successful flush", status)
	}
}

func TestPauseAllPausesActiveAndPendingJobs(t *testing.T) {
	manager := New(nil, nil)
	manager.SetConcurrency(1)
	started := make(chan struct{}, 1)
	manager.runDownload = func(ctx context.Context, _ engine.Request, _ engine.EventHandler) (engine.Result, error) {
		started <- struct{}{}
		<-ctx.Done()
		return engine.Result{}, ctx.Err()
	}
	first, err := manager.Submit(Request{URL: "https://example.invalid/one", OutputDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	second, err := manager.Submit(Request{URL: "https://example.invalid/two", OutputDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.PauseAll(); got != 2 {
		t.Fatalf("PauseAll() = %d; want 2", got)
	}
	deadline := time.Now().Add(time.Second)
	for {
		firstSnap, _ := manager.Find(first)
		secondSnap, _ := manager.Find(second)
		if firstSnap.Status == StatusPaused && secondSnap.Status == StatusPaused {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("statuses = %s/%s; want paused/paused", firstSnap.Status, secondSnap.Status)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPauseIsDisabledDuringMediaProcessing(t *testing.T) {
	manager := New(nil, nil)
	processing := make(chan struct{})
	release := make(chan struct{})
	manager.runDownload = func(ctx context.Context, _ engine.Request, handler engine.EventHandler) (engine.Result, error) {
		if err := handler(ctx, engine.Event{Kind: engine.EventPostprocessStarting}); err != nil {
			return engine.Result{}, err
		}
		close(processing)
		<-release
		return engine.Result{}, nil
	}
	id, err := manager.Submit(Request{URL: "https://example.invalid/video", OutputDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	<-processing
	if err := manager.Pause(id); err == nil || !strings.Contains(err.Error(), "finalized") {
		t.Fatalf("Pause() error = %v; want processing-stage rejection", err)
	}
	close(release)
}

func TestPersistenceRestoresInterruptedJobAsPausedWithPrivatePlan(t *testing.T) {
	persistence := &memoryPersistence{jobs: []PersistedJob{{
		Snapshot: JobSnapshot{
			ID: "job-1", URL: "https://example.invalid/video", OutputDir: t.TempDir(),
			VideoID: "abc123", PlanID: "video-1080-mp4", Status: StatusActive,
		},
		Plan:            outputplan.Plan{ID: "video-1080-mp4", Kind: outputplan.KindVideo, Container: "MP4"},
		PrivateSelector: "137+140",
	}}}
	manager := New(nil, nil)
	if err := manager.SetPersistence(persistence, true); err != nil {
		t.Fatal(err)
	}
	snap, ok := manager.Find("job-1")
	if !ok || snap.Status != StatusPaused {
		t.Fatalf("restored snapshot = %#v, %v; want paused", snap, ok)
	}
	manager.FlushPersistence()
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	if len(persistence.jobs) != 1 || persistence.jobs[0].PrivateSelector != "137+140" {
		t.Fatalf("persisted jobs = %#v; private selector was not retained", persistence.jobs)
	}
}

func TestDownloadRequestUsesConfiguredFFmpegLocation(t *testing.T) {
	manager := New(nil, nil)
	configured := filepath.Join(t.TempDir(), "ffmpeg")
	manager.SetFFmpegLocation(configured)
	started := make(chan engine.Request, 1)
	manager.runDownload = func(ctx context.Context, req engine.Request, _ engine.EventHandler) (engine.Result, error) {
		started <- req
		<-ctx.Done()
		return engine.Result{}, ctx.Err()
	}

	id, err := manager.Submit(Request{URL: "https://example.invalid/video", OutputDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-started:
		if req.Filesystem.FfmpegLocation != configured {
			t.Fatalf("ffmpeg location = %q; want %q", req.Filesystem.FfmpegLocation, configured)
		}
		if !req.Filesystem.PreservePartialOnCancel {
			t.Fatal("desktop requests must preserve resumable partial state")
		}
	case <-time.After(time.Second):
		t.Fatal("download runner did not receive a request")
	}
	manager.Cancel(id)
}

func TestDownloadRequestsUseExactV0SelectorsAndDistinctOutputTemplates(t *testing.T) {
	tests := []struct {
		quality  Quality
		selector string
		template string
	}{
		{QualityBest, "bv*+ba/b", "%(title)s [%(id)s] [Best].%(ext)s"},
		{Quality4K, "bv*[height<=2160]+ba/b[height<=2160]", "%(title)s [%(id)s] [4K].%(ext)s"},
		{Quality1440p, "bv*[height<=1440]+ba/b[height<=1440]", "%(title)s [%(id)s] [1440p].%(ext)s"},
		{Quality1080p, "bv*[height<=1080]+ba/b[height<=1080]", "%(title)s [%(id)s] [1080p].%(ext)s"},
		{Quality720p, "bv*[height<=720]+ba/b[height<=720]", "%(title)s [%(id)s] [720p].%(ext)s"},
		{QualityAudioOnly, "ba/b", "%(title)s [%(id)s] [Audio only].%(ext)s"},
	}

	for _, test := range tests {
		t.Run(test.quality.Label(), func(t *testing.T) {
			manager := New(nil, nil)
			started := make(chan engine.Request, 1)
			manager.runDownload = func(_ context.Context, req engine.Request, _ engine.EventHandler) (engine.Result, error) {
				started <- req
				return engine.Result{}, nil
			}

			if _, err := manager.Submit(Request{URL: "https://example.invalid/video", OutputDir: t.TempDir(), Quality: test.quality}); err != nil {
				t.Fatal(err)
			}
			select {
			case req := <-started:
				if req.Format != test.selector {
					t.Fatalf("format = %q; want %q", req.Format, test.selector)
				}
				if req.OutputTemplate != test.template {
					t.Fatalf("output template = %q; want %q", req.OutputTemplate, test.template)
				}
			case <-time.After(time.Second):
				t.Fatal("download runner did not receive a request")
			}
		})
	}

	if QualityBest.outputTemplate() == QualityAudioOnly.outputTemplate() {
		t.Fatal("Best and Audio only must not resolve to the same output template")
	}
}

func TestSubmitRejectsUnknownQuality(t *testing.T) {
	manager := New(nil, nil)
	if _, err := manager.Submit(Request{
		URL: "https://example.invalid/video", OutputDir: t.TempDir(), Quality: Quality("8k"),
	}); err == nil || !strings.Contains(err.Error(), "unsupported quality") {
		t.Fatalf("Submit() error = %v; want unsupported quality", err)
	}
}

func TestEventsAreDeliveredInEmissionOrder(t *testing.T) {
	received := make(chan string, 3)
	manager := New(nil, func(event Event) { received <- event.Job.ID })

	manager.mu.Lock()
	manager.emitLocked(Event{Name: EventJobUpdate, Job: JobSnapshot{ID: "first"}})
	manager.emitLocked(Event{Name: EventJobUpdate, Job: JobSnapshot{ID: "second"}})
	manager.emitLocked(Event{Name: EventJobUpdate, Job: JobSnapshot{ID: "third"}})
	manager.mu.Unlock()

	for _, want := range []string{"first", "second", "third"} {
		select {
		case got := <-received:
			if got != want {
				t.Fatalf("event = %q; want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

func TestQueueEventsCannotOvertakeJobEvents(t *testing.T) {
	enteredFirst := make(chan struct{})
	releaseFirst := make(chan struct{})
	received := make(chan Event, 3)
	manager := New(nil, func(event Event) {
		if event.Name == EventJobUpdate && event.Job.ID == "first" {
			close(enteredFirst)
			<-releaseFirst
		}
		received <- event
	})

	manager.mu.Lock()
	manager.emitLocked(Event{Name: EventJobUpdate, Job: JobSnapshot{ID: "first"}})
	manager.mu.Unlock()
	select {
	case <-enteredFirst:
	case <-time.After(time.Second):
		t.Fatal("first event did not reach dispatcher")
	}

	manager.mu.Lock()
	manager.all["queued"] = &jobState{snap: JobSnapshot{ID: "queued", Status: StatusPending}}
	manager.emitQueueLocked()
	manager.emitLocked(Event{Name: EventJobUpdate, Job: JobSnapshot{ID: "third"}})
	manager.mu.Unlock()
	select {
	case event := <-received:
		t.Fatalf("event %q overtook blocked first event", event.Name)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseFirst)

	for _, want := range []string{EventJobUpdate, EventQueue, EventJobUpdate} {
		select {
		case event := <-received:
			if event.Name != want {
				t.Fatalf("event = %q; want %q", event.Name, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

func TestLosslessEventMailboxDeliversBlockedBurstAndReleasesAfterDrain(t *testing.T) {
	enteredFirst := make(chan struct{})
	releaseFirst := make(chan struct{})
	received := make([]Event, 0, 322)
	var receivedMu sync.Mutex
	manager := New(nil, func(event Event) {
		if event.Name == EventJobUpdate && event.Job.ID == "first" {
			close(enteredFirst)
			<-releaseFirst
		}
		receivedMu.Lock()
		received = append(received, event)
		receivedMu.Unlock()
	})

	manager.mu.Lock()
	manager.emitLocked(Event{Name: EventJobUpdate, Job: JobSnapshot{ID: "first"}})
	manager.mu.Unlock()
	select {
	case <-enteredFirst:
	case <-time.After(time.Second):
		t.Fatal("first event did not block the listener")
	}

	manager.mu.Lock()
	const burstEvents = 320
	for index := 0; index < burstEvents; index++ {
		id := fmt.Sprintf("burst-%d", index)
		status := StatusPaused
		if index == burstEvents-1 {
			id = "completed"
			status = StatusComplete
		}
		manager.emitLocked(Event{Name: EventJobUpdate, Job: JobSnapshot{ID: id, Status: status}})
	}
	manager.eventMu.Lock()
	pending := len(manager.pendingEvents)
	manager.eventMu.Unlock()
	manager.mu.Unlock()
	if pending != burstEvents {
		t.Fatalf("pending mailbox length = %d; want all %d blocked events", pending, burstEvents)
	}

	close(releaseFirst)
	deadline := time.Now().Add(time.Second)
	for {
		manager.eventMu.Lock()
		drained := manager.pendingEvents == nil
		manager.eventMu.Unlock()
		if drained {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("event mailbox did not release drained storage")
		}
		time.Sleep(time.Millisecond)
	}
	receivedMu.Lock()
	defer receivedMu.Unlock()
	if len(received) != burstEvents+1 {
		t.Fatalf("received events = %d; want %d", len(received), burstEvents+1)
	}
	if received[0].Job.ID != "first" {
		t.Fatalf("first event = %q; want first", received[0].Job.ID)
	}
	for index := 0; index < burstEvents-1; index++ {
		want := fmt.Sprintf("burst-%d", index)
		if got := received[index+1].Job.ID; got != want {
			t.Fatalf("event %d = %q; want %q", index+1, got, want)
		}
	}
	terminal := received[len(received)-1]
	if terminal.Name != EventJobUpdate || terminal.Job.ID != "completed" || terminal.Job.Status != StatusComplete {
		t.Fatalf("terminal event = %#v; want delivered completion update", terminal)
	}
}

func TestCloseDrainsBlockedTerminalEventBeforeReturning(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	received := make(chan Event, 2)
	manager := New(nil, func(event Event) {
		if event.Job.ID == "blocked" {
			close(entered)
			<-release
		}
		received <- event
	})

	manager.mu.Lock()
	manager.emitLocked(Event{Name: EventJobUpdate, Job: JobSnapshot{ID: "blocked", Status: StatusActive}})
	manager.emitLocked(Event{Name: EventJobUpdate, Job: JobSnapshot{ID: "completed", Status: StatusComplete}})
	manager.mu.Unlock()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("listener did not block on the first event")
	}

	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close() returned while listener was blocked: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-closed; err != nil {
		t.Fatalf("Close() = %v", err)
	}

	for _, want := range []struct {
		id     string
		status Status
	}{
		{id: "blocked", status: StatusActive},
		{id: "completed", status: StatusComplete},
	} {
		select {
		case event := <-received:
			if event.Name != EventJobUpdate || event.Job.ID != want.id || event.Job.Status != want.status {
				t.Fatalf("event = %#v; want %s/%s", event, want.id, want.status)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s event", want.id)
		}
	}
	select {
	case <-manager.eventDone:
	default:
		t.Fatal("dispatcher is still running after Close returned")
	}
}

func TestCloseDeliversFinalPersistenceEvent(t *testing.T) {
	persistence := &failingPersistence{}
	events := make(chan Event, 1)
	manager := New(nil, func(event Event) {
		if event.Name == EventPersistence {
			events <- event
		}
	})
	if err := manager.SetPersistence(persistence, false); err != nil {
		t.Fatalf("SetPersistence() = %v", err)
	}
	persistence.mu.Lock()
	persistence.saveErr = errors.New("write /private/queue.json: no space left")
	persistence.mu.Unlock()

	if err := manager.Close(); err == nil || err.Error() != persistenceFailureMessage {
		t.Fatalf("Close() error = %v; want final persistence error", err)
	}
	select {
	case event := <-events:
		if event.Persistence == nil || event.Persistence.Healthy || event.Persistence.Message != persistenceFailureMessage {
			t.Fatalf("final persistence event = %#v; want failure status", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for final persistence event")
	}
}

func TestCloseIsSafeWithNilListenerAndRejectsNewActivity(t *testing.T) {
	manager := New(nil, nil)
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("repeated Close() = %v", err)
	}
	select {
	case <-manager.eventDone:
	default:
		t.Fatal("nil-listener manager did not mark dispatcher complete")
	}
	if _, err := manager.Submit(Request{URL: "https://example.invalid/video", OutputDir: t.TempDir()}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Submit() after Close() = %v; want ErrClosed", err)
	}
}

func TestConcurrentCloseCallsShareResult(t *testing.T) {
	persistence := &failingPersistence{}
	manager := New(nil, nil)
	if err := manager.SetPersistence(persistence, false); err != nil {
		t.Fatalf("SetPersistence() = %v", err)
	}
	persistence.mu.Lock()
	persistence.saveErr = errors.New("write /private/queue.json: permission denied")
	persistence.mu.Unlock()

	const callers = 16
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			errs <- manager.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err == nil || err.Error() != persistenceFailureMessage {
			t.Fatalf("concurrent Close() error = %v; want shared persistence error", err)
		}
	}
}

func TestCloseJoinsActiveWorkerAndPersistsPausedState(t *testing.T) {
	started := make(chan struct{})
	allowExit := make(chan struct{})
	exited := make(chan struct{})
	events := make(chan Event, 16)
	persistence := &memoryPersistence{}
	manager := New(nil, func(event Event) { events <- event })
	manager.runDownload = func(ctx context.Context, _ engine.Request, _ engine.EventHandler) (engine.Result, error) {
		close(started)
		<-allowExit
		close(exited)
		return engine.Result{}, ctx.Err()
	}
	if err := manager.SetPersistence(persistence, false); err != nil {
		t.Fatalf("SetPersistence() = %v", err)
	}
	id, err := manager.Submit(Request{URL: "https://example.invalid/video", OutputDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}

	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	waitForManagerClosing(t, manager)
	select {
	case err := <-closed:
		t.Fatalf("Close() returned before the worker was released: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(allowExit)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("worker did not exit after Close cancellation was released")
	}
	if err := <-closed; err != nil {
		t.Fatalf("Close() = %v", err)
	}

	persistence.mu.Lock()
	stored := append([]PersistedJob(nil), persistence.jobs...)
	persistence.mu.Unlock()
	if len(stored) != 1 || stored[0].Snapshot.ID != id || stored[0].Snapshot.Status != StatusPaused {
		t.Fatalf("persisted after Close = %#v; want paused worker state", stored)
	}
	foundPaused := false
	for {
		select {
		case event := <-events:
			if event.Name == EventJobUpdate && event.Job.ID == id && event.Job.Status == StatusPaused {
				foundPaused = true
			}
		default:
			if !foundPaused {
				t.Fatalf("Close() drained no paused worker event for %s", id)
			}
			return
		}
	}
}

func TestCloseDrainsCompletedWorkerEventAtCompletionBoundary(t *testing.T) {
	enteredTerminal := make(chan struct{})
	releaseListener := make(chan struct{})
	events := make(chan Event, 16)
	persistence := &memoryPersistence{}
	manager := New(nil, func(event Event) {
		if event.Name == EventJobUpdate && event.Job.Status == StatusComplete {
			close(enteredTerminal)
			<-releaseListener
		}
		events <- event
	})
	manager.runDownload = func(_ context.Context, _ engine.Request, _ engine.EventHandler) (engine.Result, error) {
		return engine.Result{}, nil
	}
	if err := manager.SetPersistence(persistence, false); err != nil {
		t.Fatalf("SetPersistence() = %v", err)
	}
	id, err := manager.Submit(Request{URL: "https://example.invalid/video", OutputDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	select {
	case <-enteredTerminal:
	case <-time.After(time.Second):
		t.Fatal("worker completion event was not delivered to the listener")
	}

	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close() returned while the completed event listener was blocked: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseListener)
	if err := <-closed; err != nil {
		t.Fatalf("Close() = %v", err)
	}

	completed := 0
	for {
		select {
		case event := <-events:
			if event.Name == EventJobUpdate && event.Job.ID == id && event.Job.Status == StatusComplete {
				completed++
			}
		default:
			if completed != 1 {
				t.Fatalf("completed events for %s = %d; want exactly once", id, completed)
			}
			persistence.mu.Lock()
			stored := append([]PersistedJob(nil), persistence.jobs...)
			persistence.mu.Unlock()
			if len(stored) != 0 {
				t.Fatalf("terminal job persisted after completion = %#v; want queue exclusion", stored)
			}
			return
		}
	}
}

func TestCloseCancelsInFlightAnalyzeAndReturnsPromptly(t *testing.T) {
	started := make(chan struct{})
	runnerCanceled := make(chan struct{})
	manager := New(nil, nil)
	manager.runAnalyze = func(ctx context.Context, _ engine.Request) (engine.Result, error) {
		close(started)
		<-ctx.Done()
		close(runnerCanceled)
		return engine.Result{}, ctx.Err()
	}

	analyzeDone := make(chan error, 1)
	go func() {
		_, err := manager.Analyze(context.Background(), "https://example.invalid/video")
		analyzeDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("analysis runner did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case <-runnerCanceled:
	case <-time.After(time.Second):
		t.Fatal("Close() did not cancel the in-flight analysis")
	}
	select {
	case err := <-analyzeDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Analyze() error = %v; want manager cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Analyze() did not return after lifecycle cancellation")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() did not return promptly after analysis cancellation")
	}
}

func TestAnalyzePreservesCallerCancellationAndDeadline(t *testing.T) {
	started := make(chan struct{})
	deadlineSeen := make(chan time.Time, 1)
	manager := New(nil, nil)
	manager.runAnalyze = func(ctx context.Context, _ engine.Request) (engine.Result, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return engine.Result{}, errors.New("analysis context lost caller deadline")
		}
		deadlineSeen <- deadline
		close(started)
		<-ctx.Done()
		return engine.Result{}, ctx.Err()
	}

	callerDeadline := time.Now().Add(time.Minute)
	callerCtx, cancelCaller := context.WithDeadline(context.Background(), callerDeadline)
	defer cancelCaller()
	analyzeDone := make(chan error, 1)
	go func() {
		_, err := manager.Analyze(callerCtx, "https://example.invalid/video")
		analyzeDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("analysis runner did not start")
	}
	select {
	case got := <-deadlineSeen:
		if delta := got.Sub(callerDeadline); delta < -time.Millisecond || delta > time.Millisecond {
			t.Fatalf("analysis deadline = %v; want caller deadline %v", got, callerDeadline)
		}
	case <-time.After(time.Second):
		t.Fatal("analysis runner did not report its deadline")
	}
	cancelCaller()
	select {
	case err := <-analyzeDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Analyze() error = %v; want caller cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Analyze() did not preserve caller cancellation")
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func waitForManagerClosing(t *testing.T, manager *Manager) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		manager.mu.Lock()
		closing := manager.closing
		manager.mu.Unlock()
		if closing {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Close() did not enter closing state")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRemoveDoesNotDropActiveOrPendingJobs(t *testing.T) {
	manager := New(nil, nil)
	manager.all["active"] = &jobState{snap: JobSnapshot{ID: "active", Status: StatusActive}}
	manager.all["pending"] = &jobState{snap: JobSnapshot{ID: "pending", Status: StatusPending}}

	manager.Remove("active")
	manager.Remove("pending")

	if _, ok := manager.Find("active"); !ok {
		t.Fatal("Remove dropped an active job")
	}
	if _, ok := manager.Find("pending"); !ok {
		t.Fatal("Remove dropped a pending job")
	}
}

func TestClearTerminalRemovesCanceledJobAndEmitsEmptyQueue(t *testing.T) {
	events := make(chan Event, 1)
	manager := New(nil, func(event Event) { events <- event })
	manager.all["canceled-job"] = &jobState{snap: JobSnapshot{ID: "canceled-job", Status: StatusCanceled}}
	manager.ClearTerminal()
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("jobs after ClearTerminal = %#v; want empty", got)
	}
	select {
	case event := <-events:
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if event.Name != EventQueue || !strings.Contains(string(encoded), `"queue":[]`) {
			t.Fatalf("event = %s; want empty queue", encoded)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queue update")
	}
}

func TestCancelActiveTransitionsToCanceledThenClearTerminalRemovesIt(t *testing.T) {
	manager := New(nil, nil)
	started := make(chan struct{}, 1)
	manager.runDownload = func(ctx context.Context, _ engine.Request, _ engine.EventHandler) (engine.Result, error) {
		started <- struct{}{}
		<-ctx.Done()
		return engine.Result{}, ctx.Err()
	}
	id, err := manager.Submit(Request{URL: "https://example.invalid/active", OutputDir: t.TempDir(), Quality: Quality4K})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("active download did not start")
	}
	manager.mu.Lock()
	done := manager.all[id].done
	manager.mu.Unlock()
	manager.Cancel(id)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled download worker did not exit")
	}
	job, ok := manager.Find(id)
	if !ok {
		t.Fatal("canceled job disappeared before ClearTerminal")
	}
	if job.Status != StatusCanceled {
		t.Fatalf("status after Cancel = %q; want %q", job.Status, StatusCanceled)
	}
	manager.ClearTerminal()
	if _, ok := manager.Find(id); ok {
		t.Fatal("canceled job remains after ClearTerminal")
	}
}
