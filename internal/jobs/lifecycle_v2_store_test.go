package jobs_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/tejasa97/vidstow/internal/jobmodel"
	"github.com/tejasa97/vidstow/internal/jobs"
	"github.com/tejasa97/vidstow/internal/store"
	"github.com/tejasa97/youtube_dlp/engine"
)

// TestV2ZeroProgressRestartCommitsOnRealStore proves the escalated retry
// rotation is valid under V2Store.Transaction: no mid-life tombstone is
// written, so validateState cannot reject the commit the way a retired-session
// tombstone overlapping a live job would.
func TestV2ZeroProgressRestartCommitsOnRealStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	durable, status, err := store.OpenV2(path)
	if err != nil || !status.Healthy() {
		t.Fatalf("OpenV2: %v %#v", err, status)
	}
	defer durable.Close()

	root := t.TempDir()
	now := time.Now().UTC()
	const jobID = "job-real-store"
	originalSession := "0123456789abcdef0123456789abcdef"
	outputRoot := jobmodel.OutputRootRef{CanonicalPath: root, Identity: "volume-test"}
	job := jobmodel.DurableJob{
		ID: jobID, Revision: 1, AttemptID: "attempt-real-store", SessionID: originalSession,
		QueueOrdinal: 1, Lifecycle: jobmodel.LifecycleFailed, Phase: jobmodel.PhasePreparing, Desired: jobmodel.DesiredRunning,
		Request: jobmodel.PersistedRequest{
			SourceURL: "https://www.youtube.com/watch?v=abc12345678", VideoID: "abc12345678",
			Title: "Demo", Channel: "Creator", Quality: "best", PlanID: "video-1080-mp4",
		},
		Plan:       jobmodel.PersistedPlan{ID: "video-1080-mp4", Kind: "video", Label: "1080p", Container: "MP4", PrivateSelector: "137+140"},
		OutputRoot: outputRoot,
		Reservation: jobmodel.ReservationSet{
			GroupID: jobID, Directory: outputRoot,
			Artifacts: []jobmodel.ReservedArtifact{{Kind: string(engine.ArtifactKindPrimary), Identity: "primary", Basename: "Demo [abc123] [1080p].mp4"}},
		},
		RetryMode: jobmodel.RetryModeResumeValidated, LastErrorCode: jobs.RetryCodeMediaLinkExpiredForTest,
		LastFailureCommittedBytes: 3145728, ZeroProgressResumes: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := durable.Transaction(nil, func(state *jobmodel.State) error {
		state.Jobs = []jobmodel.DurableJob{job}
		state.NextQueueOrdinal = 2
		return nil
	}); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	manager := jobs.New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(durable); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreStateV2(durable.Snapshot()); err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 1)
	jobs.InstallDownloadHooksForTest(manager,
		func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
			return engine.ResumeSummary{
				HasManifest: true, Classification: "available",
				Components: []engine.ResumeComponent{{ID: "video", Kind: "video", CommittedBytes: 3145728}},
			}, nil
		},
		func(_ context.Context, request engine.Request, _ engine.EventHandler) (engine.Result, error) {
			started <- request.Filesystem.Resume.SessionID
			return engine.Result{Filename: filepath.Join(root, "Demo [abc123] [1080p].mp4")}, nil
		},
	)

	if err := manager.Retry(jobID); err != nil {
		t.Fatal(err)
	}
	restarted := <-started
	if restarted == originalSession || restarted == "" {
		t.Fatalf("real-store restart session = %q; want fresh identity", restarted)
	}

	deadline := time.Now().Add(2 * time.Second)
	var completed jobmodel.DurableJob
	found := false
	for time.Now().Before(deadline) {
		for _, candidate := range durable.Snapshot().Jobs {
			if candidate.ID == jobID && candidate.Lifecycle == jobmodel.LifecycleCompleted {
				completed = candidate
				found = true
				break
			}
		}
		if found {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !found {
		t.Fatalf("real-store job did not complete; snapshot = %#v", durable.Snapshot().Jobs)
	}
	if completed.SessionID != restarted || completed.RetryMode != jobmodel.RetryModeRestartNewSession || completed.SessionRestarts != 1 {
		t.Fatalf("real-store completed identity = %#v; want session %q restart-new-session restarts=1", completed, restarted)
	}
	if len(durable.Snapshot().Cleanup) != 0 {
		t.Fatalf("real-store cleanup = %#v; want no mid-life tombstone", durable.Snapshot().Cleanup)
	}
}
