// Package jobmodel contains VidStow-owned durable queue DTOs. It deliberately
// has no dependency on the live jobs manager or engine implementation.
package jobmodel

import (
	"fmt"
	"strings"
	"time"
)

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
	// OutputOptions seeds the per-download output choices shown before a
	// download is queued. The zero value keeps VidStow's historical output.
	OutputOptions OutputOptions `json:"outputOptions"`
}

// Subtitle delivery modes. The empty string means no subtitles.
const (
	SubtitleModeSidecar = "sidecar"
	SubtitleModeEmbed   = "embed"
)

// maxSubtitleLanguages bounds the per-request language list well below the
// engine's 64-track embedding ceiling so one download cannot fan out into an
// unreasonable sidecar batch.
const maxSubtitleLanguages = 16

// OutputOptions carries per-download subtitle and embedding preferences. It
// rides on requests, durable jobs, and the settings defaults, so it must stay
// free of engine and UI dependencies. The zero value preserves VidStow's
// historical output: media only, no sidecars, no container metadata.
type OutputOptions struct {
	// SubtitleMode is "" (off), SubtitleModeSidecar, or SubtitleModeEmbed.
	SubtitleMode string `json:"subtitleMode,omitempty"`
	// SubtitleLanguages selects track languages. Empty defers to the engine's
	// default: manual English, then English, then the first language offered.
	SubtitleLanguages []string `json:"subtitleLanguages,omitempty"`
	// SubtitleAutoCaptions allows auto-generated tracks when a selected
	// language has no manual captions.
	SubtitleAutoCaptions bool `json:"subtitleAutoCaptions,omitempty"`
	// SubtitleFormat converts sidecar files to "srt" or "vtt". Empty keeps
	// the source format. Embedding always converts internally.
	SubtitleFormat string `json:"subtitleFormat,omitempty"`
	// EmbedMetadata writes title, channel, and related canonical fields into
	// the media container.
	EmbedMetadata bool `json:"embedMetadata,omitempty"`
	// EmbedThumbnail attaches the artwork to the media container.
	EmbedThumbnail bool `json:"embedThumbnail,omitempty"`
	// EmbedChapters attaches chapter markers. When EmbedMetadata is set and
	// EmbedChapters is not, the engine embeds chapters alongside metadata.
	EmbedChapters bool `json:"embedChapters,omitempty"`
}

// IsZero reports whether every option is at its historical default.
func (o OutputOptions) IsZero() bool {
	return o.Equal(OutputOptions{})
}

// RequiresFFmpeg reports whether any choice needs FFmpeg post-processing:
// embedding subtitles or metadata always remuxes, and sidecar conversion
// re-encodes the subtitle file.
func (o OutputOptions) RequiresFFmpeg() bool {
	return o.SubtitleMode == SubtitleModeEmbed ||
		o.EmbedMetadata || o.EmbedThumbnail || o.EmbedChapters ||
		(o.SubtitleMode == SubtitleModeSidecar && o.SubtitleFormat != "")
}

// Equal compares two option sets, including language order.
func (o OutputOptions) Equal(other OutputOptions) bool {
	if o.SubtitleMode != other.SubtitleMode ||
		o.SubtitleAutoCaptions != other.SubtitleAutoCaptions ||
		o.SubtitleFormat != other.SubtitleFormat ||
		o.EmbedMetadata != other.EmbedMetadata ||
		o.EmbedThumbnail != other.EmbedThumbnail ||
		o.EmbedChapters != other.EmbedChapters ||
		len(o.SubtitleLanguages) != len(other.SubtitleLanguages) {
		return false
	}
	for index, language := range o.SubtitleLanguages {
		if other.SubtitleLanguages[index] != language {
			return false
		}
	}
	return true
}

// Clone returns an independent copy, including the language slice.
func (o OutputOptions) Clone() OutputOptions {
	out := o
	out.SubtitleLanguages = append([]string(nil), o.SubtitleLanguages...)
	return out
}

// Validate enforces the shapes the UI and engine agree on. Language entries
// are deliberately tight (the engine's language rules also accept patterns;
// VidStow only ever sends codes it listed during analysis) so persisted
// requests cannot smuggle rule syntax through State v2.
func (o OutputOptions) Validate() error {
	switch o.SubtitleMode {
	case "", SubtitleModeSidecar, SubtitleModeEmbed:
	default:
		return fmt.Errorf("unsupported subtitle mode %q", o.SubtitleMode)
	}
	switch o.SubtitleFormat {
	case "", "srt", "vtt":
	default:
		return fmt.Errorf("unsupported subtitle format %q", o.SubtitleFormat)
	}
	if len(o.SubtitleLanguages) > maxSubtitleLanguages {
		return fmt.Errorf("too many subtitle languages (max %d)", maxSubtitleLanguages)
	}
	for _, language := range o.SubtitleLanguages {
		if !ValidSubtitleLanguage(language) {
			return fmt.Errorf("invalid subtitle language %q", language)
		}
	}
	return nil
}

