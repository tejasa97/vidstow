// Package admission coordinates V1 output admission. It renders the engine's
// exact artifact declaration, selects a whole-set reservation against the
// caller-owned filesystem facts and the latest State v2 image, commits the
// pending durable job, and only then hands the same job ID to the existing
// FIFO manager.
package admission

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tejasa97/vidstow/internal/jobmodel"
	"github.com/tejasa97/vidstow/internal/jobs"
	"github.com/tejasa97/vidstow/internal/outputplan"
	"github.com/tejasa97/vidstow/internal/reservation"
	"github.com/tejasa97/vidstow/internal/reservationfs"
	"github.com/tejasa97/vidstow/internal/store"
	"github.com/tejasa97/youtube_dlp/engine"
	"github.com/tejasa97/youtube_dlp/engine/value"
)

// Queue is the narrow manager seam needed after the State v2 admission
// transaction commits. The concrete jobs.Manager preserves the existing FIFO
// scheduler and implements this interface.
type Queue interface {
	SubmitAdmitted(id string, request jobs.Request, plan *outputplan.Plan, output jobs.AdmittedOutput) (string, error)
}

// PlanResolver is the server-side output-plan authority. UI requests carry
// only the stable plan ID; they never provide a selector, container, or
// filename policy to admission.
type PlanResolver interface {
	ResolvePlan(videoID, planID string) (outputplan.Plan, error)
}

// StateStore is the transaction boundary required by admission. Keeping the
// interface narrow lets failure tests prove that a rejected transaction never
// reaches the FIFO manager without coupling them to store internals.
type StateStore interface {
	Transaction([]store.JobPrecondition, func(*jobmodel.State) error) error
}

// Dependencies are the side-effecting owners used by Coordinator.
type Dependencies struct {
	Store    StateStore
	Resolver PlanResolver
	Queue    Queue
	Now      func() time.Time
	NewIDs   func() (jobID, attemptID, sessionID string)
}

// Request is the safe, analyzed input for one curated output plan. Metadata
// is an in-memory engine value and is never persisted or sent to the frontend.
// Queue.PlanID is resolved through PlanResolver; callers cannot supply a raw
// engine selector or arbitrary output plan. Queue.OutputDir must be the exact
// path used to open Root, whose canonical handle facts remain authoritative.
type Request struct {
	Queue    jobs.Request
	Metadata value.Info
}

// Result reports the durable admission facts needed by the caller to wire the
// eventual session-enabled engine run. It contains no private engine paths.
type Result struct {
	Job         jobmodel.DurableJob
	Plan        outputplan.Plan
	Reservation jobmodel.ReservationSet
	Artifacts   []engine.ArtifactDeclaration
}

// Coordinator performs one V1 admission transaction. A successful State
// commit always precedes Queue.SubmitAdmitted; a later manager error leaves the
// pending durable row available for startup reconciliation.
type Coordinator struct {
	deps      Dependencies
	maxSuffix uint64
}

