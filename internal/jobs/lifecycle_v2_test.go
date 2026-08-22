package jobs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tejasa97/vidstow/internal/jobmodel"
	"github.com/tejasa97/vidstow/internal/outputplan"
	"github.com/tejasa97/youtube_dlp/engine"
)

func TestQueueThumbnailURL(t *testing.T) {
	t.Run("keeps analyzed thumbnail", func(t *testing.T) {
		const thumbnail = "https://example.invalid/analyzed.jpg"
		if got := queueThumbnailURL(JobSnapshot{VideoID: "aqz-KE-bpKQ", Thumbnail: thumbnail}); got != thumbnail {
			t.Fatalf("queueThumbnailURL() = %q, want analyzed thumbnail %q", got, thumbnail)
		}
	})

	t.Run("derives restored YouTube thumbnail", func(t *testing.T) {
		const want = "https://i.ytimg.com/vi/aqz-KE-bpKQ/hqdefault.jpg"
		if got := queueThumbnailURL(JobSnapshot{VideoID: "aqz-KE-bpKQ"}); got != want {
			t.Fatalf("queueThumbnailURL() = %q, want %q", got, want)
		}
	})

	for _, videoID := range []string{"short", "tenletters", "twelveletter", "contains/slash", "contains?query", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		t.Run("rejects invalid "+videoID, func(t *testing.T) {
			if got := queueThumbnailURL(JobSnapshot{VideoID: videoID}); got != "" {
				t.Fatalf("queueThumbnailURL() = %q for invalid YouTube video ID", got)
			}
		})
	}
}

func TestRestoredStateV2QueueViewDerivesThumbnailURL(t *testing.T) {
	store, _, _ := newV2TestStore(t, "job-restored-thumbnail")
	store.state.Jobs[0].Lifecycle = jobmodel.LifecyclePaused
	store.state.Jobs[0].Desired = jobmodel.DesiredPaused
	store.state.Jobs[0].Request.VideoID = "aqz-KE-bpKQ"
	store.state.Jobs[0].Request.SourceURL = "https://www.youtube.com/watch?v=aqz-KE-bpKQ"

	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreStateV2(store.Snapshot()); err != nil {
		t.Fatal(err)
	}

	view := manager.QueueView()
	if len(view.Rows) != 1 {
		t.Fatalf("QueueView() rows = %d, want 1", len(view.Rows))
	}
	const want = "https://i.ytimg.com/vi/aqz-KE-bpKQ/hqdefault.jpg"
	if got := view.Rows[0].ThumbnailURL; got != want {
		t.Fatalf("restored QueueView thumbnail URL = %q, want %q", got, want)
	}
}

type v2MemoryStore struct {
	mu       sync.Mutex
	state    jobmodel.State
	failNext error
	calls    int
}

// blockingV2Store wraps v2MemoryStore with a one-shot gate on every durable
// transaction and records whether any transaction is attempted after the test
// marks the store closed. It proves the manager's bounded close waits for a
// detached cancelIdle goroutine instead of returning while that goroutine may
// still touch State v2.
type blockingV2Store struct {
	*v2MemoryStore
	block           func()
	mu              sync.Mutex
	closedAt        time.Time
	callsAfterClose int
}

func (s *blockingV2Store) Transaction(preconditions []jobmodel.JobPrecondition, mutate func(*jobmodel.State) error) error {
	s.mu.Lock()
	if !s.closedAt.IsZero() {
		s.callsAfterClose++
	}
	s.mu.Unlock()
	if s.block != nil {
		s.block()
	}
	return s.v2MemoryStore.Transaction(preconditions, mutate)
}

func (s *blockingV2Store) markClosed() {
	s.mu.Lock()
	s.closedAt = time.Now()
	s.mu.Unlock()
}

func (s *blockingV2Store) recordedAfterClose() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callsAfterClose
}

func (s *blockingV2Store) isMarkedClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closedAt.IsZero()
}

func (s *v2MemoryStore) Snapshot() jobmodel.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return jobmodel.CloneState(s.state)
}