// ValidSubtitleLanguage reports whether a language entry is a bounded,
// pattern-free code. VidStow only ever lists codes the engine reported during
// analysis, so anything outside this shape is out of contract.
func ValidSubtitleLanguage(language string) bool {
	if language == "" || len(language) > 16 {
		return false
	}
	for _, r := range language {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Note renders a short, human-readable summary of the non-default choices for
// queue row metadata. It never includes private paths or engine details.
func (o OutputOptions) Note() string {
	parts := make([]string, 0, 2)
	switch o.SubtitleMode {
	case SubtitleModeSidecar:
		parts = append(parts, "subtitles"+o.noteLanguages())
	case SubtitleModeEmbed:
		parts = append(parts, "embedded subtitles"+o.noteLanguages())
	}
	var embeds []string
	if o.EmbedMetadata {
		embeds = append(embeds, "metadata")
	}
	if o.EmbedThumbnail {
		embeds = append(embeds, "thumbnail")
	}
	if o.EmbedChapters {
		embeds = append(embeds, "chapters")
	}
	if len(embeds) > 0 {
		parts = append(parts, "embedded "+strings.Join(embeds, ", "))
	}
	return strings.Join(parts, " · ")
}

func (o OutputOptions) noteLanguages() string {
	if len(o.SubtitleLanguages) == 0 {
		return ""
	}
	suffix := " (" + strings.Join(o.SubtitleLanguages, ", ") + ")"
	if len(suffix) > 48 {
		suffix = suffix[:48] + "…)"
	}
	return suffix
}

type State struct {
	Version          int                 `json:"version"`
	StoreRevision    uint64              `json:"storeRevision"`
	NextQueueOrdinal uint64              `json:"nextQueueOrdinal"`
	Settings         Settings            `json:"settings"`
	Jobs             []DurableJob        `json:"jobs"`
	Collections      []DurableCollection `json:"collections,omitempty"`
	History          []HistoryEntry      `json:"history"`
	Cleanup          []CleanupTombstone  `json:"cleanup"`
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

type DurableCollection struct {
	ID          string    `json:"id"`
	Revision    uint64    `json:"revision"`
	PlaylistID  string    `json:"playlistId"`
	SourceURL   string    `json:"sourceUrl"`
	Title       string    `json:"title"`
	Channel     string    `json:"channel,omitempty"`
	Thumbnail   string    `json:"thumbnail,omitempty"`
	Policy      string    `json:"policy"`
	ChildJobIDs []string  `json:"childJobIds"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type DurableJob struct {
	ID                 string           `json:"id"`
	CollectionID       string           `json:"collectionId,omitempty"`
	CollectionIndex    int              `json:"collectionIndex,omitempty"`
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
	SourceURL     string        `json:"sourceUrl"`
	VideoID       string        `json:"videoId"`
	Title         string        `json:"title"`
	Channel       string        `json:"channel"`
	Quality       string        `json:"quality"`
	PlanID        string        `json:"planId"`
	Duration      string        `json:"duration"`
	OutputOptions OutputOptions `json:"outputOptions,omitempty"`
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
	out.Collections = append([]DurableCollection(nil), in.Collections...)
	for i := range out.Collections {
		out.Collections[i].ChildJobIDs = append([]string(nil), in.Collections[i].ChildJobIDs...)
	}
	for i := range out.Jobs {
		out.Jobs[i].Reservation.Artifacts = append([]ReservedArtifact(nil), in.Jobs[i].Reservation.Artifacts...)
		out.Jobs[i].Request.OutputOptions = in.Jobs[i].Request.OutputOptions.Clone()
	}
	out.History = append([]HistoryEntry(nil), in.History...)
	out.Cleanup = append([]CleanupTombstone(nil), in.Cleanup...)
	for i := range out.Cleanup {
		out.Cleanup[i].Reservation.Artifacts = append([]ReservedArtifact(nil), in.Cleanup[i].Reservation.Artifacts...)
	}
	return out
}