// NewCoordinator validates the required State and queue owners.
func NewCoordinator(deps Dependencies) (*Coordinator, error) {
	if deps.Store == nil {
		return nil, errors.New("admission: nil State v2 store")
	}
	if deps.Resolver == nil {
		return nil, errors.New("admission: nil server-side output-plan resolver")
	}
	if deps.Queue == nil {
		return nil, errors.New("admission: nil FIFO queue")
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	if deps.NewIDs == nil {
		deps.NewIDs = defaultIDs
	}
	return &Coordinator{deps: deps, maxSuffix: reservation.DefaultMaxSuffix}, nil
}

// SetMaxSuffix bounds the deterministic collision search for tests and
// deployments that need a stricter operational ceiling.
func (c *Coordinator) SetMaxSuffix(max uint64) error {
	if c == nil {
		return errors.New("admission: nil coordinator")
	}
	if max == 0 || max > reservation.MaxAllowedSuffix {
		return fmt.Errorf("admission: maximum suffix exceeds %d", reservation.MaxAllowedSuffix)
	}
	c.maxSuffix = max
	return nil
}

// Admit reserves and persists one job before admitting it to the existing
// manager. The caller must keep root open for the duration of this call.
func (c *Coordinator) Admit(ctx context.Context, root *reservationfs.Root, request Request) (Result, error) {
	if c == nil {
		return Result{}, errors.New("admission: nil coordinator")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if root == nil {
		return Result{}, errors.New("admission: nil reservation root")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if request.Queue.URL == "" || request.Queue.VideoID == "" || request.Queue.Title == "" || request.Queue.OutputDir == "" {
		return Result{}, errors.New("admission: incomplete queue request")
	}
	if request.Queue.PlanID == "" {
		return Result{}, errors.New("admission: output plan ID is required")
	}
	plan, err := c.deps.Resolver.ResolvePlan(request.Queue.VideoID, request.Queue.PlanID)
	if err != nil {
		return Result{}, fmt.Errorf("admission: resolve output plan: %w", err)
	}
	if err := validateResolvedPlan(plan, request.Queue.PlanID); err != nil {
		return Result{}, err
	}

	facts := root.Facts()
	if facts.Volume.CanonicalPath == "" || facts.Volume.Identity == "" {
		return Result{}, errors.New("admission: reservation root has no stable identity")
	}
	engineRoot, err := engine.ValidateOutputRoot(facts.Volume.CanonicalPath)
	if err != nil {
		return Result{}, fmt.Errorf("admission: engine output root validation: %w", err)
	}
	if engineRoot.CanonicalPath != facts.Volume.CanonicalPath {
		return Result{}, errors.New("admission: engine and reservation output roots differ")
	}
	if err := verifyOutputRoot(request.Queue.OutputDir, facts.Volume.CanonicalPath); err != nil {
		return Result{}, err
	}
	policies := facts.Policies()
	if policies.Names == nil || policies.Volumes == nil || facts.Probe == nil {
		return Result{}, errors.New("admission: incomplete reservation root facts")
	}

	metadata := value.NewInfo(request.Metadata.Fields().Clone())
	metadata.Set("title", value.String(request.Queue.Title))
	metadata.Set("id", value.String(request.Queue.VideoID))
	if request.Queue.Channel != "" {
		metadata.Set("channel", value.String(request.Queue.Channel))
	}
	extension := strings.ToLower(strings.TrimPrefix(plan.Container, "."))
	artifacts, err := engine.RenderOutputArtifacts(engine.OutputPreviewRequest{
		Template:  jobs.OutputTemplateForPlan(plan),
		Metadata:  metadata,
		Extension: extension,
	})
	if err != nil {
		return Result{}, fmt.Errorf("admission: render output artifacts: %w", err)
	}
	if len(artifacts) == 0 {
		return Result{}, errors.New("admission: engine returned no output artifacts")
	}

	declarations := make([]reservation.ArtifactDeclaration, len(artifacts))
	for i, artifact := range artifacts {
		declarations[i] = reservation.ArtifactDeclaration{
			Kind:             string(artifact.Kind),
			Identity:         artifact.Identity,
			ProposedBasename: artifact.ProposedBasename,
		}
	}
	selector, err := reservation.NewSelector(reservation.Options{
		Policies:  policies,
		Probe:     facts.Probe,
		MaxSuffix: c.maxSuffix,
	})
	if err != nil {
		return Result{}, fmt.Errorf("admission: configure reservation selector: %w", err)
	}

	jobID, attemptID, sessionID := c.deps.NewIDs()
	if err := validateGeneratedIDs(jobID, attemptID, sessionID); err != nil {
		return Result{}, err
	}
	now := c.deps.Now().UTC()
	if now.IsZero() {
		return Result{}, errors.New("admission: clock returned zero time")
	}

	rootRef := jobmodel.OutputRootRef{CanonicalPath: facts.Volume.CanonicalPath, Identity: facts.Volume.Identity, EngineIdentity: engineRoot.Identity}
	durableRequest := jobmodel.PersistedRequest{
		SourceURL: request.Queue.URL,
		VideoID:   request.Queue.VideoID,
		Title:     request.Queue.Title,
		Channel:   request.Queue.Channel,
		Quality:   string(request.Queue.Quality),
		PlanID:    request.Queue.PlanID,
		Duration:  request.Queue.Duration,
	}
	if durableRequest.Quality == "" {
		durableRequest.Quality = string(jobs.QualityBest)
	}
	durablePlan := jobmodel.PersistedPlan{
		ID:              plan.ID,
		Kind:            string(plan.Kind),
		Label:           plan.Label,
		Container:       plan.Container,
		VideoCodec:      plan.VideoCodec,
		AudioCodec:      plan.AudioCodec,
		RequiresFFmpeg:  plan.RequiresFFmpeg,
		PrivateSelector: plan.Selector,
	}

	var committedJob jobmodel.DurableJob
	var committedReservation jobmodel.ReservationSet
	var admittedOutput jobs.AdmittedOutput
	err = c.deps.Store.Transaction(nil, func(state *jobmodel.State) error {
		active := activeReservations(*state)
		selected, selectErr := selector.Select(ctx, reservation.SelectionRequest{
			GroupID: jobID,
			Directory: reservation.Volume{
				CanonicalPath: facts.Volume.CanonicalPath,
				Identity:      facts.Volume.Identity,
			},
			Artifacts: declarations,
		}, active)
		if selectErr != nil {
			return selectErr
		}

		committedReservation = toJobReservation(selected)
		var outputErr error
		admittedOutput, outputErr = primaryOutput(committedReservation)
		if outputErr != nil {
			return outputErr
		}
		committedJob = jobmodel.DurableJob{
			ID:           jobID,
			Revision:     1,
			AttemptID:    attemptID,
			SessionID:    sessionID,
			QueueOrdinal: state.NextQueueOrdinal,
			Lifecycle:    jobmodel.LifecyclePending,
			Phase:        jobmodel.PhasePreparing,
			Desired:      jobmodel.DesiredRunning,
			Request:      durableRequest,
			Plan:         durablePlan,
			OutputRoot:   rootRef,
			Reservation:  committedReservation,
			RetryMode:    jobmodel.RetryModeNone,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		state.Jobs = append(state.Jobs, committedJob)
		state.NextQueueOrdinal++
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("admission: commit reservation and job: %w", err)
	}

	queueRequest := request.Queue
	if queueRequest.Quality == "" {
		queueRequest.Quality = jobs.QualityBest
	}
	admittedID, err := c.deps.Queue.SubmitAdmitted(jobID, queueRequest, &plan, admittedOutput)
	if err != nil {
		return Result{Job: committedJob, Plan: plan, Reservation: committedReservation, Artifacts: artifacts}, fmt.Errorf("admission: FIFO manager: %w", err)
	}
	if admittedID != jobID {
		return Result{Job: committedJob, Plan: plan, Reservation: committedReservation, Artifacts: artifacts}, fmt.Errorf("admission: FIFO manager returned job %q for admitted job %q", admittedID, jobID)
	}
	return Result{Job: committedJob, Plan: plan, Reservation: committedReservation, Artifacts: artifacts}, nil
}

func primaryOutput(set jobmodel.ReservationSet) (jobs.AdmittedOutput, error) {
	for _, artifact := range set.Artifacts {
		if artifact.Kind == string(engine.ArtifactKindPrimary) && artifact.Identity == "primary" {
			return jobs.AdmittedOutput{Basename: artifact.Basename}, nil
		}
	}
	return jobs.AdmittedOutput{}, errors.New("admission: reservation has no primary artifact")
}

func validateResolvedPlan(plan outputplan.Plan, requestedID string) error {
	if plan.ID == "" || plan.ID != requestedID || plan.Label == "" || plan.Container == "" || plan.Selector == "" {
		return errors.New("admission: server-resolved output plan is incomplete")
	}
	switch plan.Kind {
	case outputplan.KindVideo, outputplan.KindAudio:
		return nil
	default:
		return errors.New("admission: server-resolved output plan kind is unsupported")
	}
}

func activeReservations(state jobmodel.State) []reservation.ReservationSet {
	claims := make([]reservation.ReservationSet, 0, len(state.Jobs)+len(state.Cleanup))
	for _, job := range state.Jobs {
		if job.Lifecycle == jobmodel.LifecycleCanceled || job.Lifecycle == jobmodel.LifecycleCompleted || len(job.Reservation.Artifacts) == 0 {
			continue
		}
		claims = append(claims, toReservation(job.Reservation))
	}
	for _, tombstone := range state.Cleanup {
		if len(tombstone.Reservation.Artifacts) == 0 {
			continue
		}
		claims = append(claims, toReservation(tombstone.Reservation))
	}
	return claims
}

func toReservation(set jobmodel.ReservationSet) reservation.ReservationSet {
	result := reservation.ReservationSet{
		GroupID: set.GroupID,
		Directory: reservation.Volume{
			CanonicalPath: set.Directory.CanonicalPath,
			Identity:      set.Directory.Identity,
		},
		Artifacts: make([]reservation.ReservedArtifact, len(set.Artifacts)),
	}
	for i, artifact := range set.Artifacts {
		result.Artifacts[i] = reservation.ReservedArtifact{Kind: artifact.Kind, Identity: artifact.Identity, Basename: artifact.Basename}
	}
	return result
}

func toJobReservation(set reservation.ReservationSet) jobmodel.ReservationSet {
	result := jobmodel.ReservationSet{
		GroupID:   set.GroupID,
		Directory: jobmodel.OutputRootRef{CanonicalPath: set.Directory.CanonicalPath, Identity: set.Directory.Identity},
		Artifacts: make([]jobmodel.ReservedArtifact, len(set.Artifacts)),
	}
	for i, artifact := range set.Artifacts {
		result.Artifacts[i] = jobmodel.ReservedArtifact{Kind: artifact.Kind, Identity: artifact.Identity, Basename: artifact.Basename}
	}
	return result
}

func verifyOutputRoot(requested, canonical string) error {
	absolute, err := filepath.Abs(requested)
	if err != nil || filepath.Clean(absolute) != canonical {
		return errors.New("admission: queue output directory does not match reservation root")
	}
	return nil
}

func validateGeneratedIDs(jobID, attemptID, sessionID string) error {
	if jobID == "" || strings.ContainsAny(jobID, `\\/`) || attemptID == "" || strings.ContainsAny(attemptID, `\\/`) {
		return errors.New("admission: generated job identity is invalid")
	}
	if len(sessionID) != 32 {
		return errors.New("admission: generated session identity is invalid")
	}
	for _, character := range sessionID {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return errors.New("admission: generated session identity is invalid")
		}
	}
	return nil
}

func defaultIDs() (string, string, string) {
	return uuid.NewString(), uuid.NewString(), strings.ReplaceAll(uuid.NewString(), "-", "")
}
