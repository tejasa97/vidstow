// Package recovery owns the V3 startup evidence pass. It runs before the
// manager is constructed, so no runner, scheduler, or cleanup worker can act
// on a State image that has not been reconciled.
package recovery

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/tejasa97/vidstow/internal/jobmodel"
	"github.com/tejasa97/ytdlp-go/engine"
)

// StateStore is the smallest State v2 authority needed for startup. The
// store implementation rereads and validates the latest image under its
// stable lock before committing a reconciliation batch.
type StateStore interface {
	Snapshot() jobmodel.State
	Transaction([]jobmodel.JobPrecondition, func(*jobmodel.State) error) error
}

// InspectFunc and PrepareDiscardFunc are injectable only to make the
// evidence matrix deterministic in tests. Production uses the public engine
// facades, never an engine internal/session package.
type InspectFunc func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error)
type PrepareDiscardFunc func(context.Context, engine.OutputRootRef, string) (*engine.ResumeDiscardHandle, error)

// Options controls clock and public engine seams for one startup pass.
type Options struct {
	Now            func() time.Time
	Inspect        InspectFunc
	PrepareDiscard PrepareDiscardFunc
}

const (
	classAvailable                = "available"
	classUnavailableRoot          = "unavailable_root"
	classMissingLease             = "missing_lease"
	classUnsafePath               = "unsafe_path"
	classCorruptManifest          = "corrupt_manifest"
	classUnknownManifest          = "unknown_manifest_version"
	classManifestIndeterminate    = "manifest_commit_indeterminate"
	classPublicationIndeterminate = "publication_indeterminate"
	classNeedsReconciliation      = "needs_reconciliation"
	classDiscardPending           = "discard_pending"
	classLeaseContention          = "lease_contention"

	phasePrepared       = "prepared"
	phaseExtracting     = "extracting"
	phaseDownloading    = "downloading"
	phaseProcessing     = "processing"
	phaseReadyToPublish = "ready_to_publish"
	phaseCompleted      = "completed"

	publicationCommitted = "committed"
	statusCompleted      = "completed"
)

type decision struct {
	lifecycle      jobmodel.Lifecycle
	desired        jobmodel.DesiredState
	phase          jobmodel.Phase
	retryMode      jobmodel.RetryMode
	actionCode     string
	lastErrorCode  string
	cleanupPending bool
	completed      bool
}

