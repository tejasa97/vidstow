package recovery

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/tejasa97/vidstow/internal/jobmodel"
	"github.com/tejasa97/vidstow/internal/reservationfs"
	"github.com/tejasa97/youtube_dlp/engine"
)

func TestCleanupMissingSessionTombstoneIsIdempotent(t *testing.T) {
	rootPath := realTempDir(t)
	root, err := reservationfs.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	facts := root.Facts()
	engineFacts, err := engine.ValidateOutputRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	store := &memoryStateStore{state: jobmodel.State{
		Version:  jobmodel.StateVersion,
		Settings: jobmodel.Settings{DownloadFolder: rootPath},
		Cleanup: []jobmodel.CleanupTombstone{{
			JobID: "job-1", SessionID: "0123456789abcdef0123456789abcdef",
			OutputRoot: jobmodel.OutputRootRef{CanonicalPath: facts.Volume.CanonicalPath, Identity: facts.Volume.Identity, EngineIdentity: engineFacts.Identity},
			State:      jobmodel.CleanupPending, CreatedAt: now, UpdatedAt: now,
		}},
	}}
	first, err := RunCleanupOnce(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if first.RemovedTombstones != 1 || len(store.Snapshot().Cleanup) != 0 {
		t.Fatalf("first cleanup pass = %#v state=%#v; want one removed tombstone", first, store.Snapshot().Cleanup)
	}
	second, err := RunCleanupOnce(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if second.RemovedTombstones != 0 || len(store.Snapshot().Cleanup) != 0 {
		t.Fatalf("second cleanup pass = %#v state=%#v; want idempotent empty result", second, store.Snapshot().Cleanup)
	}
}

func TestCleanupQuarantineIsPreservedAndNotRetried(t *testing.T) {
	rootPath := realTempDir(t)
	root, err := reservationfs.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	facts := root.Facts()
	engineFacts, err := engine.ValidateOutputRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = root.Close()
	now := time.Now().UTC()
	store := &memoryStateStore{state: jobmodel.State{
		Version:  jobmodel.StateVersion,
		Settings: jobmodel.Settings{DownloadFolder: rootPath},
		Cleanup: []jobmodel.CleanupTombstone{{
			JobID: "job-quarantine", SessionID: "0123456789abcdef0123456789abcdef",
			OutputRoot: jobmodel.OutputRootRef{CanonicalPath: facts.Volume.CanonicalPath, Identity: "replaced-volume", EngineIdentity: engineFacts.Identity},
			State:      jobmodel.CleanupQuarantined, CreatedAt: now, UpdatedAt: now,
		}},
	}}
	pass, err := RunCleanupOnce(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	state := store.Snapshot()
	if pass.QuarantinedTombstones != 1 || len(state.Cleanup) != 1 || state.Cleanup[0].State != jobmodel.CleanupQuarantined {
		t.Fatalf("quarantined cleanup pass=%#v state=%#v; want preserved quarantine", pass, state.Cleanup)
	}
}

func TestCleanupMissingRootRemainsPendingForRetry(t *testing.T) {
	rootPath := realTempDir(t)
	now := time.Now().UTC()
	store := &memoryStateStore{state: jobmodel.State{
		Version:  jobmodel.StateVersion,
		Settings: jobmodel.Settings{DownloadFolder: rootPath},
		Cleanup: []jobmodel.CleanupTombstone{{
			JobID: "job-missing-root", SessionID: "0123456789abcdef0123456789abcdef",
			OutputRoot: jobmodel.OutputRootRef{CanonicalPath: rootPath + "/gone", Identity: "volume-test"},
			State:      jobmodel.CleanupPending, CreatedAt: now, UpdatedAt: now,
		}},
	}}
	pass, err := RunCleanupOnce(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	state := store.Snapshot()
	if pass.RetriedTombstones != 1 || len(state.Cleanup) != 1 || state.Cleanup[0].State != jobmodel.CleanupPending {
		t.Fatalf("missing-root cleanup pass=%#v state=%#v; want pending retry", pass, state.Cleanup)
	}
}

func realTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temporary directory: %v", err)
	}
	return path
}