func (s *v2MemoryStore) Transaction(preconditions []jobmodel.JobPrecondition, mutate func(*jobmodel.State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failNext != nil {
		err := s.failNext
		s.failNext = nil
		return err
	}
	next := jobmodel.CloneState(s.state)
	for _, want := range preconditions {
		found := false
		for _, job := range next.Jobs {
			if job.ID != want.ID {
				continue
			}
			found = true
			if job.Revision != want.Revision || job.Lifecycle != want.Lifecycle || (want.AttemptID != "" && job.AttemptID != want.AttemptID) || job.SessionID != want.SessionID || job.OutputRoot != want.OutputRoot {
				return errors.New("v2 test store: stale revision")
			}
		}
		if !found {
			return errors.New("v2 test store: missing row")
		}
	}
	if err := mutate(&next); err != nil {
		return err
	}
	next.StoreRevision++
	s.state = next
	return nil
}

func newV2TestStore(t *testing.T, ids ...string) (*v2MemoryStore, string, outputplan.Plan) {
	t.Helper()
	root := t.TempDir()
	plan := outputplan.Plan{
		ID: "video-1080-mp4", Kind: outputplan.KindVideo, Label: "1080p", Container: "MP4", Selector: "137+140",
	}
	now := time.Now().UTC()
	state := jobmodel.State{
		Version: jobmodel.StateVersion, NextQueueOrdinal: uint64(len(ids) + 1),
		Settings: jobmodel.Settings{DownloadConcurrency: 2},
		Jobs:     make([]jobmodel.DurableJob, 0, len(ids)), History: []jobmodel.HistoryEntry{}, Cleanup: []jobmodel.CleanupTombstone{},
	}
	for index, id := range ids {
		state.Jobs = append(state.Jobs, jobmodel.DurableJob{
			ID: id, Revision: 1, AttemptID: "attempt-" + id, SessionID: "0123456789abcdef0123456789abcde" + string('0'+byte(index)),
			QueueOrdinal: uint64(index + 1), Lifecycle: jobmodel.LifecyclePending, Phase: jobmodel.PhasePreparing, Desired: jobmodel.DesiredRunning,
			Request:     jobmodel.PersistedRequest{SourceURL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Demo", Channel: "Creator", Quality: "best", PlanID: plan.ID},
			Plan:        jobmodel.PersistedPlan{ID: plan.ID, Kind: string(plan.Kind), Label: plan.Label, Container: plan.Container, PrivateSelector: plan.Selector},
			OutputRoot:  jobmodel.OutputRootRef{CanonicalPath: root, Identity: "volume-test"},
			Reservation: jobmodel.ReservationSet{GroupID: id, Directory: jobmodel.OutputRootRef{CanonicalPath: root, Identity: "volume-test"}, Artifacts: []jobmodel.ReservedArtifact{{Kind: string(engine.ArtifactKindPrimary), Identity: "primary", Basename: "Demo [abc123] [1080p].mp4"}}},
			RetryMode:   jobmodel.RetryModeNone, CreatedAt: now, UpdatedAt: now,
		})
	}
	return &v2MemoryStore{state: state}, root, plan
}

func waitForV2Job(t *testing.T, store *v2MemoryStore, id string, want jobmodel.Lifecycle) jobmodel.DurableJob {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, job := range store.Snapshot().Jobs {
			if job.ID == id && job.Lifecycle == want {
				return job
			}
		}
		time.Sleep(time.Millisecond)
	}
	for _, job := range store.Snapshot().Jobs {
		if job.ID == id {
			t.Fatalf("job %s lifecycle = %s; want %s", id, job.Lifecycle, want)
		}
	}
	t.Fatalf("job %s missing", id)
	return jobmodel.DurableJob{}
}

func TestV2PauseKeepsSlotUntilRunnerExitAndUsesAttemptSession(t *testing.T) {
	store, root, plan := newV2TestStore(t, "job-one", "job-two")
	manager := New(nil, nil)
	defer manager.Close()
	manager.SetConcurrency(1)
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	started := make(chan engine.Request, 2)
	release := make(chan struct{})
	manager.runDownload = func(ctx context.Context, request engine.Request, _ engine.EventHandler) (engine.Result, error) {
		started <- request
		<-ctx.Done()
		<-release
		return engine.Result{}, ctx.Err()
	}

	firstPlan := plan
	if _, err := manager.SubmitAdmitted("job-one", Request{URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Demo", PlanID: plan.ID, OutputDir: root}, &firstPlan, AdmittedOutput{Basename: "Demo [abc123] [1080p].mp4"}); err != nil {
		t.Fatal(err)
	}
	request := <-started
	if request.Overwrite || request.Filesystem.Resume.SessionID == "" || len(request.Filesystem.Resume.CommitTargets) != 1 {
		t.Fatalf("session request = %#v; want no-replace session target", request)
	}
	active := waitForV2Job(t, store, "job-one", jobmodel.LifecycleActive)
	if active.AttemptID == "" || active.SessionID != request.Filesystem.Resume.SessionID {
		t.Fatalf("active identity = %#v; request session = %q", active, request.Filesystem.Resume.SessionID)
	}
	if _, err := manager.SubmitAdmitted("job-two", Request{URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Demo", PlanID: plan.ID, OutputDir: root}, &firstPlan, AdmittedOutput{Basename: "Demo [abc123] [1080p].mp4"}); err != nil {
		t.Fatal(err)
	}
	manager.Cancel("job-one")
	select {
	case <-started:
		t.Fatal("second FIFO runner started before first runner exited")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	waitForV2Job(t, store, "job-one", jobmodel.LifecycleCanceled)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("second FIFO runner did not start after first exit")
	}
	if got := manager.Active(); got != "job-two" {
		t.Fatalf("active job = %q; want job-two", got)
	}
}

func TestV2ShutdownHonorsSharedDeadlineAndLeavesPausingEvidence(t *testing.T) {
	store, root, plan := newV2TestStore(t, "job-shutdown")
	manager := New(nil, nil)
	manager.SetConcurrency(1)
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	manager.runDownload = func(ctx context.Context, _ engine.Request, _ engine.EventHandler) (engine.Result, error) {
		close(started)
		<-release
		return engine.Result{}, ctx.Err()
	}
	if _, err := manager.SubmitAdmitted("job-shutdown", Request{URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Demo", PlanID: plan.ID, OutputDir: root}, &plan, AdmittedOutput{Basename: "Demo [abc123] [1080p].mp4"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("shutdown test worker did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := manager.Shutdown(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v; want shared deadline", err)
	}
	job := store.Snapshot().Jobs[0]
	if job.Lifecycle != jobmodel.LifecyclePausing || job.Desired != jobmodel.DesiredPaused {
		t.Fatalf("durable shutdown evidence = %#v; want pausing/paused", job)
	}

	close(release)
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := manager.Close(closeCtx); err != nil {
		t.Fatalf("Close() after releasing worker = %v", err)
	}
}

func TestV2CloseDeadlineLeavesStateOpenUntilBlockedWorkerDrains(t *testing.T) {
	store, root, plan := newV2TestStore(t, "job-close-deadline")
	blocking := &blockingV2Store{v2MemoryStore: store}
	manager := New(nil, nil)
	manager.SetConcurrency(1)
	if err := manager.SetStateStore(blocking); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	manager.runDownload = func(ctx context.Context, _ engine.Request, _ engine.EventHandler) (engine.Result, error) {
		close(started)
		<-release
		return engine.Result{}, ctx.Err()
	}
	if _, err := manager.SubmitAdmitted("job-close-deadline", Request{URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Demo", PlanID: plan.ID, OutputDir: root}, &plan, AdmittedOutput{Basename: "Demo [abc123] [1080p].mp4"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("deadline test worker did not start")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- manager.Close(closeCtx) }()
	select {
	case err := <-returned:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close() error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not return at its deadline")
	}

	if err := manager.Close(closeCtx); err == nil {
		blocking.markClosed()
	}
	if blocking.isMarkedClosed() {
		t.Fatal("State v2 was marked closed while the worker was still blocked")
	}

	close(release)
	deadline := time.Now().Add(time.Second)
	for manager.Active() != "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if active := manager.Active(); active != "" {
		t.Fatalf("worker remained active after release: %q", active)
	}
	blocking.markClosed()
	time.Sleep(20 * time.Millisecond)
	if got := blocking.recordedAfterClose(); got != 0 {
		t.Fatalf("store transactions after safe close point = %d; want 0", got)
	}
}

func TestV2CancelPendingThenCloseWaitsForDetachedGoroutine(t *testing.T) {
	store, root, _ := newV2TestStore(t, "job-pending")
	blocking := &blockingV2Store{v2MemoryStore: store}
	manager := New(nil, nil)
	if err := manager.SetStateStore(blocking); err != nil {
		t.Fatal(err)
	}

	durable := store.Snapshot().Jobs[0]
	state := &jobState{
		snap: JobSnapshot{
			ID: "job-pending", Status: StatusPending, URL: durable.Request.SourceURL,
			VideoID: durable.Request.VideoID, Title: durable.Request.Title,
			PlanID: durable.Plan.ID, OutputDir: root,
		},
		durable:     durable,
		fromStateV2: true,
		done:        make(chan struct{}),
	}
	manager.mu.Lock()
	manager.all["job-pending"] = state
	manager.mu.Unlock()

	entered := make(chan struct{})
	release := make(chan struct{})
	blocking.block = func() {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	}

	manager.Cancel("job-pending")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("detached cancelIdle did not enter its durable transition")
	}

	closed := make(chan error, 1)
	go func() { closed <- manager.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned while detached cancelIdle was blocked: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wait for the detached cancelIdle goroutine")
	}

	blocking.markClosed()
	time.Sleep(20 * time.Millisecond)
	if got := blocking.recordedAfterClose(); got != 0 {
		t.Fatalf("store transactions after Close returned = %d; want 0 (no State use after close)", got)
	}
}

func TestV2CompletionAndHistoryShareOneTerminalTransaction(t *testing.T) {
	store, root, plan := newV2TestStore(t, "job-complete")
	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	manager.runDownload = func(context.Context, engine.Request, engine.EventHandler) (engine.Result, error) {
		return engine.Result{Filename: filepath.Join(root, "Demo [abc123] [1080p].mp4"), Bytes: 42}, nil
	}
	if _, err := manager.SubmitAdmitted("job-complete", Request{URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Demo", PlanID: plan.ID, OutputDir: root}, &plan, AdmittedOutput{Basename: "Demo [abc123] [1080p].mp4"}); err != nil {
		t.Fatal(err)
	}
	job := waitForV2Job(t, store, "job-complete", jobmodel.LifecycleCompleted)
	state := store.Snapshot()
	if len(state.History) != 1 || state.History[0].ID != job.ID || state.History[0].SizeBytes != 42 {
		t.Fatalf("State v2 terminal image = %#v; want one completion/history image", state)
	}
	if job.Revision < 3 {
		t.Fatalf("completed revision = %d; want admission, active, and terminal transitions", job.Revision)
	}
}

func TestV2FailedRetryWithoutAuthoritativeWorkspaceRequiresAction(t *testing.T) {
	store, root, plan := newV2TestStore(t, "job-failed")
	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	attempts := make(chan string, 2)
	manager.runDownload = func(_ context.Context, request engine.Request, _ engine.EventHandler) (engine.Result, error) {
		attempts <- request.Filesystem.Resume.SessionID
		return engine.Result{}, errors.New("network failed before the injected runner created a workspace")
	}
	if _, err := manager.SubmitAdmitted("job-failed", Request{URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Demo", PlanID: plan.ID, OutputDir: root}, &plan, AdmittedOutput{Basename: "Demo [abc123] [1080p].mp4"}); err != nil {
		t.Fatal(err)
	}
	firstSession := <-attempts
	first := waitForV2Job(t, store, "job-failed", jobmodel.LifecycleFailed)
	if err := manager.Retry("job-failed"); err != nil {
		t.Fatal(err)
	}
	second := waitForV2Job(t, store, "job-failed", jobmodel.LifecycleActionRequired)
	if first.ID != second.ID || first.AttemptID != second.AttemptID || first.SessionID != second.SessionID || second.SessionID != firstSession {
		t.Fatalf("unavailable retry rotated authority: first=%#v second=%#v runner session=%q", first, second, firstSession)
	}
	select {
	case sessionID := <-attempts:
		t.Fatalf("unavailable retry started a second session %q", sessionID)
	default:
	}
}

func TestV2RetryReusesOnlyAvailableManifest(t *testing.T) {
	store, root, _ := newV2TestStore(t, "job-reuse")
	store.state.Jobs[0].Lifecycle = jobmodel.LifecycleFailed
	store.state.Jobs[0].Desired = jobmodel.DesiredRunning
	original := store.state.Jobs[0]

	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreStateV2(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	manager.inspectResume = func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
		return engine.ResumeSummary{HasManifest: true, Classification: "available"}, nil
	}
	requests := make(chan engine.Request, 1)
	manager.runDownload = func(_ context.Context, request engine.Request, _ engine.EventHandler) (engine.Result, error) {
		requests <- request
		return engine.Result{Filename: filepath.Join(root, "Demo [abc123] [1080p].mp4")}, nil
	}

	if err := manager.Retry("job-reuse"); err != nil {
		t.Fatal(err)
	}
	request := <-requests
	if request.Filesystem.Resume.SessionID != original.SessionID {
		t.Fatalf("retry session = %q; want validated session %q", request.Filesystem.Resume.SessionID, original.SessionID)
	}
	completed := waitForV2Job(t, store, "job-reuse", jobmodel.LifecycleCompleted)
	if completed.AttemptID == original.AttemptID || completed.RetryMode != jobmodel.RetryModeResumeValidated {
		t.Fatalf("validated retry identity/mode = %#v; want new attempt and resume-validated", completed)
	}
}

func TestPreTransferRestartRequiresChallengeMarkerAndNoEvidence(t *testing.T) {
	base := &jobState{
		snap:    JobSnapshot{},
		durable: jobmodel.DurableJob{LastErrorCode: retryCodeYouTubeChallengePreTransfer},
	}
	if !canRestartPreTransferFailure(base, engine.ResumeSummary{}) {
		t.Fatal("empty challenge-timeout session was not restartable")
	}
	tests := []struct {
		name    string
		mutate  func(*jobState)
		summary engine.ResumeSummary
	}{
		{name: "media bytes observed", mutate: func(state *jobState) { state.snap.Bytes = 1 }},
		{name: "different failure", mutate: func(state *jobState) { state.durable.LastErrorCode = "network" }},
		{name: "manifest exists", summary: engine.ResumeSummary{HasManifest: true, Classification: "available"}},
		{name: "unavailable root", summary: engine.ResumeSummary{Classification: "unavailable_root"}},
		{name: "lease contention", summary: engine.ResumeSummary{LeaseContended: true}},
		{name: "publication uncertainty", summary: engine.ResumeSummary{Publication: "indeterminate"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &jobState{snap: base.snap, durable: base.durable}
			if test.mutate != nil {
				test.mutate(state)
			}
			if canRestartPreTransferFailure(state, test.summary) {
				t.Fatal("unsafe evidence was authorized for a fresh retry")
			}
		})
	}
}

func TestV2RetryStartsFreshSessionAfterChallengeFailedBeforeTransfer(t *testing.T) {
	store, root, _ := newV2TestStore(t, "job-challenge")
	store.state.Jobs[0].Lifecycle = jobmodel.LifecycleFailed
	store.state.Jobs[0].Desired = jobmodel.DesiredRunning
	store.state.Jobs[0].LastErrorCode = retryCodeYouTubeChallengePreTransfer
	original := store.state.Jobs[0]

	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreStateV2(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	manager.inspectResume = func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
		return engine.ResumeSummary{}, errors.New("workspace unavailable")
	}
	requests := make(chan engine.Request, 1)
	manager.runDownload = func(_ context.Context, request engine.Request, _ engine.EventHandler) (engine.Result, error) {
		requests <- request
		return engine.Result{Filename: filepath.Join(root, "Demo [abc123] [1080p].mp4")}, nil
	}

	if err := manager.Retry("job-challenge"); err != nil {
		t.Fatal(err)
	}
	request := <-requests
	if request.Filesystem.Resume.SessionID == original.SessionID || request.Filesystem.Resume.SessionID == "" {
		t.Fatalf("retry session = %q; want fresh identity distinct from %q", request.Filesystem.Resume.SessionID, original.SessionID)
	}
	completed := waitForV2Job(t, store, "job-challenge", jobmodel.LifecycleCompleted)
	if completed.AttemptID == original.AttemptID || completed.RetryMode != jobmodel.RetryModeRestartNewSession {
		t.Fatalf("challenge retry identity/mode = %#v; want new attempt and restart-new-session", completed)
	}
}

func TestV2RetryPreservesUncertainSessionAsActionRequired(t *testing.T) {
	tests := []struct {
		name    string
		summary engine.ResumeSummary
		err     error
		code    string
	}{
		{name: "inspection error", err: errors.New("inspection unavailable"), code: "recovery-session-unavailable"},
		{name: "unclassified missing manifest", summary: engine.ResumeSummary{}, code: "session-reconciliation-required"},
		{name: "unavailable root", summary: engine.ResumeSummary{Classification: "unavailable_root"}, code: "recovery-session-unavailable"},
		{name: "corrupt manifest", summary: engine.ResumeSummary{Classification: "corrupt_manifest"}, code: "session-manifest-corrupt"},
		{name: "unknown manifest", summary: engine.ResumeSummary{Classification: "unknown_manifest_version"}, code: "session-version-unknown"},
		{name: "unsafe path", summary: engine.ResumeSummary{Classification: "unsafe_path"}, code: "session-path-unsafe"},
		{name: "lease contention", summary: engine.ResumeSummary{LeaseContended: true, Classification: "lease_contention"}, code: "session-lease-contended"},
		{name: "manifest commit indeterminate", summary: engine.ResumeSummary{Classification: "manifest_commit_indeterminate"}, code: "publication-reconciliation-required"},
		{name: "publication indeterminate", summary: engine.ResumeSummary{HasManifest: true, Classification: "available", Publication: "indeterminate"}, code: "publication-reconciliation-required"},
		{name: "publication already committed", summary: engine.ResumeSummary{HasManifest: true, Classification: "available", Publication: "committed"}, code: "publication-reconciliation-required"},
		{name: "discard pending", summary: engine.ResumeSummary{Classification: "discard_pending"}, code: "session-reconciliation-required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, _, _ := newV2TestStore(t, "job-uncertain")
			store.state.Jobs[0].Lifecycle = jobmodel.LifecycleFailed
			store.state.Jobs[0].Desired = jobmodel.DesiredRunning
			original := store.state.Jobs[0]

			manager := New(nil, nil)
			defer manager.Close()
			if err := manager.SetStateStore(store); err != nil {
				t.Fatal(err)
			}
			if err := manager.RestoreStateV2(store.Snapshot()); err != nil {
				t.Fatal(err)
			}
			manager.inspectResume = func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
				return test.summary, test.err
			}
			runs := 0
			manager.runDownload = func(context.Context, engine.Request, engine.EventHandler) (engine.Result, error) {
				runs++
				return engine.Result{}, nil
			}

			if err := manager.Retry("job-uncertain"); err != nil {
				t.Fatalf("Retry() = %v; want accepted Action-required transition", err)
			}
			if runs != 0 {
				t.Fatalf("uncertain retry started %d runners; want 0", runs)
			}
			after := store.Snapshot().Jobs[0]
			if after.Lifecycle != jobmodel.LifecycleActionRequired || after.Desired != jobmodel.DesiredPaused || after.ActionRequiredCode != test.code || after.LastErrorCode != test.code {
				t.Fatalf("uncertain retry durable result = %#v; want Action required code %q", after, test.code)
			}
			if after.SessionID != original.SessionID || after.AttemptID != original.AttemptID || after.QueueOrdinal != original.QueueOrdinal {
				t.Fatalf("uncertain retry rotated authority: before=%#v after=%#v", original, after)
			}
			snapshot, ok := manager.Find("job-uncertain")
			if !ok || snapshot.Status != StatusActionRequired || snapshot.ErrorReason != test.code {
				t.Fatalf("uncertain retry runtime result = %#v, %v; want Action required", snapshot, ok)
			}
		})
	}
}

// TestV2ZeroProgressResumeEscalatesToFreshSession reproduces the stale signed
// URL trap end to end: a mid-transfer failure resumes once, and when that
// resume commits no new bytes the next retry discards the dead session and
// restarts with a fresh one instead of pinning the same URL forever.
func TestV2ZeroProgressResumeEscalatesToFreshSession(t *testing.T) {
	store, root, plan := newV2TestStore(t, "job-zero-progress")
	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	var committed int64 = 100
	manager.inspectResume = func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
		return engine.ResumeSummary{
			HasManifest: true, Classification: "available",
			Components: []engine.ResumeComponent{{ID: "video", Kind: "video", CommittedBytes: committed}},
		}, nil
	}
	sessions := make(chan string, 3)
	runs := 0
	manager.runDownload = func(_ context.Context, request engine.Request, _ engine.EventHandler) (engine.Result, error) {
		runs++
		sessions <- request.Filesystem.Resume.SessionID
		if runs <= 2 {
			return engine.Result{}, errors.New("connection reset by peer")
		}
		return engine.Result{Filename: filepath.Join(root, "Demo [abc123] [1080p].mp4")}, nil
	}
	if _, err := manager.SubmitAdmitted("job-zero-progress", Request{URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Demo", PlanID: plan.ID, OutputDir: root}, &plan, AdmittedOutput{Basename: "Demo [abc123] [1080p].mp4"}); err != nil {
		t.Fatal(err)
	}
	firstSession := <-sessions
	first := waitForV2Job(t, store, "job-zero-progress", jobmodel.LifecycleFailed)
	if first.LastFailureCommittedBytes != committed || first.ZeroProgressResumes != 0 {
		t.Fatalf("first failure bookkeeping = %d/%d; want committed %d and no zero-progress mark", first.LastFailureCommittedBytes, first.ZeroProgressResumes, committed)
	}
	if first.SessionID != firstSession {
		t.Fatalf("first failure session = %q; want runner session %q", first.SessionID, firstSession)
	}

	// The first retry of a mid-transfer failure still resumes: the saved URL
	// may still serve ranges.
	if err := manager.Retry("job-zero-progress"); err != nil {
		t.Fatal(err)
	}
	if resumed := <-sessions; resumed != firstSession {
		t.Fatalf("resume attempt session = %q; want validated session %q", resumed, firstSession)
	}
	second := waitForV2Job(t, store, "job-zero-progress", jobmodel.LifecycleFailed)
	if second.RetryMode != jobmodel.RetryModeResumeValidated {
		t.Fatalf("resume attempt retry mode = %s; want resume-validated", second.RetryMode)
	}
	if second.ZeroProgressResumes != 1 || second.LastFailureCommittedBytes != committed {
		t.Fatalf("zero-progress bookkeeping after stuck resume = %d/%d; want 1/%d", second.ZeroProgressResumes, second.LastFailureCommittedBytes, committed)
	}

	// The checkpoint never moved, so the next retry must not resume the dead
	// session again.
	if err := manager.Retry("job-zero-progress"); err != nil {
		t.Fatal(err)
	}
	restarted := <-sessions
	if restarted == firstSession || restarted == "" {
		t.Fatalf("escalated retry session = %q; want fresh identity distinct from %q", restarted, firstSession)
	}
	completed := waitForV2Job(t, store, "job-zero-progress", jobmodel.LifecycleCompleted)
	if completed.SessionID != restarted || completed.RetryMode != jobmodel.RetryModeRestartNewSession {
		t.Fatalf("escalated retry identity/mode = %q/%s; want fresh session and restart-new-session", completed.SessionID, completed.RetryMode)
	}
	if completed.SessionRestarts != 1 || completed.ZeroProgressResumes != 0 || completed.LastFailureCommittedBytes != 0 {
		t.Fatalf("escalation bookkeeping = restarts %d, zero-progress %d, committed %d; want 1/0/0", completed.SessionRestarts, completed.ZeroProgressResumes, completed.LastFailureCommittedBytes)
	}
}

// TestV2ProgressMakingResumeStillResumes pins the flip side of escalation: a
// resume attempt that committed new bytes before failing again must keep
// resuming, because the saved link demonstrably still serves ranges.
func TestV2ProgressMakingResumeStillResumes(t *testing.T) {
	store, root, _ := newV2TestStore(t, "job-progress")
	store.state.Jobs[0].Lifecycle = jobmodel.LifecycleFailed
	store.state.Jobs[0].Desired = jobmodel.DesiredRunning
	store.state.Jobs[0].LastFailureCommittedBytes = 100
	store.state.Jobs[0].ZeroProgressResumes = 0
	// The budget is already spent: a resume that committed new bytes must
	// still resume rather than decline on the restart count alone.
	store.state.Jobs[0].SessionRestarts = maxZeroProgressRestarts
	original := store.state.Jobs[0]

	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreStateV2(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	manager.inspectResume = func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
		return engine.ResumeSummary{
			HasManifest: true, Classification: "available",
			Components: []engine.ResumeComponent{{ID: "video", Kind: "video", CommittedBytes: 150}},
		}, nil
	}
	requests := make(chan engine.Request, 1)
	manager.runDownload = func(_ context.Context, request engine.Request, _ engine.EventHandler) (engine.Result, error) {
		requests <- request
		return engine.Result{Filename: filepath.Join(root, "Demo [abc123] [1080p].mp4")}, nil
	}

	if err := manager.Retry("job-progress"); err != nil {
		t.Fatal(err)
	}
	request := <-requests
	if request.Filesystem.Resume.SessionID != original.SessionID {
		t.Fatalf("progress-making retry session = %q; want validated session %q", request.Filesystem.Resume.SessionID, original.SessionID)
	}
	completed := waitForV2Job(t, store, "job-progress", jobmodel.LifecycleCompleted)
	if completed.RetryMode != jobmodel.RetryModeResumeValidated || completed.SessionRestarts != maxZeroProgressRestarts {
		t.Fatalf("progress-making retry mode/restarts = %s/%d; want resume-validated and an unchanged budget", completed.RetryMode, completed.SessionRestarts)
	}
}

// TestV2ExpiredMediaLinkFailureRestartsImmediately covers the deterministic
// signal: a download-phase HTTP 403 means the captured signed URL is dead, so
// retry must not spend an attempt resuming it.
func TestV2ExpiredMediaLinkFailureRestartsImmediately(t *testing.T) {
	store, root, _ := newV2TestStore(t, "job-expired-link")
	store.state.Jobs[0].Lifecycle = jobmodel.LifecycleFailed
	store.state.Jobs[0].Desired = jobmodel.DesiredRunning
	store.state.Jobs[0].LastErrorCode = retryCodeMediaLinkExpired
	original := store.state.Jobs[0]

	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreStateV2(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	manager.inspectResume = func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
		return engine.ResumeSummary{
			HasManifest: true, Classification: "available",
			Components: []engine.ResumeComponent{{ID: "video", Kind: "video", CommittedBytes: 3145728}},
		}, nil
	}
	requests := make(chan engine.Request, 1)
	manager.runDownload = func(_ context.Context, request engine.Request, _ engine.EventHandler) (engine.Result, error) {
		requests <- request
		return engine.Result{Filename: filepath.Join(root, "Demo [abc123] [1080p].mp4")}, nil
	}

	if err := manager.Retry("job-expired-link"); err != nil {
		t.Fatal(err)
	}
	request := <-requests
	if request.Filesystem.Resume.SessionID == original.SessionID || request.Filesystem.Resume.SessionID == "" {
		t.Fatalf("expired-link retry session = %q; want fresh identity distinct from %q", request.Filesystem.Resume.SessionID, original.SessionID)
	}
	completed := waitForV2Job(t, store, "job-expired-link", jobmodel.LifecycleCompleted)
	if completed.RetryMode != jobmodel.RetryModeRestartNewSession || completed.SessionRestarts != 1 {
		t.Fatalf("expired-link retry mode/restarts = %s/%d; want restart-new-session and 1 restart", completed.RetryMode, completed.SessionRestarts)
	}
}

// TestV2ZeroProgressEscalationIsBounded verifies retry stops auto-escalating
// after the restart budget is spent and tells the user to download the item
// again from Home instead of looping through dead sessions forever.
func TestV2ZeroProgressEscalationIsBounded(t *testing.T) {
	store, _, _ := newV2TestStore(t, "job-escalation-spent")
	store.state.Jobs[0].Lifecycle = jobmodel.LifecycleFailed
	store.state.Jobs[0].Desired = jobmodel.DesiredRunning
	store.state.Jobs[0].LastErrorCode = retryCodeMediaLinkExpired
	store.state.Jobs[0].LastFailureCommittedBytes = 3145728
	store.state.Jobs[0].ZeroProgressResumes = 1
	store.state.Jobs[0].SessionRestarts = maxZeroProgressRestarts
	original := store.state.Jobs[0]

	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreStateV2(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	manager.inspectResume = func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
		return engine.ResumeSummary{
			HasManifest: true, Classification: "available",
			Components: []engine.ResumeComponent{{ID: "video", Kind: "video", CommittedBytes: 3145728}},
		}, nil
	}
	runs := 0
	manager.runDownload = func(context.Context, engine.Request, engine.EventHandler) (engine.Result, error) {
		runs++
		return engine.Result{}, nil
	}

	if err := manager.Retry("job-escalation-spent"); err == nil {
		t.Fatal("Retry() beyond the restart budget succeeded; want decline")
	}
	if runs != 0 {
		t.Fatalf("declined retry started %d runners; want 0", runs)
	}
	after := store.Snapshot().Jobs[0]
	if after.Lifecycle != jobmodel.LifecycleFailed || after.SessionID != original.SessionID || after.LastErrorCode != retryCodeFreshDownloadRequired {
		t.Fatalf("declined retry durable result = %#v; want failed row with fresh-download guidance", after)
	}
	snapshot, ok := manager.Find("job-escalation-spent")
	if !ok || snapshot.Status != StatusFailed || snapshot.Message != freshDownloadRequiredNotice || snapshot.ErrorReason != retryCodeFreshDownloadRequired {
		t.Fatalf("declined retry runtime result = %#v, %v; want guidance copy on the failed row", snapshot, ok)
	}

	// Restoring the declined row must keep the guidance copy.
	restored := New(nil, nil)
	defer restored.Close()
	if err := restored.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	if err := restored.RestoreStateV2(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if snapshot, ok := restored.Find("job-escalation-spent"); !ok || snapshot.Message != freshDownloadRequiredNotice {
		t.Fatalf("restored declined row message = %q, %v; want persisted guidance copy", snapshot.Message, ok)
	}

	view := manager.QueueView()
	if len(view.Rows) != 1 || view.Rows[0].Capabilities.Retry || !view.Rows[0].Capabilities.Remove {
		t.Fatalf("exhausted row capabilities = %#v; want Retry hidden and Remove kept", view.Rows)
	}
	if err := manager.Retry("job-escalation-spent"); err == nil {
		t.Fatal("repeated Retry() after exhaustion succeeded; want decline")
	}
	if runs != 0 {
		t.Fatalf("repeated declined retry started %d runners; want 0", runs)
	}
}

// TestV2BudgetSpentTransientFailureStillResumes pins the reviewer's
// correction: the restart count alone must not exhaust a row. When the final
// fresh session has committed new bytes and then failed transiently, Retry
// must offer a validated resume, not a decline.
func TestV2BudgetSpentTransientFailureStillResumes(t *testing.T) {
	store, root, _ := newV2TestStore(t, "job-progress-at-cap")
	store.state.Jobs[0].Lifecycle = jobmodel.LifecycleFailed
	store.state.Jobs[0].Desired = jobmodel.DesiredRunning
	store.state.Jobs[0].LastErrorCode = "internal"
	store.state.Jobs[0].LastFailureCommittedBytes = 150
	store.state.Jobs[0].ZeroProgressResumes = 0
	store.state.Jobs[0].SessionRestarts = maxZeroProgressRestarts
	original := store.state.Jobs[0]

	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreStateV2(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	manager.inspectResume = func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
		return engine.ResumeSummary{
			HasManifest: true, Classification: "available",
			Components: []engine.ResumeComponent{{ID: "video", Kind: "video", CommittedBytes: 150}},
		}, nil
	}
	requests := make(chan engine.Request, 1)
	manager.runDownload = func(_ context.Context, request engine.Request, _ engine.EventHandler) (engine.Result, error) {
		requests <- request
		return engine.Result{Filename: filepath.Join(root, "Demo [abc123] [1080p].mp4")}, nil
	}

	view := manager.QueueView()
	if len(view.Rows) != 1 || !view.Rows[0].Capabilities.Retry {
		t.Fatalf("budget-spent unexhausted capabilities = %#v; want Retry still offered", view.Rows)
	}
	if err := manager.Retry("job-progress-at-cap"); err != nil {
		t.Fatalf("Retry() of a transient failure at the restart cap failed: %v", err)
	}
	request := <-requests
	if request.Filesystem.Resume.SessionID != original.SessionID {
		t.Fatalf("transient retry session = %q; want validated session %q", request.Filesystem.Resume.SessionID, original.SessionID)
	}
	completed := waitForV2Job(t, store, "job-progress-at-cap", jobmodel.LifecycleCompleted)
	if completed.RetryMode != jobmodel.RetryModeResumeValidated || completed.SessionRestarts != maxZeroProgressRestarts {
		t.Fatalf("transient retry mode/restarts = %s/%d; want resume-validated and an unchanged budget", completed.RetryMode, completed.SessionRestarts)
	}
}

// TestV2ExpiredMediaLinkAtRestartCapSettlesGuidance covers the last restart:
// a 403 after SessionRestarts reaches the cap must persist the exhausted
// marker and copy before any further click.
func TestV2ExpiredMediaLinkAtRestartCapSettlesGuidance(t *testing.T) {
	store, _, _ := newV2TestStore(t, "job-cap-copy")
	store.state.Jobs[0].Lifecycle = jobmodel.LifecycleFailed
	store.state.Jobs[0].Desired = jobmodel.DesiredRunning
	store.state.Jobs[0].LastErrorCode = retryCodeMediaLinkExpired
	store.state.Jobs[0].LastFailureCommittedBytes = 3145728
	store.state.Jobs[0].SessionRestarts = maxZeroProgressRestarts - 1
	original := store.state.Jobs[0]

	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreStateV2(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	manager.inspectResume = func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
		return engine.ResumeSummary{
			HasManifest: true, Classification: "available",
			Components: []engine.ResumeComponent{{ID: "video", Kind: "video", CommittedBytes: 3145728}},
		}, nil
	}
	runs := 0
	manager.runDownload = func(_ context.Context, request engine.Request, _ engine.EventHandler) (engine.Result, error) {
		runs++
		if request.Filesystem.Resume.SessionID == original.SessionID {
			t.Error("at-cap restart reused the retired session")
		}
		return engine.Result{}, fmt.Errorf("multi-track transfer: %w", &engine.DownloadHTTPStatusError{Code: http.StatusForbidden})
	}

	if err := manager.Retry("job-cap-copy"); err != nil {
		t.Fatal(err)
	}
	failed := waitForV2Job(t, store, "job-cap-copy", jobmodel.LifecycleFailed)
	if failed.SessionRestarts != maxZeroProgressRestarts || failed.LastErrorCode != retryCodeFreshDownloadRequired {
		t.Fatalf("at-cap 403 settle = restarts %d code %q; want %d/%s", failed.SessionRestarts, failed.LastErrorCode, maxZeroProgressRestarts, retryCodeFreshDownloadRequired)
	}
	snapshot, ok := manager.Find("job-cap-copy")
	if !ok || snapshot.Message != freshDownloadRequiredNotice || snapshot.ErrorReason != retryCodeFreshDownloadRequired {
		t.Fatalf("at-cap 403 runtime copy = %#v, %v; want exhausted notice before the next click", snapshot, ok)
	}
	if runs != 1 {
		t.Fatalf("at-cap restart started %d runners; want 1", runs)
	}
	view := manager.QueueView()
	if len(view.Rows) != 1 || view.Rows[0].Capabilities.Retry {
		t.Fatalf("at-cap 403 capabilities = %#v; want Retry hidden", view.Rows)
	}
	if err := manager.Retry("job-cap-copy"); err == nil {
		t.Fatal("Retry() after at-cap settle succeeded; want decline")
	}
	if runs != 1 {
		t.Fatalf("Retry after at-cap settle started extra runners (%d)", runs)
	}
}

// TestV2MidTransferAtRestartCapSettlesGuidance is the non-403 twin of the
// at-cap copy test, as an honest ladder: the final restart's first failure
// moved the checkpoint, so its row still promises a resume; only the follow-up
// resume that freezes the checkpoint again settles the exhausted marker.
func TestV2MidTransferAtRestartCapSettlesGuidance(t *testing.T) {
	store, _, _ := newV2TestStore(t, "job-cap-mid")
	store.state.Jobs[0].Lifecycle = jobmodel.LifecycleFailed
	store.state.Jobs[0].Desired = jobmodel.DesiredRunning
	store.state.Jobs[0].LastFailureCommittedBytes = 100
	store.state.Jobs[0].ZeroProgressResumes = 1
	store.state.Jobs[0].SessionRestarts = maxZeroProgressRestarts - 1

	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreStateV2(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	var committed int64 = 100
	manager.inspectResume = func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
		return engine.ResumeSummary{
			HasManifest: true, Classification: "available",
			Components: []engine.ResumeComponent{{ID: "video", Kind: "video", CommittedBytes: committed}},
		}, nil
	}
	sessions := make(chan string, 2)
	runs := 0
	manager.runDownload = func(_ context.Context, request engine.Request, _ engine.EventHandler) (engine.Result, error) {
		runs++
		sessions <- request.Filesystem.Resume.SessionID
		return engine.Result{}, errors.New("connection reset by peer")
	}

	// The pre-restart session is zero-progress, so this retry spends the last
	// restart instead of resuming it.
	if err := manager.Retry("job-cap-mid"); err != nil {
		t.Fatal(err)
	}
	restarted := <-sessions
	first := waitForV2Job(t, store, "job-cap-mid", jobmodel.LifecycleFailed)
	if first.SessionRestarts != maxZeroProgressRestarts || first.LastErrorCode == retryCodeFreshDownloadRequired {
		t.Fatalf("first failure of final restart settled exhausted: %#v", first)
	}
	if snapshot, ok := manager.Find("job-cap-mid"); !ok || snapshot.Message != downloadStoppedMidMessage {
		t.Fatalf("first failure of final restart copy = %#v, %v; want honest mid-transfer message", snapshot, ok)
	}

	// The restarted session's first failure recorded a fresh checkpoint, so
	// this retry is a validated resume of the same session.
	if err := manager.Retry("job-cap-mid"); err != nil {
		t.Fatal(err)
	}
	if resumed := <-sessions; resumed != restarted {
		t.Fatalf("budget-spent resume session = %q; want the restarted session %q", resumed, restarted)
	}
	failed := waitForV2Job(t, store, "job-cap-mid", jobmodel.LifecycleFailed)
	if failed.LastErrorCode != retryCodeFreshDownloadRequired {
		t.Fatalf("frozen follow-up resume settle code = %q; want %s", failed.LastErrorCode, retryCodeFreshDownloadRequired)
	}
	snapshot, ok := manager.Find("job-cap-mid")
	if !ok || snapshot.Message != freshDownloadRequiredNotice {
		t.Fatalf("frozen follow-up resume copy = %#v, %v; want exhausted notice", snapshot, ok)
	}
	view := manager.QueueView()
	if len(view.Rows) != 1 || view.Rows[0].Capabilities.Retry {
		t.Fatalf("settled exhausted capabilities = %#v; want Retry hidden", view.Rows)
	}
	if err := manager.Retry("job-cap-mid"); err == nil {
		t.Fatal("Retry() after at-cap settle succeeded; want decline")
	}
	if runs != 2 {
		t.Fatalf("post-exhaustion ladder started %d runners; want 2", runs)
	}
}

func TestShouldSettleFreshDownloadRequired(t *testing.T) {
	expired := fmt.Errorf("multi-track transfer: %w", &engine.DownloadHTTPStatusError{Code: http.StatusForbidden})
	reset := errors.New("connection reset by peer")
	tests := []struct {
		name      string
		restarts  int
		err       error
		committed int64
		last      int64
		post      bool
		want      bool
	}{
		{name: "403 below cap", restarts: 1, err: expired, committed: 100, last: 100, want: false},
		{name: "403 at cap", restarts: maxZeroProgressRestarts, err: expired, committed: 100, last: 100, want: true},
		{name: "frozen checkpoint at cap", restarts: maxZeroProgressRestarts, err: reset, committed: 100, last: 100, want: true},
		{name: "progress making failure at cap keeps resume", restarts: maxZeroProgressRestarts, err: reset, committed: 150, last: 100, want: false},
		{name: "first failure of fresh session at cap keeps resume", restarts: maxZeroProgressRestarts, err: reset, committed: 100, last: 0, want: false},
		{name: "postprocess at cap keeps own copy", restarts: maxZeroProgressRestarts, err: reset, committed: 100, last: 100, post: true, want: false},
		{name: "unknown evidence at cap", restarts: maxZeroProgressRestarts, err: reset, committed: -1, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &jobState{durable: jobmodel.DurableJob{SessionRestarts: test.restarts, LastFailureCommittedBytes: test.last}}
			if got := shouldSettleFreshDownloadRequired(state, test.err, test.committed, test.post); got != test.want {
				t.Fatalf("shouldSettleFreshDownloadRequired() = %t; want %t", got, test.want)
			}
		})
	}
}

func TestShouldRestartZeroProgressFailure(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*jobState)
		summary engine.ResumeSummary
		want    bool
	}{
		{name: "expired media link", mutate: func(state *jobState) { state.durable.LastErrorCode = retryCodeMediaLinkExpired }, want: true},
		{
			name:    "zero progress resume with committed bytes",
			mutate:  func(state *jobState) { state.durable.ZeroProgressResumes = zeroProgressResumeThreshold },
			summary: engine.ResumeSummary{Components: []engine.ResumeComponent{{CommittedBytes: 3145728}}},
			want:    true,
		},
		{name: "progress making failure", mutate: func(state *jobState) {}, summary: engine.ResumeSummary{Components: []engine.ResumeComponent{{CommittedBytes: 150}}}, want: false},
		{
			name:    "zero progress without committed bytes",
			mutate:  func(state *jobState) { state.durable.ZeroProgressResumes = zeroProgressResumeThreshold },
			summary: engine.ResumeSummary{HasManifest: true},
			want:    false,
		},
		{name: "challenge timeout is not escalation", mutate: func(state *jobState) { state.durable.LastErrorCode = retryCodeYouTubeChallengePreTransfer }, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &jobState{snap: JobSnapshot{}, durable: jobmodel.DurableJob{}}
			if test.mutate != nil {
				test.mutate(state)
			}
			if got := shouldRestartZeroProgressFailure(state, test.summary); got != test.want {
				t.Fatalf("shouldRestartZeroProgressFailure() = %t; want %t", got, test.want)
			}
		})
	}
	if shouldRestartZeroProgressFailure(nil, engine.ResumeSummary{}) {
		t.Fatal("nil state was restartable")
	}
}

func TestIsExpiredMediaLinkErrorUsesTypedDownloadStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil"},
		{name: "download 403", err: &engine.DownloadHTTPStatusError{Code: http.StatusForbidden}, want: true},
		{name: "wrapped download 403", err: fmt.Errorf("multi-track transfer: %w", &engine.DownloadHTTPStatusError{Code: http.StatusForbidden}), want: true},
		{name: "download 401", err: &engine.DownloadHTTPStatusError{Code: http.StatusUnauthorized}},
		{name: "extractor 403", err: &engine.HTTPStatusError{Code: http.StatusForbidden}},
		{name: "coincidental text", err: errors.New("multi-track transfer: download HTTP status 403")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isExpiredMediaLinkError(test.err); got != test.want {
				t.Fatalf("isExpiredMediaLinkError(%v) = %t; want %t", test.err, got, test.want)
			}
		})
	}
}

