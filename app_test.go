package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tejasa97/vidstow/internal/admission"
	"github.com/tejasa97/vidstow/internal/jobmodel"
	"github.com/tejasa97/vidstow/internal/jobs"
	"github.com/tejasa97/vidstow/internal/recovery"
	"github.com/tejasa97/vidstow/internal/store"
	"github.com/tejasa97/youtube_dlp/engine"
)

func TestCurrentBuildInfoHasReleaseAndPlatformIdentity(t *testing.T) {
	info := currentBuildInfo()
	if info.Version != appVersion || info.EngineVersion == "" || info.OS == "" || info.Architecture == "" || info.GoVersion == "" {
		t.Fatalf("build info = %#v; want complete release identity", info)
	}
}

func TestFriendlyAnalyzeErrorDeadline(t *testing.T) {
	if got := friendlyAnalyzeError(context.DeadlineExceeded); got != "Video analysis timed out — retry" {
		t.Fatalf("friendlyAnalyzeError() = %q; want analysis-timeout message", got)
	}
}

func TestFriendlyAnalyzeErrorYouTubeChallengeTimeout(t *testing.T) {
	typed := &engine.Error{
		Category: engine.ErrorUnsupported,
		Op:       "youtube extraction",
		Err: errors.New(
			"JavaScript challenge solver unavailable: EJS helper timeout: JavaScript execution timed out",
		),
	}
	err := fmt.Errorf("analyze video: %w", typed)

	if got := friendlyAnalyzeError(err); got != "YouTube challenge timed out — retry" {
		t.Fatalf("friendlyAnalyzeError() = %q; want challenge-timeout message", got)
	}
}

func TestFriendlyAnalyzeErrorOtherUnsupportedIsUnchanged(t *testing.T) {
	err := &engine.Error{
		Category: engine.ErrorUnsupported,
		Op:       "youtube extraction",
		Err:      errors.New("video unavailable"),
	}

	if got := friendlyAnalyzeError(err); got != "That link is not a supported single YouTube video." {
		t.Fatalf("friendlyAnalyzeError() = %q; want ordinary unsupported message", got)
	}
}

func TestVideoSubfolderIsPortableAndBounded(t *testing.T) {
	got := videoSubfolder(`  A/B: "Demo"? *Video*  `, "abc123")
	if got != `AB Demo Video [abc123]` {
		t.Fatalf("videoSubfolder() = %q", got)
	}
	if got := videoSubfolder("...", "abc123"); got != "Video [abc123]" {
		t.Fatalf("empty videoSubfolder() = %q", got)
	}
}

func TestStartupRecoveryRequiredFailsClosedWithoutRuntimeFallback(t *testing.T) {
	for _, status := range []store.StartupStatus{
		{Mode: store.StartupRecoveryRequired, Reason: store.RecoveryCorruptState},
		{Mode: store.StartupRecoveryRequired, Reason: store.RecoveryUnsupportedVersion},
		{Mode: store.StartupRecoveryRequired, Reason: store.RecoveryIndeterminate},
	} {
		t.Run(string(status.Reason), func(t *testing.T) {
			restore := installAppTestSeams(t)
			defer restore()
			openStateV2 = func(string) (*store.V2Store, store.StartupStatus, error) { return nil, status, nil }
			cleanupStarts := 0
			startStartupCleanup = func(context.Context, recovery.StateStore, time.Duration, func(recovery.CleanupPass)) <-chan struct{} {
				cleanupStarts++
				done := make(chan struct{})
				close(done)
				return done
			}

			app := NewApp()
			app.startupAt(context.Background(), filepath.Join(t.TempDir(), "state.json"))
			if got := app.GetStartupStatus(); got != status {
				t.Fatalf("startup status = %#v, want %#v", got, status)
			}
			if app.store != nil || app.jobs != nil || app.coordinator != nil || app.cleanupDone != nil {
				t.Fatalf("recovery-required startup created authoritative runtime state: %#v", app)
			}
			if cleanupStarts != 0 {
				t.Fatalf("cleanup worker starts = %d, want 0", cleanupStarts)
			}
		})
	}
}