// Reconcile performs the complete State-v2 startup evidence pass. It never
// starts a worker. Interrupted pending/active work is converted to paused;
// publication-winner evidence becomes completed; uncertain evidence becomes
// action-required; and canceling rows are discarded only after the public
// engine says the session is safe to destroy.
func Reconcile(ctx context.Context, stateStore StateStore, options Options) (jobmodel.State, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if stateStore == nil {
		return jobmodel.State{}, errors.New("recovery: nil State v2 store")
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.Inspect == nil {
		options.Inspect = engine.InspectResumeState
	}
	if options.PrepareDiscard == nil {
		options.PrepareDiscard = engine.PrepareResumeDiscard
	}
	now := options.Now().UTC()
	if now.IsZero() {
		return jobmodel.State{}, errors.New("recovery: clock returned zero time")
	}

	snapshot := stateStore.Snapshot()
	if snapshot.Version != jobmodel.StateVersion {
		return jobmodel.State{}, errors.New("recovery: State v2 snapshot is unavailable")
	}
	decisions := make(map[string]decision)
	preconditions := make([]jobmodel.JobPrecondition, 0, len(snapshot.Jobs))
	for _, job := range snapshot.Jobs {
		if err := ctx.Err(); err != nil {
			return jobmodel.State{}, err
		}
		result, changed, err := reconcileJob(ctx, job, options, now)
		if err != nil {
			return jobmodel.State{}, fmt.Errorf("recovery: reconcile job %q: %w", job.ID, err)
		}
		if !changed {
			continue
		}
		decisions[job.ID] = result
		preconditions = append(preconditions, jobmodel.JobPrecondition{
			ID: job.ID, Revision: job.Revision, Lifecycle: job.Lifecycle,
			AttemptID: job.AttemptID, SessionID: job.SessionID, OutputRoot: job.OutputRoot,
		})
	}

	if len(decisions) > 0 {
		if err := stateStore.Transaction(preconditions, func(state *jobmodel.State) error {
			for index := range state.Jobs {
				job := &state.Jobs[index]
				result, ok := decisions[job.ID]
				if !ok {
					continue
				}
				applyDecision(job, result, now)
				if result.completed {
					appendCompletionHistory(state, *job)
				}
				if result.cleanupPending {
					upsertCleanupTombstone(state, *job, result.lastErrorCode, now)
				}
			}
			return nil
		}); err != nil {
			return jobmodel.State{}, fmt.Errorf("recovery: commit reconciliation: %w", err)
		}
	}
	return stateStore.Snapshot(), nil
}

func reconcileJob(ctx context.Context, job jobmodel.DurableJob, options Options, now time.Time) (decision, bool, error) {
	result := decision{
		lifecycle:     job.Lifecycle,
		desired:       job.Desired,
		phase:         job.Phase,
		retryMode:     job.RetryMode,
		actionCode:    job.ActionRequiredCode,
		lastErrorCode: job.LastErrorCode,
	}

	// An already terminal row is durable evidence owned by State v2. Its
	// session may have been cleaned, so do not downgrade it just because the
	// optional session workspace is gone.
	if job.Lifecycle == jobmodel.LifecycleCanceled || job.Lifecycle == jobmodel.LifecycleCompleted || job.Lifecycle == jobmodel.LifecycleFailed || job.Lifecycle == jobmodel.LifecycleActionRequired {
		return result, false, nil
	}

	if job.Lifecycle == jobmodel.LifecyclePending {
		result.lifecycle = jobmodel.LifecyclePaused
		result.desired = jobmodel.DesiredPaused
		result.phase = normalizeJobPhase(job.Phase)
		result.actionCode = ""
		result.lastErrorCode = ""
		return result, decisionDiffers(job, result), nil
	}

	if job.OutputRoot.CanonicalPath == "" || job.OutputRoot.Identity == "" || job.SessionID == "" {
		result.lifecycle = jobmodel.LifecycleActionRequired
		result.desired = jobmodel.DesiredPaused
		result.actionCode = "recovery-session-reference-invalid"
		result.lastErrorCode = result.actionCode
		return result, decisionDiffers(job, result), nil
	}

	summary, inspectErr := options.Inspect(ctx, recoveryEngineRootRef(job.OutputRoot), job.SessionID)
	if inspectErr != nil {
		if job.Lifecycle == jobmodel.LifecycleCanceling {
			result.lifecycle = jobmodel.LifecycleActionRequired
			result.desired = jobmodel.DesiredPaused
			result.actionCode = "cancel-reconciliation-required"
		} else {
			result.lifecycle = jobmodel.LifecycleActionRequired
			result.desired = jobmodel.DesiredPaused
			result.actionCode = "recovery-session-unavailable"
		}
		result.lastErrorCode = result.actionCode
		return result, decisionDiffers(job, result), nil
	}

	if summary.Publication == publicationCommitted && (summary.Phase == phaseReadyToPublish || summary.Phase == phaseCompleted || summary.Status == statusCompleted) {
		result.lifecycle = jobmodel.LifecycleCompleted
		result.desired = jobmodel.DesiredRunning
		result.phase = jobmodel.PhaseReadyToPublish
		result.retryMode = jobmodel.RetryModePublishOnly
		result.actionCode = ""
		result.lastErrorCode = ""
		result.completed = true
		return result, decisionDiffers(job, result), nil
	}

	if IsRootUnavailable(summary) && job.Lifecycle != jobmodel.LifecycleCanceling {
		result.lifecycle = jobmodel.LifecycleActionRequired
		result.desired = jobmodel.DesiredPaused
		result.actionCode = "output-root-unavailable"
		result.lastErrorCode = result.actionCode
		return result, decisionDiffers(job, result), nil
	}

	if summary.LeaseContended || hasUncertainEvidence(summary) {
		result.lifecycle = jobmodel.LifecycleActionRequired
		result.desired = jobmodel.DesiredPaused
		result.actionCode = evidenceActionCode(summary)
		result.lastErrorCode = result.actionCode
		return result, decisionDiffers(job, result), nil
	}

	if job.Lifecycle == jobmodel.LifecycleCanceling {
		return reconcileCanceling(ctx, job, result, options, summary)
	}

	result.lifecycle = jobmodel.LifecyclePaused
	result.desired = jobmodel.DesiredPaused
	result.phase = mapSessionPhase(summary.Phase, job.Phase)
	result.actionCode = ""
	result.lastErrorCode = ""
	if result.phase == jobmodel.PhaseReadyToPublish {
		result.retryMode = jobmodel.RetryModePublishOnly
	} else if result.retryMode == jobmodel.RetryModeNone {
		result.retryMode = jobmodel.RetryModeResumeValidated
	}
	return result, decisionDiffers(job, result), nil
}

func reconcileCanceling(ctx context.Context, job jobmodel.DurableJob, result decision, options Options, summary engine.ResumeSummary) (decision, bool, error) {
	// A missing session workspace is a safe, idempotent no-op: Cancel already
	// has no resumable evidence left to discard.
	if IsRootUnavailable(summary) {
		result.lifecycle = jobmodel.LifecycleCanceled
		result.desired = jobmodel.DesiredCanceled
		result.phase = jobmodel.PhaseCleaningUp
		result.actionCode = ""
		result.lastErrorCode = ""
		return result, decisionDiffers(job, result), nil
	}
	handle, err := options.PrepareDiscard(ctx, recoveryEngineRootRef(job.OutputRoot), job.SessionID)
	if err != nil {
		result.lifecycle = jobmodel.LifecycleActionRequired
		result.desired = jobmodel.DesiredPaused
		result.actionCode = "cancel-reconciliation-required"
		result.lastErrorCode = result.actionCode
		return result, decisionDiffers(job, result), nil
	}
	if handle == nil {
		return decision{}, false, errors.New("recovery: engine returned a nil discard handle")
	}
	discard, discardErr := handle.Discard(ctx)
	if discardErr != nil {
		if errors.Is(discardErr, context.Canceled) || errors.Is(discardErr, context.DeadlineExceeded) {
			return decision{}, false, discardErr
		}
		result.lifecycle = jobmodel.LifecycleCanceled
		result.desired = jobmodel.DesiredCanceled
		result.phase = jobmodel.PhaseCleaningUp
		result.lastErrorCode = "cleanup"
		result.cleanupPending = true
		return result, decisionDiffers(job, result) || true, nil
	}
	switch string(discard.Disposition) {
	case string(engine.ResumeDiscarded):
		result.lifecycle = jobmodel.LifecycleCanceled
		result.desired = jobmodel.DesiredCanceled
		result.phase = jobmodel.PhaseCleaningUp
		result.actionCode = ""
		result.lastErrorCode = ""
		return result, decisionDiffers(job, result), nil
	case string(engine.ResumeDiscardCleanupPending):
		result.lifecycle = jobmodel.LifecycleCanceled
		result.desired = jobmodel.DesiredCanceled
		result.phase = jobmodel.PhaseCleaningUp
		result.lastErrorCode = "cleanup"
		result.cleanupPending = true
		return result, decisionDiffers(job, result) || true, nil
	default:
		result.lifecycle = jobmodel.LifecycleActionRequired
		result.desired = jobmodel.DesiredPaused
		result.actionCode = "cancel-reconciliation-required"
		result.lastErrorCode = result.actionCode
		return result, decisionDiffers(job, result), nil
	}
}

func applyDecision(job *jobmodel.DurableJob, result decision, now time.Time) {
	job.Lifecycle = result.lifecycle
	job.Desired = result.desired
	job.Phase = result.phase
	job.RetryMode = result.retryMode
	job.ActionRequiredCode = result.actionCode
	job.LastErrorCode = result.lastErrorCode
	job.Revision++
	job.UpdatedAt = now
}

func decisionDiffers(job jobmodel.DurableJob, result decision) bool {
	return job.Lifecycle != result.lifecycle || job.Desired != result.desired || job.Phase != result.phase || job.RetryMode != result.retryMode || job.ActionRequiredCode != result.actionCode || job.LastErrorCode != result.lastErrorCode
}

func hasUncertainEvidence(summary engine.ResumeSummary) bool {
	if summary.Publication == "indeterminate" || summary.Cleanup == "indeterminate" || summary.Status == "needs_reconciliation" {
		return true
	}
	classes := append([]engine.ResumeInspectionClass{summary.Classification}, summary.Classifications...)
	for _, class := range classes {
		if class == "" {
			continue
		}
		switch string(class) {
		case classAvailable, classMissingLease, classUnavailableRoot:
			continue
		default:
			return true
		}
	}
	return false
}

func evidenceActionCode(summary engine.ResumeSummary) string {
	if summary.LeaseContended {
		return "session-lease-contended"
	}
	classes := append([]engine.ResumeInspectionClass{summary.Classification}, summary.Classifications...)
	for _, class := range classes {
		switch string(class) {
		case classPublicationIndeterminate, classManifestIndeterminate:
			return "publication-reconciliation-required"
		case classUnknownManifest:
			return "session-version-unknown"
		case classCorruptManifest:
			return "session-manifest-corrupt"
		case classUnsafePath:
			return "session-path-unsafe"
		case classNeedsReconciliation, classDiscardPending:
			return "session-reconciliation-required"
		}
	}
	if summary.Cleanup == "indeterminate" || summary.Status == "needs_reconciliation" {
		return "session-reconciliation-required"
	}
	return "session-reconciliation-required"
}

func normalizeJobPhase(phase jobmodel.Phase) jobmodel.Phase {
	if phase == "" {
		return jobmodel.PhasePreparing
	}
	return phase
}

func mapSessionPhase(phase engine.SessionPhase, fallback jobmodel.Phase) jobmodel.Phase {
	switch string(phase) {
	case phaseDownloading:
		return jobmodel.PhaseDownloading
	case phaseProcessing:
		return jobmodel.PhaseFinalizing
	case phaseReadyToPublish, phaseCompleted:
		return jobmodel.PhaseReadyToPublish
	case phasePrepared, phaseExtracting:
		return jobmodel.PhasePreparing
	default:
		return normalizeJobPhase(fallback)
	}
}

func appendCompletionHistory(state *jobmodel.State, job jobmodel.DurableJob) {
	for _, existing := range state.History {
		if existing.ID == job.ID {
			return
		}
	}
	filename := ""
	for _, artifact := range job.Reservation.Artifacts {
		if artifact.Kind == string(engine.ArtifactKindPrimary) && artifact.Identity == "primary" {
			filename = artifact.Basename
			break
		}
	}
	completedAt := job.UpdatedAt.UTC().Format(time.RFC3339Nano)
	absolute := ""
	if job.OutputRoot.CanonicalPath != "" && filename != "" {
		absolute = filepath.Join(job.OutputRoot.CanonicalPath, filename)
	}
	state.History = append([]jobmodel.HistoryEntry{{
		ID: job.ID, VideoID: job.Request.VideoID, Title: job.Request.Title, Channel: job.Request.Channel,
		Quality: job.Request.Quality, Container: job.Plan.Container, VideoCodec: job.Plan.VideoCodec,
		AudioCodec: job.Plan.AudioCodec, Filename: filename, AbsolutePath: absolute,
		CompletedAt: completedAt, DurationLabel: job.Request.Duration,
	}}, state.History...)
	if len(state.History) > 200 {
		state.History = state.History[:200]
	}
}

func upsertCleanupTombstone(state *jobmodel.State, job jobmodel.DurableJob, code string, now time.Time) {
	for index := range state.Cleanup {
		if state.Cleanup[index].JobID != job.ID {
			continue
		}
		state.Cleanup[index].SessionID = job.SessionID
		state.Cleanup[index].OutputRoot = job.OutputRoot
		state.Cleanup[index].Reservation = job.Reservation
		state.Cleanup[index].State = jobmodel.CleanupPending
		state.Cleanup[index].LastErrorCode = code
		state.Cleanup[index].UpdatedAt = now
		return
	}
	state.Cleanup = append(state.Cleanup, jobmodel.CleanupTombstone{
		JobID: job.ID, SessionID: job.SessionID, OutputRoot: job.OutputRoot, Reservation: job.Reservation,
		State: jobmodel.CleanupPending, LastErrorCode: code, CreatedAt: now, UpdatedAt: now,
	})
}

// IsRootUnavailable is used by the app to distinguish a temporarily missing
// output directory from an unsafe/replaced root. It deliberately matches only
// the bounded public inspection classification.
func IsRootUnavailable(summary engine.ResumeSummary) bool {
	if summary.HasManifest {
		return false
	}
	if strings.EqualFold(string(summary.Classification), classUnavailableRoot) {
		return true
	}
	for _, class := range summary.Classifications {
		if strings.EqualFold(string(class), classUnavailableRoot) {
			return true
		}
	}
	return false
}

func recoveryEngineRootRef(root jobmodel.OutputRootRef) engine.OutputRootRef {
	identity := root.EngineIdentity
	if identity == "" {
		if validated, err := engine.ValidateOutputRoot(root.CanonicalPath); err == nil {
			return validated
		}
	}
	if identity == "" {
		identity = root.Identity
	}
	return engine.OutputRootRef{CanonicalPath: root.CanonicalPath, Identity: identity}
}