func TestExpiredMediaLinkFailureCopy(t *testing.T) {
	expired := fmt.Errorf("multi-track transfer: %w", &engine.DownloadHTTPStatusError{Code: http.StatusForbidden})
	if got := failureMessage(expired, 0, false); got != mediaLinkExpiredMessage {
		t.Fatalf("expired-link copy = %q; want %q", got, mediaLinkExpiredMessage)
	}
	if got := failureMessage(expired, 3145728, false); got != mediaLinkExpiredMessage {
		t.Fatalf("expired-link copy with bytes = %q; want %q", got, mediaLinkExpiredMessage)
	}
	if got := errorReason(expired); got != retryCodeMediaLinkExpired {
		t.Fatalf("expired-link reason = %q; want %q", got, retryCodeMediaLinkExpired)
	}
	if got := failureMessage(errors.New("connection reset by peer"), 100, false); got != downloadStoppedMidMessage {
		t.Fatalf("mid-transfer copy = %q; want %q", got, downloadStoppedMidMessage)
	}
	// Post-processing failures have complete bytes and keep their own copy.
	network := &engine.Error{Category: engine.ErrorNetwork, Err: errors.New("transport closed")}
	if got := failureMessage(network, 5000, true); got != "Network error" {
		t.Fatalf("post-processing copy = %q; want Network error", got)
	}
	// Unknown checkpoint evidence keeps the generic copy.
	if got := failureMessage(errors.New("connection reset by peer"), -1, false); got != "connection reset by peer" {
		t.Fatalf("unknown-evidence copy = %q; want raw message", got)
	}
	if got := errorReason(errors.New("connection reset by peer")); got != "internal" {
		t.Fatalf("generic reason = %q; want internal", got)
	}
}

