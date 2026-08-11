// Package jobmodel contains VidStow-owned durable queue DTOs. It deliberately
// has no dependency on the live jobs manager or engine implementation.
package jobmodel

import "time"

const StateVersion = 2

type Lifecycle string

const (
	LifecyclePending        Lifecycle = "pending"
	LifecycleActive         Lifecycle = "active"
	LifecyclePausing        Lifecycle = "pausing"
	LifecyclePaused         Lifecycle = "paused"
	LifecycleCanceling      Lifecycle = "canceling"
	LifecycleFailed         Lifecycle = "failed"
	LifecycleCanceled       Lifecycle = "canceled"
	LifecycleCompleted      Lifecycle = "completed"
	LifecycleActionRequired Lifecycle = "action-required"
)

type Phase string

const (
	PhasePreparing            Phase = "preparing"
	PhaseDownloading          Phase = "downloading"
	PhaseWaitingForProcessing Phase = "waiting-for-processing"
	PhaseFinalizing           Phase = "finalizing"
	PhaseReadyToPublish       Phase = "ready-to-publish"
	PhasePublishing           Phase = "publishing"
	PhaseCleaningUp           Phase = "cleaning-up"
)

type DesiredState string

const (
	DesiredRunning  DesiredState = "running"
	DesiredPaused   DesiredState = "paused"
	DesiredCanceled DesiredState = "canceled"
)

type RetryMode string

const (
	RetryModeNone              RetryMode = "none"
	RetryModeResumeValidated   RetryMode = "resume-validated"
	RetryModeRestartNewSession RetryMode = "restart-new-session"
	RetryModePublishOnly       RetryMode = "publish-only"
)

type CleanupState string

const (
	CleanupPending     CleanupState = "pending"
	CleanupQuarantined CleanupState = "quarantined"
)

// Settings has no restoration preference. Interrupted jobs are restored as
// paused by the product contract, not by preference.
type Settings struct {
	DownloadFolder        string `json:"downloadFolder"`
	FFmpegPath            string `json:"ffmpegPath"`
	WindowWidth           int    `json:"windowWidth"`
	WindowHeight          int    `json:"windowHeight"`
	DownloadConcurrency   int    `json:"downloadConcurrency"`
	PerVideoSubfolder     bool   `json:"perVideoSubfolder"`
	ConfirmBeforeDownload bool   `json:"confirmBeforeDownload"`
}

type State struct {
	Version          int                `json:"version"`
	StoreRevision    uint64             `json:"storeRevision"`
	NextQueueOrdinal uint64             `json:"nextQueueOrdinal"`
	Settings         Settings           `json:"settings"`
	Jobs             []DurableJob       `json:"jobs"`
	History          []HistoryEntry     `json:"history"`
	Cleanup          []CleanupTombstone `json:"cleanup"`
}

// JobPrecondition identifies the exact durable row a lifecycle operation
// observed. Store transactions compare every field before applying a
// mutation, so a stale worker or command cannot overwrite a newer winner.
// It lives beside the durable DTOs so jobs can use State v2 without importing
// store (which still contains the legacy compatibility adapter).
type JobPrecondition struct {
	ID        string
	Revision  uint64
	Lifecycle Lifecycle
	// AttemptID is optional for older admission/store callers, but manager
	// lifecycle transitions always populate it so stale attempt callbacks
	// cannot settle a newer execution under the same logical job.
	AttemptID  string
	SessionID  string
	OutputRoot OutputRootRef
}

type DurableJob struct {
	ID                 string           `json:"id"`
	Revision           uint64           `json:"revision"`
	AttemptID          string           `json:"attemptId"`
	SessionID          string           `json:"sessionId"`
	QueueOrdinal       uint64           `json:"queueOrdinal"`
	Lifecycle          Lifecycle        `json:"lifecycle"`
	Phase              Phase            `json:"phase"`
	Desired            DesiredState     `json:"desired"`
	Request            PersistedRequest `json:"request"`
	Plan               PersistedPlan    `json:"plan"`
	OutputRoot         OutputRootRef    `json:"outputRoot"`
	Reservation        ReservationSet   `json:"reservation"`
	RetryMode          RetryMode        `json:"retryMode"`
	ActionRequiredCode string           `json:"actionRequiredCode,omitempty"`
	LastErrorCode      string           `json:"lastErrorCode,omitempty"`
	CreatedAt          time.Time        `json:"createdAt"`
	UpdatedAt          time.Time        `json:"updatedAt"`
}