func TestHealthyStartupReconcilesBeforeRestoringManager(t *testing.T) {
	restore := installAppTestSeams(t)
	defer restore()
	sequence := make([]string, 0, 4)
	prepareStartupStateRoots = func(jobmodel.State) error {
		sequence = append(sequence, "roots")
		return nil
	}
	reconcileStartupState = func(_ context.Context, state *store.V2Store) (jobmodel.State, error) {
		sequence = append(sequence, "reconcile")
		return state.Snapshot(), nil
	}
	restoreStartupManager = func(manager *jobs.Manager, snapshot jobmodel.State) error {
		sequence = append(sequence, "restore")
		return manager.RestoreStateV2(snapshot)
	}
	startStartupCleanup = func(ctx context.Context, _ recovery.StateStore, _ time.Duration, _ func(recovery.CleanupPass)) <-chan struct{} {
		sequence = append(sequence, "cleanup")
		done := make(chan struct{})
		go func() {
			<-ctx.Done()
			close(done)
		}()
		return done
	}

	app := NewApp()
	app.startupAt(context.Background(), filepath.Join(secureAppTempDir(t), "state.json"))
	if status := app.GetStartupStatus(); !status.Healthy() {
		t.Fatalf("startup status = %#v, want healthy", status)
	}
	if app.store == nil || app.jobs == nil || app.coordinator == nil || app.cleanupDone == nil {
		t.Fatalf("healthy startup did not construct V3 authority: %#v", app)
	}
	if got := app.jobs.Active(); got != "" {
		t.Fatalf("restored manager active job = %q, want zero occupied slots", got)
	}
	if len(sequence) != 4 || sequence[0] != "roots" || sequence[1] != "reconcile" || sequence[2] != "restore" || sequence[3] != "cleanup" {
		t.Fatalf("startup order = %#v, want roots/reconcile/restore/cleanup", sequence)
	}
	app.stopCleanup(context.Background())
	if err := app.jobs.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.store.Close(); err != nil {
		t.Fatal(err)
	}
}

func secureAppTempDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(home, ".vidstow-app-tests-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

type deadlineShutdownManager struct{}

func (deadlineShutdownManager) Shutdown(context.Context) error { return nil }

func (deadlineShutdownManager) Close(contexts ...context.Context) error {
	ctx := context.Background()
	if len(contexts) > 0 && contexts[0] != nil {
		ctx = contexts[0]
	}
	<-ctx.Done()
	return ctx.Err()
}

type deadlineStateStore struct {
	mu              sync.Mutex
	closed          bool
	closeCalls      int
	transactions    int
	afterCloseCalls int
}

func (s *deadlineStateStore) transaction() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transactions++
	if s.closed {
		s.afterCloseCalls++
	}
}

func (s *deadlineStateStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.closeCalls++
	return nil
}

func (s *deadlineStateStore) snapshot() (int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCalls, s.transactions, s.afterCloseCalls
}

func TestShutdownDeadlineKeepsStateOpenUntilLateWorkSettles(t *testing.T) {
	restore := installAppTestSeams(t)
	defer restore()
	state := &deadlineStateStore{}
	releaseWorker := make(chan struct{})
	workerDone := make(chan struct{})
	go func() {
		<-releaseWorker
		state.transaction()
		close(workerDone)
	}()

	app := NewApp()
	app.quitDeadline = time.Now().Add(20 * time.Millisecond)
	app.shutdownManager = deadlineShutdownManager{}
	app.closeState = state.Close
	app.shutdown(context.Background())
	if closes, _, _ := state.snapshot(); closes != 0 {
		t.Fatalf("State close calls after shutdown deadline = %d, want 0", closes)
	}

	close(releaseWorker)
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("late worker did not settle after shutdown deadline")
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	if closes, transactions, afterClose := state.snapshot(); closes != 1 || transactions != 1 || afterClose != 0 {
		t.Fatalf("state after late worker = closes=%d transactions=%d after-close=%d, want 1/1/0", closes, transactions, afterClose)
	}
}

func TestDeleteDownloadFileRemovesCompletedQueueRow(t *testing.T) {
	restore := installAppTestSeams(t)
	defer restore()
	app, mediaPath := newCompletedDownloadApp(t)

	if err := app.DeleteDownloadFile("completed-job"); err != nil {
		t.Fatalf("DeleteDownloadFile: %v", err)
	}
	if _, err := os.Stat(mediaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted media still present: %v", err)
	}
	if got := app.ListDownloads(); len(got) != 0 {
		t.Fatalf("history after delete = %#v; want empty", got)
	}
	if _, ok := app.jobs.Find("completed-job"); ok {
		t.Fatal("completed queue row remained after deleting the download")
	}
	if _, ok := app.jobs.Find("failed-job"); !ok {
		t.Fatal("unrelated failed queue row was removed")
	}
}

func TestRemoveDownloadKeepsCompletedQueueRowAndFile(t *testing.T) {
	restore := installAppTestSeams(t)
	defer restore()
	app, mediaPath := newCompletedDownloadApp(t)

	if err := app.RemoveDownload("completed-job"); err != nil {
		t.Fatalf("RemoveDownload: %v", err)
	}
	if _, err := os.Stat(mediaPath); err != nil {
		t.Fatalf("history-only remove deleted media: %v", err)
	}
	if got := app.ListDownloads(); len(got) != 0 {
		t.Fatalf("history after remove = %#v; want empty", got)
	}
	if _, ok := app.jobs.Find("completed-job"); !ok {
		t.Fatal("completed queue row was removed by history-only removal")
	}
}