// TestV2EscalationDiscardRunsOnlyAfterCommit covers the destructive-cleanup
// ordering the review required: the fresh session must be durably committed
// before the retired workspace is touched, a discard failure must never block
// or roll back the fresh retry, and an omitted discard (crash-equivalent)
// leaves a recoverable orphan instead of a stranded row.
func TestV2EscalationDiscardRunsOnlyAfterCommit(t *testing.T) {
	tests := []struct {
		name          string
		failCommit    bool
		discardErr    error
		wantDiscarded bool
		wantLeakLog   bool
	}{
		{name: "transaction failure before discard", failCommit: true, wantDiscarded: false, wantLeakLog: false},
		{name: "post-commit discard failure", discardErr: errors.New("discard rejected"), wantDiscarded: true, wantLeakLog: true},
		{name: "crash-equivalent omission of discard", wantDiscarded: true, wantLeakLog: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, root, _ := newV2TestStore(t, "job-discard-order")
			store.state.Jobs[0].Lifecycle = jobmodel.LifecycleFailed
			store.state.Jobs[0].Desired = jobmodel.DesiredRunning
			store.state.Jobs[0].LastFailureCommittedBytes = 100
			store.state.Jobs[0].ZeroProgressResumes = zeroProgressResumeThreshold
			original := store.state.Jobs[0]

			manager := New(nil, nil)
			defer manager.Close()
			if err := manager.SetStateStore(store); err != nil {
				t.Fatal(err)
			}
			if err := manager.RestoreStateV2(store.Snapshot()); err != nil {
				t.Fatal(err)
			}
			manager.inspectResume = func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
				return engine.ResumeSummary{
					HasManifest: true, Classification: "available",
					Components: []engine.ResumeComponent{{ID: "video", Kind: "video", CommittedBytes: 100}},
				}, nil
			}
			discarded := make(chan string, 1)
			manager.prepareResumeDiscard = func(_ context.Context, _ engine.OutputRootRef, sessionID string) (*engine.ResumeDiscardHandle, error) {
				discarded <- sessionID
				if test.discardErr != nil {
					return nil, test.discardErr
				}
				return nil, nil
			}
			var logged strings.Builder
			logRetiredSessionLeak = func(format string, args ...any) {
				logged.WriteString(fmt.Sprintf(format, args...))
			}
			t.Cleanup(func() { logRetiredSessionLeak = log.Printf })
			requests := make(chan engine.Request, 1)
			manager.runDownload = func(_ context.Context, request engine.Request, _ engine.EventHandler) (engine.Result, error) {
				requests <- request
				return engine.Result{Filename: filepath.Join(root, "Demo [abc123] [1080p].mp4")}, nil
			}
			if test.failCommit {
				store.mu.Lock()
				store.failNext = errors.New("store unavailable")
				store.mu.Unlock()
			}

			retryErr := manager.Retry("job-discard-order")
			if test.failCommit {
				if retryErr == nil {
					t.Fatal("Retry() survived a failed escalation commit")
				}
				select {
				case sessionID := <-discarded:
					t.Fatalf("discard of session %q ran before the commit succeeded", sessionID)
				default:
				}
				after := store.Snapshot().Jobs[0]
				if after.Lifecycle != jobmodel.LifecycleFailed || after.SessionID != original.SessionID || after.SessionRestarts != 0 {
					t.Fatalf("failed-commit row = %#v; want the untouched failed row and original session", after)
				}
				return
			}
			if retryErr != nil {
				t.Fatalf("Retry() failed: %v", retryErr)
			}
			if test.wantDiscarded {
				if sessionID := <-discarded; sessionID != original.SessionID {
					t.Fatalf("discarded session = %q; want the retired session %q", sessionID, original.SessionID)
				}
			}
			request := <-requests
			if request.Filesystem.Resume.SessionID == original.SessionID || request.Filesystem.Resume.SessionID == "" {
				t.Fatalf("fresh retry session = %q; want a fresh identity", request.Filesystem.Resume.SessionID)
			}
			completed := waitForV2Job(t, store, "job-discard-order", jobmodel.LifecycleCompleted)
			if completed.SessionRestarts != 1 {
				t.Fatalf("escalation restarts = %d; want 1", completed.SessionRestarts)
			}
			cleanup := store.Snapshot().Cleanup
			if test.discardErr != nil {
				if len(cleanup) != 1 || cleanup[0].SessionID != original.SessionID || cleanup[0].State != jobmodel.CleanupPending {
					t.Fatalf("retired cleanup = %#v; want durable pending evidence for %q", cleanup, original.SessionID)
				}
			} else if len(cleanup) != 0 {
				t.Fatalf("successful retired cleanup left tombstones: %#v", cleanup)
			}
			if test.wantLeakLog {
				message := logged.String()
				if !strings.Contains(message, "job-discard-order") {
					t.Fatalf("leak log = %q; want the job ID", message)
				}
				if strings.Contains(message, root) {
					t.Fatalf("leak log = %q; leaked the output root path", message)
				}
				// Reclamation is conditional on the root staying discoverable
				// (live job, tombstone, or current download folder), so the log
				// must not promise it unconditionally.
				if strings.Contains(message, "will reclaim") {
					t.Fatalf("leak log = %q; promised reclamation the orphan scan cannot guarantee", message)
				}
			} else if logged.Len() != 0 {
				t.Fatalf("unexpected leak log = %q", logged.String())
			}
		})
	}
}

