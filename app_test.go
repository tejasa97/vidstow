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
			startStartupCleanup = func(context.Context, recovery.StateStore, time.Duration) <-chan struct{} {
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
	startStartupCleanup = func(ctx context.Context, _ recovery.StateStore, _ time.Duration) <-chan struct{} {
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