func TestDeleteDownloadFileSucceedsWhenQueueRowAlreadyGone(t *testing.T) {
	restore := installAppTestSeams(t)
	defer restore()
	app, mediaPath := newCompletedDownloadApp(t)
	if err := app.jobs.Remove("completed-job"); err != nil {
		t.Fatalf("Remove completed job: %v", err)
	}

	if err := app.DeleteDownloadFile("completed-job"); err != nil {
		t.Fatalf("DeleteDownloadFile after queue removal: %v", err)
	}
	if _, err := os.Stat(mediaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted media still present: %v", err)
	}
	if got := app.ListDownloads(); len(got) != 0 {
		t.Fatalf("history after delete = %#v; want empty", got)
	}
}

func newCompletedDownloadApp(t *testing.T) (*App, string) {
	t.Helper()
	root := secureAppTempDir(t)
	downloads := filepath.Join(root, "downloads")
	if err := os.Mkdir(downloads, 0o700); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(downloads, "Demo.mp4")
	if err := os.WriteFile(mediaPath, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(root, "state.json")
	st, status, err := store.OpenV2(statePath)
	if err != nil || !status.Healthy() || st == nil {
		t.Fatalf("OpenV2 = %v, %#v, %v", st, status, err)
	}
	t.Cleanup(func() { _ = st.Close() })

	settings := st.Settings()
	settings.DownloadFolder = downloads
	if err := st.SetSettings(settings); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}

	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	plan := jobmodel.PersistedPlan{ID: "video-1080-mp4", Kind: "video", Label: "1080p", Container: "MP4"}
	rootRef := jobmodel.OutputRootRef{CanonicalPath: downloads, Identity: "volume-test"}
	completed := jobmodel.DurableJob{
		ID: "completed-job", Revision: 1, AttemptID: "attempt-completed", SessionID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		QueueOrdinal: 1, Lifecycle: jobmodel.LifecycleCompleted, Phase: jobmodel.PhaseReadyToPublish, Desired: jobmodel.DesiredRunning,
		Request: jobmodel.PersistedRequest{
			SourceURL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", VideoID: "dQw4w9WgXcQ",
			Title: "Demo", Channel: "Creator", Quality: "best", PlanID: plan.ID,
		},
		Plan: plan, OutputRoot: rootRef,
		Reservation: jobmodel.ReservationSet{
			GroupID: "completed-job", Directory: rootRef,
			Artifacts: []jobmodel.ReservedArtifact{{Kind: string(engine.ArtifactKindPrimary), Identity: "primary", Basename: "Demo.mp4"}},
		},
		RetryMode: jobmodel.RetryModeNone, CreatedAt: now, UpdatedAt: now,
	}
	failed := completed
	failed.ID = "failed-job"
	failed.AttemptID = "attempt-failed"
	failed.SessionID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	failed.QueueOrdinal = 2
	failed.Lifecycle = jobmodel.LifecycleFailed
	failed.Phase = jobmodel.PhaseDownloading
	failed.Reservation.GroupID = "failed-job"
	failed.Reservation.Artifacts = []jobmodel.ReservedArtifact{{Kind: string(engine.ArtifactKindPrimary), Identity: "primary", Basename: "Other.mp4"}}
	failed.LastErrorCode = "download-failed"

	if err := st.Transaction(nil, func(state *jobmodel.State) error {
		state.NextQueueOrdinal = 3
		state.Jobs = []jobmodel.DurableJob{completed, failed}
		state.History = []jobmodel.HistoryEntry{{
			ID: completed.ID, VideoID: completed.Request.VideoID, Title: completed.Request.Title,
			Channel: completed.Request.Channel, Quality: "1080p", Container: "MP4",
			Filename: "Demo.mp4", AbsolutePath: mediaPath, SizeBytes: int64(len("fixture")),
			CompletedAt: now.Format(time.RFC3339Nano),
		}}
		return nil
	}); err != nil {
		t.Fatalf("seed completed download: %v", err)
	}

	manager := jobs.New(nil, nil)
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.SetStateStore(st); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreStateV2(st.Snapshot()); err != nil {
		t.Fatal(err)
	}
	coordinator, err := admission.NewCoordinator(admission.Dependencies{Store: st, Resolver: manager, Queue: manager})
	if err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.ctx = context.Background()
	app.store = st
	app.jobs = manager
	app.coordinator = coordinator
	app.setStartupStatus(store.StartupStatus{Mode: store.StartupHealthy})
	return app, mediaPath
}

func installAppTestSeams(t *testing.T) func() {
	t.Helper()
	oldOpen := openStateV2
	oldPrepare := prepareStartupStateRoots
	oldReconcile := reconcileStartupState
	oldRestore := restoreStartupManager
	oldCleanup := startStartupCleanup
	oldLog := logAppErrorf
	oldEmit := emitAppEvent
	logAppErrorf = func(context.Context, string, ...interface{}) {}
	emitAppEvent = func(context.Context, string, ...interface{}) {}
	return func() {
		openStateV2 = oldOpen
		prepareStartupStateRoots = oldPrepare
		reconcileStartupState = oldReconcile
		restoreStartupManager = oldRestore
		startStartupCleanup = oldCleanup
		logAppErrorf = oldLog
		emitAppEvent = oldEmit
	}
}