// TestV2DownloadAgainResetsEscalationBookkeeping ensures the documented escape
// hatch starts from a clean slate: a re-downloaded item must not inherit the
// canceled row's restart budget.
func TestV2DownloadAgainResetsEscalationBookkeeping(t *testing.T) {
	store, _, _ := newV2TestStore(t, "job-canceled-counters")
	store.state.Jobs[0].Lifecycle = jobmodel.LifecycleCanceled
	store.state.Jobs[0].Desired = jobmodel.DesiredCanceled
	store.state.Jobs[0].LastErrorCode = retryCodeMediaLinkExpired
	store.state.Jobs[0].LastFailureCommittedBytes = 3145728
	store.state.Jobs[0].ZeroProgressResumes = 1
	store.state.Jobs[0].SessionRestarts = maxZeroProgressRestarts

	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreStateV2(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	manager.runDownload = func(context.Context, engine.Request, engine.EventHandler) (engine.Result, error) {
		return engine.Result{}, nil
	}
	newID, err := manager.DownloadAgain("job-canceled-counters")
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range store.Snapshot().Jobs {
		if job.ID != newID {
			continue
		}
		if job.LastFailureCommittedBytes != 0 || job.ZeroProgressResumes != 0 || job.SessionRestarts != 0 || job.LastErrorCode != "" {
			t.Fatalf("replacement escalation bookkeeping = %#v; want zeroed fields", job)
		}
		if job.RetryMode != jobmodel.RetryModeNone {
			t.Fatalf("replacement retry mode = %s; want none", job.RetryMode)
		}
		return
	}
	t.Fatalf("replacement job %q missing from state", newID)
}

func TestV2DownloadAgainKeepsCanceledRowAndFreshReservationGroup(t *testing.T) {
	store, root, plan := newV2TestStore(t, "job-canceled", "job-blocker")
	store.mu.Lock()
	store.state.Jobs[1].Request.Title = "Blocker"
	store.state.Jobs[1].Reservation.Artifacts[0].Basename = "Blocker [abc123] [1080p].mp4"
	store.mu.Unlock()
	manager := New(nil, nil)
	defer manager.Close()
	manager.SetConcurrency(1)
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	releaseCanceled := make(chan struct{})
	releaseBlocker := make(chan struct{})
	started := make(chan int, 3)
	var runs int
	manager.runDownload = func(ctx context.Context, _ engine.Request, _ engine.EventHandler) (engine.Result, error) {
		runs++
		started <- runs
		switch runs {
		case 1:
			<-ctx.Done()
			<-releaseCanceled
			return engine.Result{}, ctx.Err()
		case 2:
			<-releaseBlocker
			return engine.Result{}, nil
		default:
			return engine.Result{}, nil
		}
	}
	if _, err := manager.SubmitAdmitted("job-canceled", Request{URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Demo", PlanID: plan.ID, OutputDir: root}, &plan, AdmittedOutput{Basename: "Demo [abc123] [1080p].mp4"}); err != nil {
		t.Fatal(err)
	}
	if got := <-started; got != 1 {
		t.Fatalf("first runner number = %d; want 1", got)
	}
	if _, err := manager.SubmitAdmitted("job-blocker", Request{URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Blocker", PlanID: plan.ID, OutputDir: root}, &plan, AdmittedOutput{Basename: "Blocker [abc123] [1080p].mp4"}); err != nil {
		t.Fatal(err)
	}
	manager.Cancel("job-canceled")
	close(releaseCanceled)
	waitForV2Job(t, store, "job-canceled", jobmodel.LifecycleCanceled)
	select {
	case got := <-started:
		if got != 2 {
			t.Fatalf("blocker runner number = %d; want 2", got)
		}
	case <-time.After(time.Second):
		t.Fatal("blocker runner did not occupy the FIFO slot")
	}
	newID, err := manager.DownloadAgain("job-canceled")
	if err != nil {
		t.Fatal(err)
	}
	if newID == "job-canceled" {
		t.Fatal("DownloadAgain reused canceled logical job ID")
	}
	state := store.Snapshot()
	var old, replacement jobmodel.DurableJob
	for _, job := range state.Jobs {
		switch job.ID {
		case "job-canceled":
			old = job
		case newID:
			replacement = job
		}
	}
	if old.Lifecycle != jobmodel.LifecycleCanceled || replacement.Lifecycle != jobmodel.LifecyclePending {
		t.Fatalf("old/replacement lifecycle = %s/%s", old.Lifecycle, replacement.Lifecycle)
	}
	if old.AttemptID == replacement.AttemptID || old.SessionID == replacement.SessionID || old.Reservation.GroupID == replacement.Reservation.GroupID {
		t.Fatalf("download-again identities/groups reused: old=%#v replacement=%#v", old, replacement)
	}
	if len(state.Jobs) != 3 {
		t.Fatalf("State v2 jobs after DownloadAgain = %d; want blocker, canceled history, and replacement", len(state.Jobs))
	}
	snapshot, ok := manager.Find(newID)
	if !ok || snapshot.Status != StatusPending || snapshot.Lifecycle != jobmodel.LifecyclePending || snapshot.Phase != jobmodel.PhasePreparing || snapshot.Desired != jobmodel.DesiredRunning {
		t.Fatalf("replacement pre-start snapshot = %#v, %v; want pending/preparing/running", snapshot, ok)
	}
	if replacement.Lifecycle != jobmodel.LifecyclePending || replacement.Phase != jobmodel.PhasePreparing || replacement.Desired != jobmodel.DesiredRunning {
		t.Fatalf("replacement pre-start durable row = %#v; want pending/preparing/running", replacement)
	}
	close(releaseBlocker)
	waitForV2Job(t, store, newID, jobmodel.LifecycleCompleted)
}

func TestV2StartTransitionFailureDoesNotRewritePendingDurableRow(t *testing.T) {
	store, root, plan := newV2TestStore(t, "job-pending")
	store.failNext = errors.New("disk unavailable")
	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	manager.runDownload = func(context.Context, engine.Request, engine.EventHandler) (engine.Result, error) {
		t.Fatal("runner started after failed durable activation")
		return engine.Result{}, nil
	}
	if _, err := manager.SubmitAdmitted("job-pending", Request{URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Demo", PlanID: plan.ID, OutputDir: root}, &plan, AdmittedOutput{Basename: "Demo [abc123] [1080p].mp4"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		job := store.Snapshot().Jobs[0]
		if job.Lifecycle == jobmodel.LifecyclePending {
			if job.Revision != 1 {
				t.Fatalf("failed activation changed pending revision to %d", job.Revision)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pending durable row did not remain unchanged: %#v", store.Snapshot().Jobs)
}

func TestV2PauseAllUsesOneRevisionCheckedBatchAndKeepsActiveSlot(t *testing.T) {
	store, root, plan := newV2TestStore(t, "job-one", "job-two")
	manager := New(nil, nil)
	defer manager.Close()
	manager.SetConcurrency(1)
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	manager.runDownload = func(ctx context.Context, _ engine.Request, _ engine.EventHandler) (engine.Result, error) {
		started <- struct{}{}
		<-ctx.Done()
		<-release
		return engine.Result{}, ctx.Err()
	}
	for _, id := range []string{"job-one", "job-two"} {
		if _, err := manager.SubmitAdmitted(id, Request{URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Demo", PlanID: plan.ID, OutputDir: root}, &plan, AdmittedOutput{Basename: "Demo [abc123] [1080p].mp4"}); err != nil {
			t.Fatal(err)
		}
	}
	<-started
	waitForV2Job(t, store, "job-one", jobmodel.LifecycleActive)
	store.mu.Lock()
	callsBefore := store.calls
	store.mu.Unlock()
	if got := manager.PauseAll(); got != 2 {
		t.Fatalf("PauseAll() = %d; want two accepted rows", got)
	}
	store.mu.Lock()
	callsAfter := store.calls
	store.mu.Unlock()
	if callsAfter != callsBefore+1 {
		t.Fatalf("PauseAll State transactions = %d; want one batch transaction", callsAfter-callsBefore)
	}
	waitForV2Job(t, store, "job-two", jobmodel.LifecyclePaused)
	if got := manager.Active(); got != "job-one" {
		t.Fatalf("active job during PauseAll settlement = %q; want job-one", got)
	}
	close(release)
	waitForV2Job(t, store, "job-one", jobmodel.LifecyclePaused)
}

func TestV2StaleAttemptPreconditionCannotOverwriteWinner(t *testing.T) {
	store, _, _ := newV2TestStore(t, "job-stale")
	before := store.Snapshot().Jobs[0]
	store.mu.Lock()
	store.state.Jobs[0].AttemptID = "winner-attempt"
	store.mu.Unlock()
	err := store.Transaction([]jobmodel.JobPrecondition{{
		ID: before.ID, Revision: before.Revision, Lifecycle: before.Lifecycle,
		AttemptID: before.AttemptID, SessionID: before.SessionID, OutputRoot: before.OutputRoot,
	}}, func(state *jobmodel.State) error {
		state.Jobs[0].Lifecycle = jobmodel.LifecycleCompleted
		return nil
	})
	if err == nil {
		t.Fatal("stale attempt transaction succeeded")
	}
	if got := store.Snapshot().Jobs[0].Lifecycle; got != jobmodel.LifecyclePending {
		t.Fatalf("stale attempt changed winner lifecycle to %s", got)
	}
}

func TestV2PendingCancelWinsOverPauseWithoutSnapshotOverwrite(t *testing.T) {
	store, root, plan := newV2TestStore(t, "job-running", "job-pending")
	store.mu.Lock()
	store.state.Jobs[1].SessionID = ""
	store.mu.Unlock()
	manager := New(nil, nil)
	defer manager.Close()
	manager.SetConcurrency(1)
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	manager.runDownload = func(ctx context.Context, _ engine.Request, _ engine.EventHandler) (engine.Result, error) {
		started <- struct{}{}
		<-ctx.Done()
		<-release
		return engine.Result{}, ctx.Err()
	}
	for _, id := range []string{"job-running", "job-pending"} {
		if _, err := manager.SubmitAdmitted(id, Request{URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Demo", PlanID: plan.ID, OutputDir: root}, &plan, AdmittedOutput{Basename: "Demo [abc123] [1080p].mp4"}); err != nil {
			t.Fatal(err)
		}
	}
	<-started
	manager.Cancel("job-pending")
	if err := manager.Pause("job-pending"); err == nil {
		t.Fatal("Pause accepted while pending Cancel was already the winner")
	}
	waitForV2Job(t, store, "job-pending", jobmodel.LifecycleCanceled)
	snapshot, ok := manager.Find("job-pending")
	if !ok || snapshot.Status != StatusCanceled {
		t.Fatalf("pending cancel snapshot = %#v, %v; want canceled", snapshot, ok)
	}
	manager.Cancel("job-running")
	close(release)
}

func TestV2CompletionHistoryAndStaleAttemptEventAreIdempotent(t *testing.T) {
	store, root, plan := newV2TestStore(t, "job-idempotent")
	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	var runs int
	manager.runDownload = func(_ context.Context, _ engine.Request, _ engine.EventHandler) (engine.Result, error) {
		runs++
		started <- struct{}{}
		if runs == 1 {
			return engine.Result{}, errors.New("first attempt failed")
		}
		return engine.Result{}, nil
	}
	if _, err := manager.SubmitAdmitted("job-idempotent", Request{URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Demo", PlanID: plan.ID, OutputDir: root}, &plan, AdmittedOutput{Basename: "Demo [abc123] [1080p].mp4"}); err != nil {
		t.Fatal(err)
	}
	<-started
	manager.mu.Lock()
	state := manager.all["job-idempotent"]
	oldWorker := state.worker
	manager.mu.Unlock()
	if oldWorker == nil {
		// The injected runner can finish before the test reacquires m.mu;
		// retain a distinct stale-attempt token for the rejection assertion.
		oldWorker = &worker{JobID: "job-idempotent", AttemptID: "stale"}
	}
	waitForV2Job(t, store, "job-idempotent", jobmodel.LifecycleFailed)
	manager.inspectResume = func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
		return engine.ResumeSummary{HasManifest: true, Classification: "available"}, nil
	}
	if err := manager.Retry("job-idempotent"); err != nil {
		t.Fatal(err)
	}
	waitForV2Job(t, store, "job-idempotent", jobmodel.LifecycleCompleted)
	manager.mu.Lock()
	before := manager.all["job-idempotent"].snap
	manager.mu.Unlock()
	manager.handleEventAttempt(state, oldWorker, engine.Event{Kind: engine.EventDownloadProgress, Bytes: 99, Total: 100})
	manager.mu.Lock()
	after := manager.all["job-idempotent"].snap
	manager.mu.Unlock()
	if after.Bytes != before.Bytes || after.Status != before.Status {
		t.Fatalf("stale attempt event changed snapshot from %#v to %#v", before, after)
	}

	store.mu.Lock()
	callsBefore := store.calls
	store.mu.Unlock()
	if err := manager.settleDurable(state, jobmodel.LifecycleCompleted, jobmodel.DesiredRunning, jobmodel.PhaseReadyToPublish, after, "", false, -1); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	callsAfter := store.calls
	history := append([]jobmodel.HistoryEntry(nil), store.state.History...)
	store.mu.Unlock()
	if callsAfter != callsBefore || len(history) != 1 {
		t.Fatalf("idempotent completion calls/history = %d/%d; want no extra commit and one history row", callsAfter-callsBefore, len(history))
	}
}
