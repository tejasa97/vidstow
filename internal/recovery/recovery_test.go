package recovery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tejasa97/vidstow/internal/jobmodel"
	"github.com/tejasa97/youtube_dlp/engine"
)

type memoryStateStore struct {
	mu           sync.Mutex
	state        jobmodel.State
	transactions int
}

func (s *memoryStateStore) Snapshot() jobmodel.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return jobmodel.CloneState(s.state)
}

func (s *memoryStateStore) Transaction(preconditions []jobmodel.JobPrecondition, mutate func(*jobmodel.State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := jobmodel.CloneState(s.state)
	for _, want := range preconditions {
		found := false
		for _, job := range next.Jobs {
			if job.ID != want.ID {
				continue
			}
			found = true
			if job.Revision != want.Revision || job.Lifecycle != want.Lifecycle || job.AttemptID != want.AttemptID || job.SessionID != want.SessionID || job.OutputRoot != want.OutputRoot {
				return errors.New("recovery test store: stale precondition")
			}
		}
		if !found {
			return errors.New("recovery test store: missing row")
		}
	}
	if err := mutate(&next); err != nil {
		return err
	}
	s.transactions++
	s.state = next
	return nil
}

func recoveryTestJob(id string, lifecycle jobmodel.Lifecycle, phase jobmodel.Phase) jobmodel.DurableJob {
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	return jobmodel.DurableJob{
		ID: id, Revision: 1, AttemptID: "attempt-" + id, SessionID: "0123456789abcdef0123456789abcdef",
		Lifecycle: lifecycle, Phase: phase, Desired: jobmodel.DesiredRunning,
		Request:     jobmodel.PersistedRequest{SourceURL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Example", Quality: "best", PlanID: "plan-1"},
		Plan:        jobmodel.PersistedPlan{ID: "plan-1", Kind: "video", Label: "1080p", Container: "MP4", PrivateSelector: "137+140"},
		OutputRoot:  jobmodel.OutputRootRef{CanonicalPath: "/tmp/vidstow-recovery-test", Identity: "volume-test", EngineIdentity: "engine-volume-test"},
		Reservation: jobmodel.ReservationSet{GroupID: id, Artifacts: []jobmodel.ReservedArtifact{{Kind: "primary", Identity: "primary", Basename: "Example.mp4"}}},
		CreatedAt:   now, UpdatedAt: now,
	}
}

func recoveryTestStore(job jobmodel.DurableJob) *memoryStateStore {
	return &memoryStateStore{state: jobmodel.State{
		Version:  jobmodel.StateVersion,
		Settings: jobmodel.Settings{DownloadFolder: "/tmp/vidstow-recovery-test", DownloadConcurrency: 2},
		Jobs:     []jobmodel.DurableJob{job},
	}}
}

func TestReconcileInterruptedLifecycleMatrix(t *testing.T) {
	testCases := []struct {
		name          string
		job           jobmodel.DurableJob
		summary       engine.ResumeSummary
		wantLifecycle jobmodel.Lifecycle
		wantPhase     jobmodel.Phase
		wantHistory   bool
	}{
		{
			name:          "pending is paused without engine inspection",
			job:           recoveryTestJob("pending", jobmodel.LifecyclePending, jobmodel.PhasePreparing),
			wantLifecycle: jobmodel.LifecyclePaused,
			wantPhase:     jobmodel.PhasePreparing,
		},
		{
			name:          "active is paused with resumable evidence",
			job:           recoveryTestJob("active", jobmodel.LifecycleActive, jobmodel.PhaseDownloading),
			summary:       engine.ResumeSummary{Classification: "available", Phase: "downloading", Status: "active"},
			wantLifecycle: jobmodel.LifecyclePaused,
			wantPhase:     jobmodel.PhaseDownloading,
		},
		{
			name:          "pausing settles to paused",
			job:           recoveryTestJob("pausing", jobmodel.LifecyclePausing, jobmodel.PhaseFinalizing),
			summary:       engine.ResumeSummary{Classification: "available", Phase: "processing", Status: "active"},
			wantLifecycle: jobmodel.LifecyclePaused,
			wantPhase:     jobmodel.PhaseFinalizing,
		},
		{
			name:          "ready to publish committed winner is completed",
			job:           recoveryTestJob("ready", jobmodel.LifecycleActive, jobmodel.PhaseReadyToPublish),
			summary:       engine.ResumeSummary{Classification: "available", Phase: "ready_to_publish", Status: "active", Publication: "committed"},
			wantLifecycle: jobmodel.LifecycleCompleted,
			wantPhase:     jobmodel.PhaseReadyToPublish,
			wantHistory:   true,
		},
		{
			name:          "published winner is completed",
			job:           recoveryTestJob("published", jobmodel.LifecyclePaused, jobmodel.PhaseReadyToPublish),
			summary:       engine.ResumeSummary{Classification: "available", Phase: "completed", Status: "completed", Publication: "committed"},
			wantLifecycle: jobmodel.LifecycleCompleted,
			wantPhase:     jobmodel.PhaseReadyToPublish,
			wantHistory:   true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := recoveryTestStore(testCase.job)
			inspectCalls := 0
			got, err := Reconcile(context.Background(), store, Options{
				Now: func() time.Time { return time.Date(2026, 8, 11, 13, 0, 0, 0, time.UTC) },
				Inspect: func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
					inspectCalls++
					return testCase.summary, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Jobs) != 1 || got.Jobs[0].Lifecycle != testCase.wantLifecycle || got.Jobs[0].Phase != testCase.wantPhase {
				t.Fatalf("reconciled job = %#v; want lifecycle=%q phase=%q", got.Jobs, testCase.wantLifecycle, testCase.wantPhase)
			}
			if testCase.name == "pending is paused without engine inspection" && inspectCalls != 0 {
				t.Fatalf("pending inspection calls = %d; want 0", inspectCalls)
			}
			if testCase.name != "pending is paused without engine inspection" && inspectCalls != 1 {
				t.Fatalf("inspection calls = %d; want 1", inspectCalls)
			}
			if testCase.wantHistory != (len(got.History) == 1) {
				t.Fatalf("history = %#v; want history=%t", got.History, testCase.wantHistory)
			}
			if store.transactions != 1 {
				t.Fatalf("State transactions = %d; want one reconciliation commit", store.transactions)
			}
		})
	}
}

func TestReconcileFailClosedEvidenceMatrix(t *testing.T) {
	classes := []struct {
		name string
		make func(*engine.ResumeSummary)
	}{
		{name: "unavailable output root", make: func(summary *engine.ResumeSummary) {
			summary.HasManifest = false
			summary.Classification = "unavailable_root"
		}},
		{name: "unknown manifest", make: func(summary *engine.ResumeSummary) { summary.Classification = "unknown_manifest_version" }},
		{name: "corrupt manifest", make: func(summary *engine.ResumeSummary) { summary.Classification = "corrupt_manifest" }},
		{name: "unsafe path", make: func(summary *engine.ResumeSummary) { summary.Classification = "unsafe_path" }},
		{name: "indeterminate publication", make: func(summary *engine.ResumeSummary) { summary.Publication = "indeterminate" }},
		{name: "indeterminate cleanup", make: func(summary *engine.ResumeSummary) { summary.Cleanup = "indeterminate" }},
		{name: "status needs reconciliation", make: func(summary *engine.ResumeSummary) { summary.Status = "needs_reconciliation" }},
	}
	for _, testCase := range classes {
		t.Run(testCase.name, func(t *testing.T) {
			job := recoveryTestJob("uncertain", jobmodel.LifecycleActive, jobmodel.PhaseDownloading)
			store := recoveryTestStore(job)
			summary := engine.ResumeSummary{HasManifest: true, Classification: "available", Phase: "downloading", Status: "active"}
			testCase.make(&summary)
			got, err := Reconcile(context.Background(), store, Options{Inspect: func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
				return summary, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if got.Jobs[0].Lifecycle != jobmodel.LifecycleActionRequired || got.Jobs[0].Desired != jobmodel.DesiredPaused {
				t.Fatalf("uncertain evidence lifecycle = %q desired=%q; want action-required/paused", got.Jobs[0].Lifecycle, got.Jobs[0].Desired)
			}
		})
	}
}

func TestReconcileLeaseContentionRequiresActionWithoutDiscard(t *testing.T) {
	job := recoveryTestJob("lease-contended", jobmodel.LifecycleActive, jobmodel.PhaseDownloading)
	store := recoveryTestStore(job)
	prepareDiscardCalls := 0
	got, err := Reconcile(context.Background(), store, Options{
		Inspect: func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
			return engine.ResumeSummary{HasManifest: true, Classification: "available", Phase: "downloading", Status: "active", LeaseContended: true}, nil
		},
		PrepareDiscard: func(context.Context, engine.OutputRootRef, string) (*engine.ResumeDiscardHandle, error) {
			prepareDiscardCalls++
			return nil, errors.New("discard must not be prepared while the lease is contended")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciled := got.Jobs[0]
	if reconciled.Lifecycle != jobmodel.LifecycleActionRequired || reconciled.Desired != jobmodel.DesiredPaused || reconciled.ActionRequiredCode != "session-lease-contended" {
		t.Fatalf("lease contention reconciliation = %#v; want action-required/paused/session-lease-contended", reconciled)
	}
	if prepareDiscardCalls != 0 {
		t.Fatalf("discard preparation calls = %d, want 0 while lease is contended", prepareDiscardCalls)
	}
}

func TestReconcileCancelingMissingWorkspaceIsSafeAndIdempotent(t *testing.T) {
	job := recoveryTestJob("canceling", jobmodel.LifecycleCanceling, jobmodel.PhaseCleaningUp)
	store := recoveryTestStore(job)
	got, err := Reconcile(context.Background(), store, Options{Inspect: func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
		return engine.ResumeSummary{Classification: "unavailable_root"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Jobs[0].Lifecycle != jobmodel.LifecycleCanceled || got.Jobs[0].Desired != jobmodel.DesiredCanceled {
		t.Fatalf("canceling missing workspace = %#v; want canceled", got.Jobs[0])
	}
	if len(got.Cleanup) != 0 {
		t.Fatalf("cleanup tombstones = %#v; missing workspace should be an idempotent no-op", got.Cleanup)
	}
}

func TestReconcilePreservesFailedLifecycleAndRetrySemantics(t *testing.T) {
	job := recoveryTestJob("failed", jobmodel.LifecycleFailed, jobmodel.PhasePreparing)
	job.RetryMode = jobmodel.RetryModeResumeValidated
	job.LastErrorCode = "network"
	job.Desired = jobmodel.DesiredRunning
	store := recoveryTestStore(job)
	inspected := false
	got, err := Reconcile(context.Background(), store, Options{
		Inspect: func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error) {
			inspected = true
			return engine.ResumeSummary{Classification: "available", Phase: "downloading", Status: "active"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inspected {
		t.Fatal("failed row was inspected during reconciliation; it must be preserved unchanged")
	}
	if len(got.Jobs) != 1 {
		t.Fatalf("reconciled jobs = %d; want one preserved failed row", len(got.Jobs))
	}
	reconciled := got.Jobs[0]
	if reconciled.Lifecycle != jobmodel.LifecycleFailed || reconciled.Desired != jobmodel.DesiredRunning ||
		reconciled.RetryMode != jobmodel.RetryModeResumeValidated || reconciled.LastErrorCode != "network" {
		t.Fatalf("failed row mutated by reconciliation = %#v; want lifecycle=failed desired=running retryMode=resume-validated error=network", reconciled)
	}
	if reconciled.Revision != job.Revision {
		t.Fatalf("failed row revision changed from %d to %d; no mutation expected", job.Revision, reconciled.Revision)
	}
	if store.transactions != 0 {
		t.Fatalf("State transactions = %d; preserving a failed row must not commit", store.transactions)
	}
}

func TestReconcileRejectsUnavailableStateWithoutStartingAnything(t *testing.T) {
	store := &memoryStateStore{state: jobmodel.State{Version: 99}}
	if _, err := Reconcile(context.Background(), store, Options{}); err == nil {
		t.Fatal("Reconcile accepted an unknown State version")
	}
	if store.transactions != 0 {
		t.Fatalf("transactions = %d; invalid State must not be mutated", store.transactions)
	}
}