// PersistedRequest is deliberately limited to safe, user-originated metadata.
// It must never contain media URLs, headers, cookies, or credentials.
type PersistedRequest struct {
	SourceURL string `json:"sourceUrl"`
	VideoID   string `json:"videoId"`
	Title     string `json:"title"`
	Channel   string `json:"channel"`
	Quality   string `json:"quality"`
	PlanID    string `json:"planId"`
	Duration  string `json:"duration"`
}

type PersistedPlan struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Label          string `json:"label"`
	Container      string `json:"container"`
	VideoCodec     string `json:"videoCodec,omitempty"`
	AudioCodec     string `json:"audioCodec,omitempty"`
	RequiresFFmpeg bool   `json:"requiresFfmpeg,omitempty"`
	// PrivateSelector is an owner-only reviewed format selector. It never
	// crosses the desktop boundary and is rejected if it looks credential-like.
	PrivateSelector string `json:"privateSelector,omitempty"`
}

type OutputRootRef struct {
	CanonicalPath string `json:"canonicalPath"`
	Identity      string `json:"identity,omitempty"`
	// EngineIdentity is the opaque identity format required by the pinned
	// engine facade. Reservationfs owns Identity; keeping both avoids making
	// one subsystem reinterpret another subsystem's platform token.
	EngineIdentity string `json:"engineIdentity,omitempty"`
}

type ReservationSet struct {
	GroupID   string             `json:"groupId"`
	Directory OutputRootRef      `json:"directory"`
	Artifacts []ReservedArtifact `json:"artifacts"`
}

type ReservedArtifact struct {
	Kind     string `json:"kind"`
	Identity string `json:"identity"`
	Basename string `json:"basename"`
}

type CleanupTombstone struct {
	JobID         string         `json:"jobId"`
	SessionID     string         `json:"sessionId"`
	OutputRoot    OutputRootRef  `json:"outputRoot"`
	Reservation   ReservationSet `json:"reservation"`
	State         CleanupState   `json:"state"`
	LastErrorCode string         `json:"lastErrorCode,omitempty"`
	CreatedAt     time.Time      `json:"createdAt"`
	UpdatedAt     time.Time      `json:"updatedAt"`
}

type HistoryEntry struct {
	ID            string `json:"id"`
	VideoID       string `json:"videoId"`
	Title         string `json:"title"`
	Channel       string `json:"channel"`
	Quality       string `json:"quality"`
	Container     string `json:"container,omitempty"`
	VideoCodec    string `json:"videoCodec,omitempty"`
	AudioCodec    string `json:"audioCodec,omitempty"`
	Filename      string `json:"filename"`
	AbsolutePath  string `json:"absolutePath"`
	SizeBytes     int64  `json:"sizeBytes"`
	CompletedAt   string `json:"completedAt"`
	DurationLabel string `json:"durationLabel"`
}

// CloneState returns a complete, independent copy for transactional mutation.
func CloneState(in State) State {
	out := in
	out.Jobs = append([]DurableJob(nil), in.Jobs...)
	for i := range out.Jobs {
		out.Jobs[i].Reservation.Artifacts = append([]ReservedArtifact(nil), in.Jobs[i].Reservation.Artifacts...)
	}
	out.History = append([]HistoryEntry(nil), in.History...)
	out.Cleanup = append([]CleanupTombstone(nil), in.Cleanup...)
	for i := range out.Cleanup {
		out.Cleanup[i].Reservation.Artifacts = append([]ReservedArtifact(nil), in.Cleanup[i].Reservation.Artifacts...)
	}
	return out
}
