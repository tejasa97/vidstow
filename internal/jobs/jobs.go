// Package jobs manages the desktop app's download queue. It serialises
// downloads (one active at a time, FIFO waiting) and bridges events
// from the focused engine composition into app-friendly JobSnapshot values the
// UI renders.
//
// The package deliberately keeps state in memory; persistence of
// completed downloads is handled by the store package.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/tejasa97/vidstow/internal/jobmodel"
	"github.com/tejasa97/vidstow/internal/outputplan"
	"github.com/tejasa97/vidstow/internal/reservation"
	"github.com/tejasa97/vidstow/internal/reservationfs"
	"github.com/tejasa97/youtube_dlp/engine"
	provideryoutube "github.com/tejasa97/youtube_dlp/providers/youtube"
)

// Quality is the user-visible quality preset. The string values are
// stable and part of the UI contract.
type Quality string

const (
	QualityBest      Quality = "best"
	Quality4K        Quality = "4k"
	Quality1440p     Quality = "1440p"
	Quality1080p     Quality = "1080p"
	Quality720p      Quality = "720p"
	QualityAudioOnly Quality = "audio"
)

const (
	DefaultDownloadConcurrency = 2
	MaxDownloadConcurrency     = 10
	MaxProcessingConcurrency   = 3
	MaxPlaylistEntries         = 500
	maxCachedPlaylistPreviews  = 32
	DefaultShutdownTimeout     = 3 * time.Second
)

// ErrClosed is returned when an operation would start new manager activity
// after Close has begun.
var ErrClosed = errors.New("jobs: manager is closed")

var errCancelRequested = errors.New("jobs: cancel requested")

var errActivationSuperseded = errors.New("jobs: durable activation was superseded")

// StateStore is the manager-facing State v2 seam. The precondition type is
// deliberately owned by jobmodel so jobs can use the durable authority
// without importing the store package (which retains legacy UI adapters).
type StateStore interface {
	Snapshot() jobmodel.State
	Transaction([]jobmodel.JobPrecondition, func(*jobmodel.State) error) error
}

// commitOutcome is the narrow Store transaction outcome contract. Keeping it
// here avoids coupling the manager to the concrete store package while still
// making uncertainty a fail-closed queue condition.
type commitOutcome interface {
	Committed() bool
	Indeterminate() bool
}

// AllQualities is the fixed V0 quality list.
var AllQualities = []Quality{
	QualityBest, Quality4K, Quality1440p, Quality1080p, Quality720p, QualityAudioOnly,
}

// Label returns the user-visible label for a quality.
func (q Quality) Label() string {
	switch q {
	case QualityBest:
		return "Best"
	case Quality4K:
		return "4K"
	case Quality1440p:
		return "1440p"
	case Quality1080p:
		return "1080p"
	case Quality720p:
		return "720p"
	case QualityAudioOnly:
		return "Audio only"
	}
	return string(q)
}

// ytdlpFormat returns the core ytdlp format selector for a preset.
func (q Quality) ytdlpFormat() string {
	switch q {
	case QualityBest:
		return "bv*+ba/b"
	case Quality4K:
		return "bv*[height<=2160]+ba/b[height<=2160]"
	case Quality1440p:
		return "bv*[height<=1440]+ba/b[height<=1440]"
	case Quality1080p:
		return "bv*[height<=1080]+ba/b[height<=1080]"
	case Quality720p:
		return "bv*[height<=720]+ba/b[height<=720]"
	case QualityAudioOnly:
		return "ba/b"
	}
	return "b"
}

// outputTemplate keeps artifacts for different V0 presets distinct. A video
// and audio-only download of the same title may share an extension, so the
// default title-only template would otherwise let the later job overwrite the
// earlier successful artifact.
func (q Quality) outputTemplate() string {
	return fmt.Sprintf("%%(title)s [%%(id)s] [%s].%%(ext)s", q.Label())
}

// OutputTemplateForPlan is the exact basename template used by a curated
// output plan. Admission uses the same template before choosing a durable
// reservation, so the queue and the engine do not drift on filenames.
func OutputTemplateForPlan(plan outputplan.Plan) string {
	label := plan.Label
	if label == "" {
		label = plan.ID
	}
	return fmt.Sprintf("%%(title)s [%%(id)s] [%s].%%(ext)s", label)
}

// Status is the lifecycle state of a job.
type Status string

const (
	StatusPending        Status = "pending"
	StatusActive         Status = "active"
	StatusPausing        Status = "pausing"
	StatusCanceling      Status = "canceling"
	StatusPaused         Status = "paused"
	StatusComplete       Status = "complete"
	StatusFailed         Status = "failed"
	StatusCanceled       Status = "canceled"
	StatusActionRequired Status = "action-required"
)

// OutputOptions re-exports the shared per-download output preferences so
// callers can build jobs requests without importing the durable model.
type OutputOptions = jobmodel.OutputOptions

// Request is the data needed to schedule one download.
type Request struct {
	URL       string  `json:"url"`
	VideoID   string  `json:"videoId"`
	Title     string  `json:"title"`
	Channel   string  `json:"channel"`
	Quality   Quality `json:"quality"`
	PlanID    string  `json:"planId"`
	OutputDir string  `json:"outputDir"`
	Duration  string  `json:"duration"`
	Thumbnail string  `json:"thumbnail"`
	// Options carries this download's subtitle and embedding choices. The
	// zero value keeps the historical media-only output.
	Options OutputOptions `json:"options,omitempty"`
}

// AdmittedOutput is the internal admission-to-manager contract. The basename
// comes from the committed reservation and is used as a literal engine output
// template for this attempt; it is not part of the UI request model.
type AdmittedOutput struct {
	Basename string
}

// JobSnapshot is the immutable view of a job exposed to the UI.
type JobSnapshot struct {
	ID              string                `json:"id"`
	URL             string                `json:"url"`
	VideoID         string                `json:"videoID"`
	Title           string                `json:"title"`
	Channel         string                `json:"channel"`
	Quality         Quality               `json:"quality"`
	QualityLabel    string                `json:"qualityLabel"`
	PlanID          string                `json:"planId,omitempty"`
	OutputKind      outputplan.Kind       `json:"outputKind,omitempty"`
	Container       string                `json:"container,omitempty"`
	VideoCodec      string                `json:"videoCodec,omitempty"`
	AudioCodec      string                `json:"audioCodec,omitempty"`
	ApproxBytes     int64                 `json:"approxBytes,omitempty"`
	SizeApproximate bool                  `json:"sizeApproximate,omitempty"`
	RequiresFFmpeg  bool                  `json:"requiresFfmpeg,omitempty"`
	CanPause        bool                  `json:"canPause,omitempty"`
	Processing      bool                  `json:"processing,omitempty"`
	OutputDir       string                `json:"outputDir"`
	DurationLabel   string                `json:"durationLabel"`
	Thumbnail       string                `json:"thumbnail"`
	Status          Status                `json:"status"`
	Lifecycle       jobmodel.Lifecycle    `json:"lifecycle,omitempty"`
	Phase           jobmodel.Phase        `json:"phase,omitempty"`
	Desired         jobmodel.DesiredState `json:"desired,omitempty"`
	OccupiesSlot    bool                  `json:"occupiesSlot"`
	CreatedAt       string                `json:"createdAt"`
	StartedAt       string                `json:"startedAt,omitempty"`
	CompletedAt     string                `json:"completedAt,omitempty"`
	Bytes           int64                 `json:"bytes"`
	Total           int64                 `json:"total"`
	Progress        float64               `json:"progress"`
	SpeedBps        float64               `json:"speedBps"`
	ETASeconds      float64               `json:"etaSeconds"`
	Filename        string                `json:"filename"`
	AbsolutePath    string                `json:"absolutePath"`
	Message         string                `json:"message"`
	ErrorReason     string                `json:"errorReason,omitempty"`
	// OptionsNote is a backend-authored summary of non-default output
	// options (subtitles, embedded metadata) shown in queue row metadata.
	OptionsNote string `json:"optionsNote,omitempty"`
}

// QueueJobCapabilities is deliberately backend-authored. The frontend must
// only render an action when its positive flag and the row's opaque command
// token are both present; it must never recreate lifecycle rules locally.
type QueueJobCapabilities struct {
	Pause         bool `json:"pause"`
	Cancel        bool `json:"cancel"`
	Resume        bool `json:"resume"`
	Retry         bool `json:"retry"`
	DownloadAgain bool `json:"downloadAgain"`
	StartAgain    bool `json:"startAgain"`
	OpenSource    bool `json:"openSource"`
	CopyLink      bool `json:"copyLink"`
	Review        bool `json:"review"`
	Open          bool `json:"open"`
	Remove        bool `json:"remove"`
}

// QueueFailure is stable, backend-authored failure copy. Retryable explains
// the category but never grants authority; Capabilities remains authoritative.
type QueueFailure struct {
	Category          string `json:"category"`
	MessageKey        string `json:"messageKey"`
	Heading           string `json:"heading"`
	Message           string `json:"message"`
	RecommendedAction string `json:"recommendedAction"`
	Retryable         bool   `json:"retryable"`
	PartialOutput     bool   `json:"partialOutput"`
}

// QueueRow is the safe frontend projection of a job. Lifecycle, phase,
// desired state, and occupancy intentionally remain independent facts.
type QueueRow struct {
	ID              string                `json:"id"`
	CollectionID    string                `json:"collectionId,omitempty"`
	CollectionIndex int                   `json:"collectionIndex,omitempty"`
	Title           string                `json:"title"`
	Metadata        string                `json:"metadata,omitempty"`
	ThumbnailURL    string                `json:"thumbnailUrl,omitempty"`
	Lifecycle       jobmodel.Lifecycle    `json:"lifecycle"`
	Phase           jobmodel.Phase        `json:"phase,omitempty"`
	Desired         jobmodel.DesiredState `json:"desired"`
	OccupiesSlot    bool                  `json:"occupiesSlot"`
	QueuePosition   int                   `json:"queuePosition,omitempty"`
	Progress        float64               `json:"progress,omitempty"`
	ProgressLabel   string                `json:"progressLabel,omitempty"`
	SpeedLabel      string                `json:"speedLabel,omitempty"`
	ETALabel        string                `json:"etaLabel,omitempty"`
	Message         string                `json:"message,omitempty"`
	Failure         *QueueFailure         `json:"failure,omitempty"`
	Capabilities    QueueJobCapabilities  `json:"capabilities"`
	CommandToken    string                `json:"commandToken,omitempty"`
}

type QueueCollectionCapabilities struct {
	Pause  bool `json:"pause"`
	Cancel bool `json:"cancel"`
	Resume bool `json:"resume"`
	Retry  bool `json:"retry"`
	Remove bool `json:"remove"`
}

type QueueCollection struct {
	ID            string                      `json:"id"`
	Kind          jobmodel.CollectionKind     `json:"kind"`
	Title         string                      `json:"title"`
	Metadata      string                      `json:"metadata,omitempty"`
	ThumbnailURL  string                      `json:"thumbnailUrl,omitempty"`
	Policy        string                      `json:"policy"`
	ChildJobIDs   []string                    `json:"childJobIds"`
	Total         int                         `json:"total"`
	Completed     int                         `json:"completed"`
	Failed        int                         `json:"failed"`
	Canceled      int                         `json:"canceled"`
	Active        int                         `json:"active"`
	Pending       int                         `json:"pending"`
	Paused        int                         `json:"paused"`
	Progress      float64                     `json:"progress"`
	ProgressLabel string                      `json:"progressLabel"`
	Capabilities  QueueCollectionCapabilities `json:"capabilities"`
	CommandToken  string                      `json:"commandToken,omitempty"`
}

type QueueSummary struct {
	TotalJobs          int `json:"totalJobs"`
	RunningJobs        int `json:"runningJobs"`
	OccupiedSlots      int `json:"occupiedSlots"`
	SlotLimit          int `json:"slotLimit"`
	ProcessingOccupied int `json:"processingOccupied"`
	ProcessingLimit    int `json:"processingLimit"`
	WaitingJobs        int `json:"waitingJobs"`
	PausedJobs         int `json:"pausedJobs"`
}

type QueueCapabilities struct {
	PauseAll       bool   `json:"pauseAll"`
	ClearCompleted bool   `json:"clearCompleted"`
	CommandToken   string `json:"commandToken,omitempty"`
}

// ActionRequiredReview is presentation-safe, backend-authored guidance for an
// evidence-bearing row. It deliberately excludes paths, engine details, and
// the source URL; the latter is released only by a second authorized command.
type ActionRequiredReview struct {
	JobID              string `json:"jobId"`
	Title              string `json:"title"`
	Heading            string `json:"heading"`
	Message            string `json:"message"`
	PreservationNotice string `json:"preservationNotice"`
	CanStartOver       bool   `json:"canStartOver"`
	CanRetryRecovery   bool   `json:"canRetryRecovery"`
	CanRetryFreshLink  bool   `json:"canRetryFreshLink"`
	CanDiscard         bool   `json:"canDiscard"`
	CanRetryCleanup    bool   `json:"canRetryCleanup"`
}

// QueueView is the only live queue contract consumed by the V4 frontend.
// It includes all aggregate facts so Svelte does not infer authority or
// occupancy from a legacy status string.
type QueueView struct {
	Revision     uint64            `json:"revision"`
	Rows         []QueueRow        `json:"rows"`
	Collections  []QueueCollection `json:"collections"`
	Summary      QueueSummary      `json:"summary"`
	Capabilities QueueCapabilities `json:"capabilities"`
	Persistence  PersistenceStatus `json:"persistence"`
}

// Listener receives lifecycle events. Every event is delivered by one
// dispatcher so job and queue snapshots retain their causal order.
type Listener func(event Event)

// Event names match the Wails events emitted by the app layer.
const (
	EventJobUpdate   = "job:update"
	EventQueue       = "queue:update"
	EventPersistence = "persistence:update"
)

// Event is a single lifecycle notification.
type Event struct {
	Name        string             `json:"name"`
	Job         JobSnapshot        `json:"job"`
	Queue       []JobSnapshot      `json:"queue"`
	QueueView   *QueueView         `json:"queueView,omitempty"`
	Persistence *PersistenceStatus `json:"persistence,omitempty"`
	// Diagnostic is an allowlisted terminal failure classification. It never
	// contains an error, URL, path, filename, or other user data.
	Diagnostic *Diagnostic `json:"-"`
}

// Diagnostic is emitted only when an admitted download reaches a terminal
// failure. App owns recording/transmission so jobs remains transport-agnostic.
type Diagnostic struct {
	Stage    string
	Category string
	Duration time.Duration
}

// PersistenceStatus reports whether the durable queue can currently be
// saved. Message is intentionally generic: persistence errors can contain
// private filesystem paths, which must never cross the desktop boundary.
type PersistenceStatus struct {
	Available bool   `json:"available"`
	Healthy   bool   `json:"healthy"`
	Message   string `json:"message,omitempty"`
}

// QuitSummary is the backend-authored payload for the native close
// confirmation. Waiting and paused rows are already safe and never count as
// occupied slots.
type QuitSummary struct {
	ActiveDownloads          int `json:"activeDownloads"`
	WaitingOrPausedDownloads int `json:"waitingOrPausedDownloads"`
}

const (
	persistenceFailureMessage     = "VidStow could not save the download queue. Check available disk space and permissions."
	persistenceUnavailableMessage = "VidStow is using temporary in-memory storage. Download queue changes will not be saved after the app closes."
)

// PersistedJob contains the non-terminal state needed to restore a queue.
// PrivateSelector never crosses the Wails boundary; it is stored only in the
// app's owner-only state file so a resumed job uses the exact analyzed format.
type PersistedJob struct {
	Snapshot        JobSnapshot     `json:"snapshot"`
	Plan            outputplan.Plan `json:"plan,omitempty"`
	PrivateSelector string          `json:"privateSelector,omitempty"`
}

type Persistence interface {
	LoadJobs() ([]PersistedJob, error)
	SaveJobs([]PersistedJob) error
}

// DurablePersistence is an optional capability for stores that can retain
// queue data after this process exits. Implementations that do not expose it
// are treated as durable for backwards compatibility.
type DurablePersistence interface {
	Durable() bool
}

// Manager owns the queue and the client used for metadata analysis. Downloads
// use short-lived clients so each job can attach its own event handler.
type Manager struct {
	client               *engine.Client
	listener             Listener
	eventSignal          chan struct{}
	eventMu              sync.Mutex
	pendingEvents        []Event
	eventStop            chan struct{}
	eventDone            chan struct{}
	eventClosed          bool
	closeOnce            sync.Once
	closeDone            chan struct{}
	closeErr             error
	closing              bool
	closed               bool
	lifecycleCtx         context.Context
	lifecycleCancel      context.CancelCauseFunc
	analysisWG           sync.WaitGroup
	detachedWG           sync.WaitGroup
	runDownload          downloadRunner
	runAnalyze           analyzeRunner
	inspectResume        resumeInspector
	prepareResumeDiscard resumeDiscardPreparer
	ffmpegLocation       string
	mu                   sync.Mutex
	all                  map[string]*jobState
	order                []string
	active               map[string]*worker
	concurrency          int
	processing           chan struct{}
	queueRevision        uint64
	queueCommandToken    string
	queueAuthoritySig    string
	stateStore           StateStore
	planCache            map[string]cachedPlans
	playlistCache        map[string]cachedPlaylist
	collectionAuthority  map[string]collectionAuthority
	collectionCommanding map[string]bool
	persistence          Persistence
	persistenceDurable   bool
	persistMu            sync.Mutex
	persistStatus        PersistenceStatus
	persistSignal        chan struct{}
	persistStop          chan struct{}
	persistDone          chan struct{}
}

type cachedPlans struct {
	plans     []outputplan.Plan
	expiresAt time.Time
}

type collectionAuthority struct {
	signature string
	token     string
}

type cachedPlaylist struct {
	summary   PlaylistSummary
	expiresAt time.Time
}

type downloadRunner func(context.Context, engine.Request, engine.EventHandler) (engine.Result, error)

type analyzeRunner func(context.Context, engine.Request) (engine.Result, error)

type resumeInspector func(context.Context, engine.OutputRootRef, string) (engine.ResumeSummary, error)

type resumeDiscardPreparer func(context.Context, engine.OutputRootRef, string) (*engine.ResumeDiscardHandle, error)

// worker is runtime-only attempt state. It is never restored from State v2;
// active is the sole live occupancy authority and retains this value until
// the runner and any session cleanup have exited.
type worker struct {
	JobID     string
	AttemptID string
	SessionID string
	Cancel    context.CancelCauseFunc
	Ctx       context.Context
	Arbiter   *engine.PublicationArbiter
	Done      chan struct{}
}

type jobState struct {
	snap               JobSnapshot
	plan               *outputplan.Plan
	outputTemplate     string
	options            jobmodel.OutputOptions
	worker             *worker
	done               chan struct{}
	durable            jobmodel.DurableJob
	fromStateV2        bool
	settling           bool
	commanding         bool
	transitionMu       sync.Mutex
	commandToken       string
	authoritySig       string
	authorityRevision  uint64
	authorityAttemptID string
	startBps           time.Time
	startByt           int64
}

// New creates a Manager. listener may be nil for headless tests.
func New(client *engine.Client, listener Listener) *Manager {
	// One composition owns the bounded in-memory EJS preprocessing cache used
	// by every short-lived download client created by this manager. Individual
	// helpers remain isolated and are still closed when their job finishes.
	composition := provideryoutube.NewComposition()
	if client == nil {
		client = newFocusedClientWithComposition(composition)
	}
	lifecycleCtx, lifecycleCancel := context.WithCancelCause(context.Background())
	manager := &Manager{
		client:               client,
		listener:             listener,
		closeDone:            make(chan struct{}),
		eventDone:            make(chan struct{}),
		lifecycleCtx:         lifecycleCtx,
		lifecycleCancel:      lifecycleCancel,
		runDownload:          downloadRunnerForComposition(composition),
		runAnalyze:           client.Run,
		inspectResume:        engine.InspectResumeState,
		prepareResumeDiscard: engine.PrepareResumeDiscard,
		all:                  make(map[string]*jobState),
		active:               make(map[string]*worker),
		concurrency:          DefaultDownloadConcurrency,
		processing:           make(chan struct{}, MaxProcessingConcurrency),
		queueCommandToken:    uuid.NewString(),
		planCache:            make(map[string]cachedPlans),
		playlistCache:        make(map[string]cachedPlaylist),
		collectionAuthority:  make(map[string]collectionAuthority),
		collectionCommanding: make(map[string]bool),
	}
	if listener != nil {
		manager.eventSignal = make(chan struct{}, 1)
		manager.eventStop = make(chan struct{})
		go manager.dispatchEvents()
	} else {
		close(manager.eventDone)
	}
	return manager
}

// SetStateStore attaches the State v2 authority used by admitted jobs and
// lifecycle transitions. It is intentionally separate from the legacy
// Persistence seam so a manager cannot accidentally write a second queue
// document while a V2 store is active.
func (m *Manager) SetStateStore(stateStore StateStore) error {
	if stateStore == nil {
		return errors.New("jobs: nil State v2 store")
	}
	snapshot := stateStore.Snapshot()
	if snapshot.Version != jobmodel.StateVersion {
		return errors.New("jobs: invalid State v2 store")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing || m.closed {
		return ErrClosed
	}
	if m.stateStore != nil {
		return errors.New("jobs: State v2 store already configured")
	}
	m.stateStore = stateStore
	m.persistStatus = PersistenceStatus{Available: true, Healthy: true}
	if durable, ok := stateStore.(DurablePersistence); ok && !durable.Durable() {
		m.persistStatus = PersistenceStatus{Available: false, Healthy: true, Message: persistenceUnavailableMessage}
	}
	return nil
}

// RestoreStateV2 reconstructs the existing FIFO manager from a committed,
// already-reconciled State v2 snapshot. It deliberately does not enqueue or
// start anything: startup restoration is always paused and active is empty.
func (m *Manager) RestoreStateV2(snapshot jobmodel.State) error {
	if snapshot.Version != jobmodel.StateVersion {
		return errors.New("jobs: invalid State v2 restore snapshot")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing || m.closed {
		return ErrClosed
	}
	if m.stateStore == nil {
		return errors.New("jobs: State v2 store is not configured")
	}
	if len(m.all) != 0 || len(m.active) != 0 || len(m.order) != 0 {
		return errors.New("jobs: manager already contains queue state")
	}
	for _, durable := range snapshot.Jobs {
		switch durable.Lifecycle {
		case jobmodel.LifecyclePending, jobmodel.LifecycleActive, jobmodel.LifecyclePausing, jobmodel.LifecycleCanceling:
			return fmt.Errorf("jobs: unreconciled transitional job %q", durable.ID)
		}
		state, err := stateFromDurable(durable)
		if err != nil {
			return err
		}
		m.all[durable.ID] = state
	}
	m.persistStatus = PersistenceStatus{Available: true, Healthy: true}
	return nil
}

func stateFromDurable(durable jobmodel.DurableJob) (*jobState, error) {
	status := StatusPaused
	switch durable.Lifecycle {
	case jobmodel.LifecyclePaused:
		status = StatusPaused
	case jobmodel.LifecycleFailed:
		status = StatusFailed
	case jobmodel.LifecycleCanceled:
		status = StatusCanceled
	case jobmodel.LifecycleCompleted:
		status = StatusComplete
	case jobmodel.LifecycleActionRequired:
		status = StatusActionRequired
	default:
		return nil, fmt.Errorf("jobs: unsupported restored lifecycle %q", durable.Lifecycle)
	}
	plan := &outputplan.Plan{
		ID: durable.Plan.ID, Kind: outputplan.Kind(durable.Plan.Kind), Label: durable.Plan.Label,
		Container: durable.Plan.Container, VideoCodec: durable.Plan.VideoCodec,
		AudioCodec: durable.Plan.AudioCodec, RequiresFFmpeg: durable.Plan.RequiresFFmpeg,
		Selector: durable.Plan.PrivateSelector, Available: true,
	}
	if plan.ID == "" && plan.Selector == "" {
		plan = nil
	}
	filename := ""
	for _, artifact := range durable.Reservation.Artifacts {
		if artifact.Kind == string(engine.ArtifactKindPrimary) && artifact.Identity == "primary" {
			filename = artifact.Basename
			break
		}
	}
	absolutePath := ""
	if durable.OutputRoot.CanonicalPath != "" && filename != "" {
		absolutePath = filepath.Join(durable.OutputRoot.CanonicalPath, filename)
	}
	quality := Quality(durable.Request.Quality)
	if quality == "" {
		quality = QualityBest
	}
	snapshot := JobSnapshot{
		ID: durable.ID, URL: durable.Request.SourceURL, VideoID: durable.Request.VideoID,
		Title: durable.Request.Title, Channel: durable.Request.Channel, Quality: quality,
		QualityLabel: durable.Plan.Label, PlanID: durable.Plan.ID, OutputKind: outputplan.Kind(durable.Plan.Kind),
		Container: durable.Plan.Container, VideoCodec: durable.Plan.VideoCodec, AudioCodec: durable.Plan.AudioCodec,
		RequiresFFmpeg: durable.Plan.RequiresFFmpeg, OutputDir: durable.OutputRoot.CanonicalPath,
		DurationLabel: durable.Request.Duration, Status: status, Lifecycle: durable.Lifecycle,
		Phase: durable.Phase, Desired: durable.Desired, OccupiesSlot: false, CreatedAt: durable.CreatedAt.UTC().Format(time.RFC3339Nano),
		Filename: filename, AbsolutePath: absolutePath, ErrorReason: durable.LastErrorCode,
		OptionsNote: durable.Request.OutputOptions.Note(),
	}
	switch status {
	case StatusPaused:
		snapshot.Message = "Paused after app restart"
	case StatusFailed:
		snapshot.Message = "Failed"
		if durable.LastErrorCode == retryCodeFreshDownloadRequired {
			snapshot.Message = freshDownloadRequiredNotice
		}
	case StatusCanceled:
		snapshot.Message = "Canceled"
	case StatusComplete:
		snapshot.Message = "Completed"
		snapshot.Progress = 1
		snapshot.CompletedAt = durable.UpdatedAt.UTC().Format(time.RFC3339Nano)
	case StatusActionRequired:
		snapshot.Message = "Action required"
	}
	outputTemplate := ""
	if filename != "" {
		var err error
		outputTemplate, err = literalOutputTemplate(filename)
		if err != nil {
			return nil, err
		}
	}
	return &jobState{snap: snapshot, plan: plan, outputTemplate: outputTemplate, options: durable.Request.OutputOptions.Clone(), done: make(chan struct{}), durable: durable, fromStateV2: true, commandToken: uuid.NewString(), authorityRevision: durable.Revision, authorityAttemptID: durable.AttemptID}, nil
}

func (m *Manager) stateStoreSnapshot() StateStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stateStore
}

func (m *Manager) transaction(store StateStore, preconditions []jobmodel.JobPrecondition, mutate func(*jobmodel.State) error) error {
	err := store.Transaction(preconditions, mutate)
	if err == nil {
		return nil
	}
	var outcome commitOutcome
	if errors.As(err, &outcome) {
		m.revokePersistenceAuthority()
	}
	return err
}

func (m *Manager) revokePersistenceAuthority() {
	m.mu.Lock()
	if !m.persistStatus.Available && !m.persistStatus.Healthy {
		m.mu.Unlock()
		return
	}
	m.persistStatus = PersistenceStatus{Available: true, Healthy: false, Message: persistenceFailureMessage}
	status := m.persistStatus
	m.emitLocked(Event{Name: EventPersistence, Persistence: &status})
	m.emitQueueLocked()
	m.mu.Unlock()
}

// commitDurable applies one lifecycle mutation against the exact durable row
// observed by this job state. The per-job mutex serializes commands with the
// terminal worker callback, while the store precondition rejects any stale
// attempt that lost a cross-process or same-process race.
func (m *Manager) commitDurable(state *jobState, mutate func(*jobmodel.DurableJob, *jobmodel.State) error) error {
	if state == nil || !state.fromStateV2 {
		return nil
	}
	if mutate == nil {
		return errors.New("jobs: nil durable mutation")
	}
	state.transitionMu.Lock()
	defer state.transitionMu.Unlock()
	store := m.stateStoreSnapshot()
	if store == nil {
		return errors.New("jobs: State v2 store is not configured")
	}
	expected := state.durable
	precondition := jobmodel.JobPrecondition{
		ID:         expected.ID,
		Revision:   expected.Revision,
		Lifecycle:  expected.Lifecycle,
		AttemptID:  expected.AttemptID,
		SessionID:  expected.SessionID,
		OutputRoot: expected.OutputRoot,
	}
	var committed jobmodel.DurableJob
	err := m.transaction(store, []jobmodel.JobPrecondition{precondition}, func(document *jobmodel.State) error {
		for index := range document.Jobs {
			if document.Jobs[index].ID != expected.ID {
				continue
			}
			candidate := &document.Jobs[index]
			if err := mutate(candidate, document); err != nil {
				return err
			}
			if candidate.Revision == ^uint64(0) {
				return errors.New("jobs: durable job revision exhausted")
			}
			candidate.Revision++
			candidate.UpdatedAt = time.Now().UTC()
			committed = *candidate
			return nil
		}
		return errors.New("jobs: durable job disappeared")
	})
	if err != nil {
		return err
	}
	m.mu.Lock()
	state.durable = committed
	if m.all[state.snap.ID] == state {
		state.authorityRevision = committed.Revision
		state.authorityAttemptID = committed.AttemptID
		state.snap.Lifecycle = committed.Lifecycle
		state.snap.Phase = committed.Phase
		state.snap.Desired = committed.Desired
		state.snap.OccupiesSlot = m.active[state.snap.ID] != nil
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) removeDurable(state *jobState) error {
	if state == nil || !state.fromStateV2 {
		return nil
	}
	state.transitionMu.Lock()
	defer state.transitionMu.Unlock()
	stateStore := m.stateStoreSnapshot()
	if stateStore == nil {
		return errors.New("jobs: State v2 store is not configured")
	}
	expected := state.durable
	return m.transaction(stateStore, []jobmodel.JobPrecondition{{
		ID: expected.ID, Revision: expected.Revision, Lifecycle: expected.Lifecycle,
		AttemptID: expected.AttemptID, SessionID: expected.SessionID, OutputRoot: expected.OutputRoot,
	}}, func(document *jobmodel.State) error {
		for index, job := range document.Jobs {
			if job.ID == expected.ID {
				if err := removeCollectionChild(document, job, time.Now().UTC()); err != nil {
					return err
				}
				document.Jobs = append(document.Jobs[:index], document.Jobs[index+1:]...)
				return nil
			}
		}
		return errors.New("jobs: durable job disappeared")
	})
}

func removeCollectionChild(document *jobmodel.State, job jobmodel.DurableJob, now time.Time) error {
	if job.CollectionID == "" {
		return nil
	}
	for collectionIndex := range document.Collections {
		collection := &document.Collections[collectionIndex]
		if collection.ID != job.CollectionID {
			continue
		}
		childIndex := -1
		for index, childID := range collection.ChildJobIDs {
			if childID == job.ID {
				childIndex = index
				break
			}
		}
		if childIndex < 0 {
			return errors.New("jobs: durable collection does not contain child")
		}
		if len(collection.ChildJobIDs) > 1 && collection.Revision == ^uint64(0) {
			return errors.New("jobs: durable collection revision exhausted")
		}
		collection.ChildJobIDs = append(collection.ChildJobIDs[:childIndex], collection.ChildJobIDs[childIndex+1:]...)
		if len(collection.ChildJobIDs) == 0 {
			document.Collections = append(document.Collections[:collectionIndex], document.Collections[collectionIndex+1:]...)
			return nil
		}
		collection.Revision++
		collection.UpdatedAt = now
		return nil
	}
	return errors.New("jobs: durable collection disappeared")
}

func (m *Manager) transitionDurable(state *jobState, lifecycle jobmodel.Lifecycle, desired jobmodel.DesiredState, phase jobmodel.Phase) error {
	if durableStateAlready(state, lifecycle, desired, phase) {
		return nil
	}
	return m.commitDurable(state, func(job *jobmodel.DurableJob, _ *jobmodel.State) error {
		job.Lifecycle = lifecycle
		job.Desired = desired
		job.Phase = phase
		return nil
	})
}

func durableStateAlready(state *jobState, lifecycle jobmodel.Lifecycle, desired jobmodel.DesiredState, phase jobmodel.Phase) bool {
	if state == nil || !state.fromStateV2 {
		return false
	}
	state.transitionMu.Lock()
	defer state.transitionMu.Unlock()
	return state.durable.Lifecycle == lifecycle && state.durable.Desired == desired && state.durable.Phase == phase
}

func resumeCommitTargets(set jobmodel.ReservationSet) []engine.CommitTarget {
	targets := make([]engine.CommitTarget, len(set.Artifacts))
	for index, artifact := range set.Artifacts {
		targets[index] = engine.CommitTarget{
			Kind:     engine.ArtifactKind(artifact.Kind),
			Identity: artifact.Identity,
			Basename: artifact.Basename,
		}
	}
	return targets
}

func historyFromSnapshot(snap JobSnapshot) jobmodel.HistoryEntry {
	completed := snap.CompletedAt
	if completed == "" {
		completed = time.Now().UTC().Format(time.RFC3339Nano)
	}
	quality := string(snap.Quality)
	if snap.QualityLabel != "" {
		quality = snap.QualityLabel
	}
	return jobmodel.HistoryEntry{
		ID:            snap.ID,
		VideoID:       snap.VideoID,
		Title:         snap.Title,
		Channel:       snap.Channel,
		Quality:       quality,
		Container:     snap.Container,
		VideoCodec:    snap.VideoCodec,
		AudioCodec:    snap.AudioCodec,
		Filename:      snap.Filename,
		AbsolutePath:  snap.AbsolutePath,
		SizeBytes:     snap.Bytes,
		CompletedAt:   completed,
		DurationLabel: snap.DurationLabel,
	}
}

// upsertJobCleanupTombstone records cleanup-pending evidence for one session
// of a job. Entries are keyed by both job and session identity so a later
// cancel cannot clobber an unrelated session's tombstone.
func upsertJobCleanupTombstone(document *jobmodel.State, jobID, sessionID string, outputRoot jobmodel.OutputRootRef, reservation jobmodel.ReservationSet, errorCode string, now time.Time) {
	for index := range document.Cleanup {
		if document.Cleanup[index].JobID == jobID && document.Cleanup[index].SessionID == sessionID {
			document.Cleanup[index].OutputRoot = outputRoot
			document.Cleanup[index].Reservation = reservation
			document.Cleanup[index].State = jobmodel.CleanupPending
			document.Cleanup[index].LastErrorCode = errorCode
			document.Cleanup[index].UpdatedAt = now
			return
		}
	}
	document.Cleanup = append(document.Cleanup, jobmodel.CleanupTombstone{
		JobID:         jobID,
		SessionID:     sessionID,
		OutputRoot:    outputRoot,
		Reservation:   reservation,
		State:         jobmodel.CleanupPending,
		LastErrorCode: errorCode,
		CreatedAt:     now,
		UpdatedAt:     now,
	})
}

// settleDurable commits the terminal lifecycle and, where required, the
// cleanup tombstone or completion history in the same State transaction.
// The row precondition is the attempt's latest accepted revision, so a stale
// callback can only fail and never replace a newer winner.
// failureCommittedBytes carries the checkpoint evidence a failed attempt
// could read: -1 means unknown (non-v2 job or unreadable session) and leaves
// previously recorded failure bookkeeping untouched.
func (m *Manager) settleDurable(state *jobState, lifecycle jobmodel.Lifecycle, desired jobmodel.DesiredState, phase jobmodel.Phase, snap JobSnapshot, errorCode string, cleanupPending bool, failureCommittedBytes int64) error {
	if durableStateAlready(state, lifecycle, desired, phase) {
		return nil
	}
	return m.commitDurable(state, func(job *jobmodel.DurableJob, document *jobmodel.State) error {
		job.Lifecycle = lifecycle
		job.Desired = desired
		job.Phase = phase
		job.LastErrorCode = errorCode
		if lifecycle == jobmodel.LifecycleActionRequired {
			job.ActionRequiredCode = errorCode
		} else {
			job.ActionRequiredCode = ""
		}
		if lifecycle == jobmodel.LifecycleCompleted {
			entry := historyFromSnapshot(snap)
			found := false
			for _, existing := range document.History {
				if existing.ID == entry.ID {
					found = true
					break
				}
			}
			if !found {
				document.History = append([]jobmodel.HistoryEntry{entry}, document.History...)
			}
		}
		if lifecycle == jobmodel.LifecycleFailed && failureCommittedBytes >= 0 {
			if failureCommittedBytes > 0 && job.LastFailureCommittedBytes == failureCommittedBytes {
				job.ZeroProgressResumes++
			} else {
				job.ZeroProgressResumes = 0
			}
			job.LastFailureCommittedBytes = failureCommittedBytes
		}
		if lifecycle == jobmodel.LifecycleCanceled && cleanupPending {
			upsertJobCleanupTombstone(document, job.ID, job.SessionID, job.OutputRoot, job.Reservation, errorCode, time.Now().UTC())
		}
		return nil
	})
}

func (m *Manager) discardSession(state *jobState) (bool, string) {
	if state == nil {
		return false, ""
	}
	return m.discardKnownSession(state, state.durable.SessionID)
}

// discardRetiredSession attempts immediate cleanup after Retry has atomically
// rotated the durable row and recorded the old session as cleanup-pending.
// Failure leaves that tombstone for the startup/periodic cleanup worker; only
// a proven discard removes it.
func (m *Manager) discardRetiredSession(state *jobState, sessionID string) {
	cleanupPending, _ := m.discardKnownSession(state, sessionID)
	if cleanupPending {
		if state != nil {
			logRetiredSessionLeak("vidstow: retired session cleanup remains pending for job %s and will be retried from durable state", state.snap.ID)
		}
		return
	}
	if state == nil || !state.fromStateV2 {
		return
	}
	store := m.stateStoreSnapshot()
	if store == nil {
		return
	}
	_ = m.transaction(store, nil, func(document *jobmodel.State) error {
		for index, tombstone := range document.Cleanup {
			if tombstone.JobID == state.snap.ID && tombstone.SessionID == sessionID {
				document.Cleanup = append(document.Cleanup[:index], document.Cleanup[index+1:]...)
				break
			}
		}
		return nil
	})
}

func (m *Manager) discardKnownSession(state *jobState, sessionID string) (bool, string) {
	handle, unavailable, err := m.prepareDiscardSession(state, sessionID)
	if err != nil {
		return true, "cleanup"
	}
	if unavailable || handle == nil {
		return false, ""
	}
	result, discardErr := handle.Discard(context.Background())
	if discardErr != nil || result.Disposition != engine.ResumeDiscarded {
		return true, "cleanup"
	}
	return false, ""
}

// prepareDiscardSession acquires the engine's no-local-runner cleanup handle
// before State mutation. A missing workspace is a valid no-op: there is no
// session evidence to tombstone. Any other failure is retained as cleanup
// evidence and never silently treated as a successful discard.
func (m *Manager) prepareDiscardSession(state *jobState, sessionID string) (*engine.ResumeDiscardHandle, bool, error) {
	if state == nil || !state.fromStateV2 || sessionID == "" || state.durable.OutputRoot.CanonicalPath == "" {
		return nil, true, nil
	}
	handle, err := m.prepareResumeDiscard(context.Background(), engineRootRef(state.durable.OutputRoot), sessionID)
	if err != nil {
		if strings.Contains(err.Error(), "workspace unavailable") {
			return nil, true, nil
		}
		return nil, false, err
	}
	return handle, false, nil
}

func engineRootRef(root jobmodel.OutputRootRef) engine.OutputRootRef {
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

func downloadRunnerForComposition(composition engine.Composition) downloadRunner {
	return func(ctx context.Context, req engine.Request, handler engine.EventHandler) (engine.Result, error) {
		client := newFocusedClientWithComposition(composition, engine.WithEventHandler(handler))
		defer client.Close()
		return client.Run(ctx, req)
	}
}

// newFocusedClient is the standalone Desktop composition factory used by
// tests and one-off callers. When Manager owns its analysis client, it uses
// newFocusedClientWithComposition so analysis and per-download clients share
// one bounded in-memory EJS cache.
func newFocusedClient(options ...engine.Option) *engine.Client {
	return newFocusedClientWithComposition(provideryoutube.NewComposition(), options...)
}

func newFocusedClientWithComposition(composition engine.Composition, options ...engine.Option) *engine.Client {
	return engine.NewClient(composition, options...)
}

// Close stops new manager activity and joins manager-owned work only until
// the supplied deadline. With no context it uses the product shutdown bound.
// It is idempotent; concurrent callers receive the same final error.
func (m *Manager) Close(contexts ...context.Context) error {
	ctx := context.Background()
	if len(contexts) > 0 && contexts[0] != nil {
		ctx = contexts[0]
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultShutdownTimeout)
		defer cancel()
	}
	m.closeOnce.Do(func() {
		m.closeErr = m.close(ctx)
		close(m.closeDone)
	})
	<-m.closeDone
	return m.closeErr
}

func (m *Manager) close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	m.closing = true
	if m.lifecycleCancel != nil {
		m.lifecycleCancel(context.Canceled)
	}
	workerDone := make([]<-chan struct{}, 0, len(m.active))
	for id, worker := range m.active {
		state := m.all[id]
		if state == nil {
			continue
		}
		state.snap.CanPause = false
		if worker != nil && worker.Cancel != nil {
			worker.Cancel(engine.ErrPauseRequested)
		}
		if worker != nil && worker.Done != nil {
			workerDone = append(workerDone, worker.Done)
		} else if state.done != nil {
			workerDone = append(workerDone, state.done)
		}
	}
	m.mu.Unlock()

	var closeErrs []error
	for _, finished := range workerDone {
		select {
		case <-finished:
		case <-ctx.Done():
			closeErrs = append(closeErrs, ctx.Err())
			finished = nil
		}
		if finished == nil {
			break
		}
	}
	analysisDone := make(chan struct{})
	go func() {
		m.analysisWG.Wait()
		m.detachedWG.Wait()
		close(analysisDone)
	}()
	select {
	case <-analysisDone:
	case <-ctx.Done():
		closeErrs = append(closeErrs, ctx.Err())
	}
	if ctx.Err() != nil {
		return errors.Join(closeErrs...)
	}

	m.mu.Lock()
	stop, persistDone := m.persistStop, m.persistDone
	m.persistStop = nil
	m.persistSignal = nil
	m.mu.Unlock()

	if stop != nil {
		close(stop)
		select {
		case <-persistDone:
		case <-ctx.Done():
			closeErrs = append(closeErrs, ctx.Err())
		}
	}
	// Stop the debounce loop before the final write. State v2 terminal
	// transitions are already committed by the manager; the legacy path keeps
	// its final compatibility flush when the shared deadline permits it.
	if !m.usingStateV2() && ctx.Err() == nil {
		flushDone := make(chan error, 1)
		go func() { flushDone <- m.FlushPersistence() }()
		select {
		case err := <-flushDone:
			if err != nil {
				closeErrs = append(closeErrs, err)
			}
		case <-ctx.Done():
			closeErrs = append(closeErrs, ctx.Err())
		}
	}
	if err := m.stopDispatcher(ctx); err != nil {
		closeErrs = append(closeErrs, err)
	}
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	if m.client != nil {
		m.client.Close()
	}
	return errors.Join(closeErrs...)
}

func (m *Manager) usingStateV2() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stateStore != nil
}

// SetPersistence attaches durable queue storage and restores prior jobs.
func (m *Manager) SetPersistence(persistence Persistence, restoreInterrupted bool) error {
	if persistence == nil {
		return errors.New("jobs: nil persistence")
	}
	stored, err := persistence.LoadJobs()
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	if m.stateStore != nil {
		m.mu.Unlock()
		return errors.New("jobs: legacy persistence cannot be used with State v2")
	}
	if m.persistence != nil {
		m.mu.Unlock()
		return errors.New("jobs: persistence already configured")
	}
	m.persistence = persistence
	m.persistenceDurable = persistenceIsDurable(persistence)
	m.persistStatus = persistenceStatus(m.persistenceDurable, nil)
	m.persistSignal = make(chan struct{}, 1)
	m.persistStop = make(chan struct{})
	m.persistDone = make(chan struct{})
	if restoreInterrupted {
		m.restoreLocked(stored)
	}
	if !m.persistenceDurable {
		status := m.persistStatus
		m.emitLocked(Event{Name: EventPersistence, Persistence: &status})
	}
	signal, stop, done := m.persistSignal, m.persistStop, m.persistDone
	go m.persistLoop(signal, stop, done)
	m.mu.Unlock()
	if !restoreInterrupted {
		return m.FlushPersistence()
	}
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	m.maybeStartNextLocked()
	m.emitQueueLocked()
	m.schedulePersistLocked()
	m.mu.Unlock()
	return nil
}

func (m *Manager) restoreLocked(stored []PersistedJob) {
	for _, persisted := range stored {
		snap := persisted.Snapshot
		if snap.ID == "" || snap.URL == "" || snap.OutputDir == "" {
			continue
		}
		switch snap.Status {
		case StatusActive:
			snap.Status = StatusPaused
			snap.Message = "Paused after app restart"
		case StatusPending, StatusPaused:
		default:
			continue
		}
		state := &jobState{snap: snap, done: make(chan struct{})}
		if persisted.PrivateSelector != "" {
			plan := persisted.Plan
			plan.Selector = persisted.PrivateSelector
			state.plan = &plan
		}
		m.all[snap.ID] = state
		if snap.Status == StatusPending {
			m.order = append(m.order, snap.ID)
		}
	}
}

func (m *Manager) persistLoop(signal <-chan struct{}, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-signal:
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-timer.C:
				m.FlushPersistence()
			case <-stop:
				if !timer.Stop() {
					<-timer.C
				}
				return
			}
		case <-stop:
			return
		}
	}
}

func (m *Manager) schedulePersistLocked() {
	if m.persistSignal == nil {
		return
	}
	select {
	case m.persistSignal <- struct{}{}:
	default:
	}
}

func (m *Manager) persistenceSnapshotLocked() []PersistedJob {
	result := make([]PersistedJob, 0, len(m.all))
	for _, state := range m.all {
		switch state.snap.Status {
		case StatusPending, StatusActive, StatusPaused:
		default:
			continue
		}
		persisted := PersistedJob{Snapshot: state.snap}
		if state.plan != nil {
			persisted.Plan = *state.plan
			persisted.PrivateSelector = state.plan.Selector
		}
		result = append(result, persisted)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Snapshot.CreatedAt < result[j].Snapshot.CreatedAt
	})
	return result
}

// FlushPersistence writes a consistent queue snapshot and records an
// app-visible health update. Saves are serialized because shutdown can race a
// debounce flush. The returned error lets callers log a final shutdown failure.
func (m *Manager) FlushPersistence() error {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()

	// Snapshot, write, and status transition are one serialized transaction.
	// The lock order is persistMu -> m.mu everywhere, so an older snapshot can
	// never write after a newer flush or replace its health status.
	m.mu.Lock()
	persistence := m.persistence
	durable := m.persistenceDurable
	jobs := m.persistenceSnapshotLocked()
	m.mu.Unlock()
	if persistence == nil {
		return nil
	}
	err := persistence.SaveJobs(jobs)
	m.recordPersistenceResult(durable, err)
	if err != nil {
		return errors.New(persistenceFailureMessage)
	}
	return nil
}

// dispatchEvents drains a nonblocking, lossless mailbox. It is deliberately
// unbounded: listeners perform completion side effects such as writing
// download history, so dropping or coalescing events would corrupt behavior.
// The production Wails listener must remain fast and nonblocking; emitters
// never hold the manager lock while waiting for it. eventStop is separate from
// eventSignal because emitters may still be sending wakeups while shutdown
// marks the mailbox closed.
func (m *Manager) dispatchEvents() {
	defer close(m.eventDone)
	for {
		select {
		case <-m.eventSignal:
			m.drainEvents()
		case <-m.eventStop:
			m.drainEvents()
			return
		}
	}
}

func (m *Manager) drainEvents() {
	for {
		m.eventMu.Lock()
		if len(m.pendingEvents) == 0 {
			// Do not retain references through the backing array after a drain.
			m.pendingEvents = nil
			m.eventMu.Unlock()
			return
		}
		event := m.pendingEvents[0]
		m.pendingEvents[0] = Event{}
		m.pendingEvents = m.pendingEvents[1:]
		m.eventMu.Unlock()
		m.listener(event)
	}
}

func (m *Manager) stopDispatcher(ctx context.Context) error {
	if m.listener == nil {
		return nil
	}
	m.eventMu.Lock()
	m.eventClosed = true
	m.eventMu.Unlock()
	close(m.eventStop)
	select {
	case <-m.eventDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// PersistenceStatus returns the current durable-queue health. It is safe for
// the UI to poll at startup in case an early write failed before it subscribed.
func (m *Manager) PersistenceStatus() PersistenceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.persistStatus
}

func (m *Manager) recordPersistenceResult(durable bool, err error) {
	next := persistenceStatus(durable, err)
	if err != nil && durable {
		// Persistence errors can contain a user-specific filesystem path. The
		// generic log entry confirms the fault without disclosing it.
		log.Printf("vidstow: queue persistence failed")
		next.Message = persistenceFailureMessage
	}
	m.mu.Lock()
	changed := m.persistStatus != next
	m.persistStatus = next
	if changed {
		status := next
		// A final flush runs after the manager stops accepting new activity, but
		// its health transition is still part of the ordered shutdown event
		// stream and must be delivered before Close returns.
		m.enqueueEvent(Event{Name: EventPersistence, Persistence: &status})
	}
	m.mu.Unlock()
}

func persistenceIsDurable(persistence Persistence) bool {
	capability, ok := persistence.(DurablePersistence)
	return !ok || capability.Durable()
}

func persistenceStatus(durable bool, err error) PersistenceStatus {
	if !durable {
		if err != nil {
			return PersistenceStatus{Available: false, Healthy: false, Message: persistenceFailureMessage}
		}
		return PersistenceStatus{Available: false, Healthy: err == nil, Message: persistenceUnavailableMessage}
	}
	if err != nil {
		return PersistenceStatus{Available: true, Healthy: false, Message: persistenceFailureMessage}
	}
	return PersistenceStatus{Available: true, Healthy: true}
}

// SetFFmpegLocation updates the per-request tool location. It is deliberately
// scoped to future requests so the desktop process never mutates PATH while a
// download is already running.
func (m *Manager) SetFFmpegLocation(path string) {
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return
	}
	m.ffmpegLocation = strings.TrimSpace(path)
	m.mu.Unlock()
}

func (m *Manager) ffmpegLocationSnapshot() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ffmpegLocation
}

// Submit enqueues a new job and returns its id. The job starts as soon
// as it reaches the head of the FIFO queue.
func (m *Manager) Submit(req Request) (string, error) {
	if m.stateStoreSnapshot() != nil {
		return "", errors.New("jobs: State v2 admission is required")
	}
	return m.submit(uuid.NewString(), req, nil, nil)
}

// SubmitAdmitted enqueues a job whose durable admission transaction has
// already committed. The supplied id is preserved so the live FIFO row and
// the State v2 row refer to the same logical job. The manager still owns all
// existing validation, queue ordering, occupancy, and worker startup rules.
// Callers must pass the exact plan used to render and reserve the output.
func (m *Manager) SubmitAdmitted(id string, req Request, selectedPlan *outputplan.Plan, output AdmittedOutput) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", errors.New("jobs: admitted job id is required")
	}
	if selectedPlan == nil || strings.TrimSpace(req.PlanID) == "" {
		return "", errors.New("jobs: admitted output plan is required")
	}
	outputTemplate, err := literalOutputTemplate(output.Basename)
	if err != nil {
		return "", err
	}
	if stateStore := m.stateStoreSnapshot(); stateStore != nil {
		var durable jobmodel.DurableJob
		found := false
		for _, candidate := range stateStore.Snapshot().Jobs {
			if candidate.ID == id {
				durable = candidate
				found = true
				break
			}
		}
		if !found || durable.Lifecycle != jobmodel.LifecyclePending {
			return "", errors.New("jobs: admitted durable job is not pending in State v2")
		}
		if req.URL != durable.Request.SourceURL || req.VideoID != durable.Request.VideoID || req.Title != durable.Request.Title || req.PlanID != durable.Request.PlanID {
			return "", errors.New("jobs: admitted request does not match State v2")
		}
		if !req.Options.Equal(durable.Request.OutputOptions) {
			return "", errors.New("jobs: admitted output options do not match State v2")
		}
		if durable.Plan.ID != selectedPlan.ID || durable.Plan.PrivateSelector != selectedPlan.Selector {
			return "", errors.New("jobs: admitted plan does not match State v2")
		}
		if durable.Plan.Label != selectedPlan.Label || durable.Plan.Container != selectedPlan.Container || durable.Plan.Kind != string(selectedPlan.Kind) {
			return "", errors.New("jobs: admitted plan metadata does not match State v2")
		}
		reservedBasename := ""
		for _, artifact := range durable.Reservation.Artifacts {
			if artifact.Kind == string(engine.ArtifactKindPrimary) && artifact.Identity == "primary" {
				reservedBasename = artifact.Basename
				break
			}
		}
		if reservedBasename == "" || reservedBasename != output.Basename {
			return "", errors.New("jobs: admitted basename does not match State v2 reservation")
		}
		if req.Quality == "" {
			req.Quality = Quality(durable.Request.Quality)
		}
	}
	return m.submit(id, req, selectedPlan, &outputTemplate)
}

func (m *Manager) submit(id string, req Request, admittedPlan *outputplan.Plan, admittedOutputTemplate *string) (string, error) {
	if req.URL == "" {
		return "", errors.New("jobs: empty url")
	}
	var selectedPlan *outputplan.Plan
	if admittedPlan != nil {
		if req.PlanID == "" || admittedPlan.ID != req.PlanID {
			return "", errors.New("jobs: admitted output plan does not match request")
		}
		plan := *admittedPlan
		selectedPlan = &plan
	} else if req.PlanID != "" {
		plan, err := m.ResolvePlan(req.VideoID, req.PlanID)
		if err != nil {
			return "", err
		}
		selectedPlan = &plan
	} else if req.Quality == "" {
		req.Quality = QualityBest
	}
	if selectedPlan == nil && !isKnownQuality(req.Quality) {
		return "", fmt.Errorf("jobs: unsupported quality %q", req.Quality)
	}
	if req.OutputDir == "" {
		return "", errors.New("jobs: empty output directory")
	}
	if err := req.Options.Validate(); err != nil {
		return "", fmt.Errorf("jobs: %w", err)
	}
	if err := ensureDir(req.OutputDir); err != nil {
		return "", fmt.Errorf("jobs: prepare output dir: %w", err)
	}

	var durable jobmodel.DurableJob
	fromStateV2 := false
	if admittedPlan != nil {
		if stateStore := m.stateStoreSnapshot(); stateStore != nil {
			snapshot := stateStore.Snapshot()
			for _, candidate := range snapshot.Jobs {
				if candidate.ID == id {
					durable = candidate
					fromStateV2 = true
					break
				}
			}
			if !fromStateV2 {
				return "", errors.New("jobs: admitted durable job is missing from State v2")
			}
			if durable.Lifecycle != jobmodel.LifecyclePending {
				return "", errors.New("jobs: admitted durable job is not pending")
			}
			requestedRoot, rootErr := filepath.Abs(req.OutputDir)
			if rootErr != nil || filepath.Clean(requestedRoot) != durable.OutputRoot.CanonicalPath {
				return "", errors.New("jobs: admitted output root does not match State v2")
			}
		}
	}
	state := &jobState{
		snap: JobSnapshot{
			ID:            id,
			URL:           req.URL,
			VideoID:       req.VideoID,
			Title:         req.Title,
			Channel:       req.Channel,
			Quality:       req.Quality,
			QualityLabel:  req.Quality.Label(),
			OutputDir:     req.OutputDir,
			DurationLabel: req.Duration,
			Thumbnail:     req.Thumbnail,
			Status:        StatusPending,
			CreatedAt:     time.Now().UTC().Format(time.RFC3339),
			OptionsNote:   req.Options.Note(),
		},
		plan:               selectedPlan,
		outputTemplate:     "",
		options:            req.Options.Clone(),
		done:               make(chan struct{}),
		durable:            durable,
		fromStateV2:        fromStateV2,
		commandToken:       uuid.NewString(),
		authorityRevision:  durable.Revision,
		authorityAttemptID: durable.AttemptID,
	}
	if admittedOutputTemplate != nil {
		state.outputTemplate = *admittedOutputTemplate
	}
	if selectedPlan != nil {
		state.snap.PlanID = selectedPlan.ID
		state.snap.QualityLabel = selectedPlan.Label
		state.snap.OutputKind = selectedPlan.Kind
		state.snap.Container = selectedPlan.Container
		state.snap.VideoCodec = selectedPlan.VideoCodec
		state.snap.AudioCodec = selectedPlan.AudioCodec
		state.snap.ApproxBytes = selectedPlan.ApproxBytes
		state.snap.SizeApproximate = selectedPlan.SizeIsApproximate
		state.snap.RequiresFFmpeg = selectedPlan.RequiresFFmpeg
	}
	if fromStateV2 {
		state.snap.Lifecycle = durable.Lifecycle
		state.snap.Phase = durable.Phase
		state.snap.Desired = durable.Desired
	}

	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return "", ErrClosed
	}
	if _, exists := m.all[id]; exists {
		m.mu.Unlock()
		return "", errors.New("jobs: duplicate admitted job id")
	}
	m.all[id] = state
	m.order = append(m.order, id)
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	m.maybeStartNextLocked()
	m.emitQueueLocked()
	m.mu.Unlock()
	return id, nil
}

// List returns a copy of every job snapshot in display order
// (active first, then queued, then terminal jobs).
func (m *Manager) List() []JobSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

// QueueView returns the complete, backend-authored queue contract. It is a
// snapshot: callers must echo the opaque token attached to a row or queue
// action, and stale/missing tokens are rejected by the command methods.
func (m *Manager) QueueView() QueueView {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queueViewLocked()
}

// RefreshDurableMaintenance emits a fresh QueueView after the recovery worker
// changes cleanup tombstones outside the manager's lifecycle transactions.
func (m *Manager) RefreshDurableMaintenance() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing || m.closed {
		return
	}
	m.emitQueueLocked()
}

func (m *Manager) queueViewLocked() QueueView {
	m.refreshQueueAuthorityLocked()
	view := QueueView{
		Revision:     m.queueRevision,
		Rows:         make([]QueueRow, 0, len(m.all)),
		Collections:  []QueueCollection{},
		Summary:      QueueSummary{SlotLimit: m.concurrency, ProcessingLimit: MaxProcessingConcurrency},
		Capabilities: QueueCapabilities{CommandToken: m.queueCommandToken},
		Persistence:  m.persistStatus,
	}
	if view.Persistence == (PersistenceStatus{}) {
		view.Persistence = PersistenceStatus{Available: false, Healthy: false, Message: persistenceUnavailableMessage}
	}
	positions := make(map[string]int, len(m.order))
	for index, id := range m.order {
		positions[id] = index + 1
	}
	for _, snap := range m.snapshotLocked() {
		state := m.all[snap.ID]
		if state == nil {
			continue
		}
		lifecycle := snap.Lifecycle
		if lifecycle == "" {
			lifecycle = lifecycleForStatus(snap.Status)
		}
		row := QueueRow{
			ID: snap.ID, CollectionID: state.durable.CollectionID, CollectionIndex: state.durable.CollectionIndex,
			Title: snap.Title, Metadata: queueMetadata(snap), ThumbnailURL: queueThumbnailURL(snap),
			Lifecycle: lifecycle, Phase: snap.Phase, Desired: snap.Desired,
			OccupiesSlot: m.active[snap.ID] != nil, QueuePosition: positions[snap.ID],
			Progress: snap.Progress, Message: snap.Message,
			Capabilities: m.queueCapabilitiesLocked(state, snap),
		}
		if lifecycle == jobmodel.LifecycleFailed {
			failure := queueFailureFor(state, snap)
			row.Failure = &failure
		}
		if present, quarantined := m.cleanupStatusLocked(snap.ID); present {
			switch snap.Status {
			case StatusCanceled, StatusFailed, StatusComplete:
				if quarantined {
					row.Message = "Temporary data needs attention and was preserved."
				} else {
					row.Message = "Cleaning up saved temporary data automatically…"
				}
			}
		}
		if row.Capabilities != (QueueJobCapabilities{}) {
			row.CommandToken = state.commandToken
		}
		if lifecycle == jobmodel.LifecycleCompleted {
			row.Progress = 1
			row.ProgressLabel = "100%"
		} else if snap.Total > 0 {
			row.Progress = snap.Progress
			row.ProgressLabel = fmt.Sprintf("%.0f%%", snap.Progress*100)
		}
		if lifecycle != jobmodel.LifecycleCompleted {
			row.SpeedLabel = queueSpeedLabel(snap.SpeedBps)
			row.ETALabel = queueETALabel(snap.ETASeconds)
		}
		view.Rows = append(view.Rows, row)
		view.Summary.TotalJobs++
		if row.OccupiesSlot {
			view.Summary.OccupiedSlots++
		}
		if snap.Processing && row.OccupiesSlot {
			view.Summary.ProcessingOccupied++
		}
		switch lifecycle {
		case jobmodel.LifecycleActive, jobmodel.LifecyclePausing, jobmodel.LifecycleCanceling:
			view.Summary.RunningJobs++
		case jobmodel.LifecyclePending:
			view.Summary.WaitingJobs++
		case jobmodel.LifecyclePaused:
			view.Summary.PausedJobs++
		}
	}
	view.Collections = m.queueCollectionsLocked(view.Rows)
	for _, row := range view.Rows {
		view.Capabilities.PauseAll = view.Capabilities.PauseAll || row.Capabilities.Pause
		view.Capabilities.ClearCompleted = view.Capabilities.ClearCompleted || (row.Lifecycle == jobmodel.LifecycleCompleted && row.Capabilities.Remove)
	}
	if !view.Capabilities.PauseAll && !view.Capabilities.ClearCompleted {
		view.Capabilities.CommandToken = ""
	}
	return view
}

func (m *Manager) queueCollectionsLocked(rows []QueueRow) []QueueCollection {
	if m.stateStore == nil {
		return []QueueCollection{}
	}
	durable := m.stateStore.Snapshot()
	rowsByID := make(map[string]QueueRow, len(rows))
	for _, row := range rows {
		rowsByID[row.ID] = row
	}
	collections := make([]QueueCollection, 0, len(durable.Collections))
	liveAuthority := make(map[string]struct{}, len(durable.Collections))
	for _, parent := range durable.Collections {
		kind := parent.Kind
		if kind == "" {
			kind = jobmodel.CollectionKindPlaylist
		}
		collection := QueueCollection{
			ID: parent.ID, Kind: kind, Title: parent.Title, ThumbnailURL: parent.Thumbnail, Policy: parent.Policy,
			ChildJobIDs: append([]string(nil), parent.ChildJobIDs...), Total: len(parent.ChildJobIDs),
		}
		if kind == jobmodel.CollectionKindBatch {
			label := "videos"
			if collection.Total == 1 {
				label = "video"
			}
			collection.Title = fmt.Sprintf("Batch download · %d %s", collection.Total, label)
		}
		if parent.Channel != "" {
			collection.Metadata = parent.Channel
		}
		removeAll := len(parent.ChildJobIDs) > 0
		progress := 0.0
		signatureParts := make([]string, 0, len(parent.ChildJobIDs)+1)
		signatureParts = append(signatureParts, fmt.Sprintf("%d", parent.Revision))
		for _, childID := range parent.ChildJobIDs {
			row, ok := rowsByID[childID]
			state := m.all[childID]
			if !ok || state == nil {
				removeAll = false
				signatureParts = append(signatureParts, childID+"=missing")
				continue
			}
			caps := row.Capabilities
			collection.Capabilities.Pause = collection.Capabilities.Pause || caps.Pause
			collection.Capabilities.Cancel = collection.Capabilities.Cancel || caps.Cancel
			collection.Capabilities.Resume = collection.Capabilities.Resume || caps.Resume
			collection.Capabilities.Retry = collection.Capabilities.Retry || caps.Retry
			removeAll = removeAll && caps.Remove
			switch row.Lifecycle {
			case jobmodel.LifecycleCompleted:
				collection.Completed++
				progress += 1
			case jobmodel.LifecycleFailed, jobmodel.LifecycleActionRequired:
				collection.Failed++
			case jobmodel.LifecycleCanceled:
				collection.Canceled++
			case jobmodel.LifecycleActive, jobmodel.LifecyclePausing, jobmodel.LifecycleCanceling:
				collection.Active++
				progress += row.Progress
			case jobmodel.LifecyclePending:
				collection.Pending++
			case jobmodel.LifecyclePaused:
				collection.Paused++
				progress += row.Progress
			}
			signatureParts = append(signatureParts, childID+"="+state.commandToken)
		}
		collection.Capabilities.Remove = removeAll
		if m.collectionCommanding[parent.ID] {
			collection.Capabilities = QueueCollectionCapabilities{}
		}
		if collection.Total > 0 {
			collection.Progress = progress / float64(collection.Total)
			collection.ProgressLabel = fmt.Sprintf("%d of %d complete", collection.Completed, collection.Total)
		}
		signature := strings.Join(signatureParts, "|") + fmt.Sprintf("|%t%t%t%t%t", collection.Capabilities.Pause, collection.Capabilities.Cancel, collection.Capabilities.Resume, collection.Capabilities.Retry, collection.Capabilities.Remove)
		authority := m.collectionAuthority[parent.ID]
		if authority.signature != signature {
			authority = collectionAuthority{signature: signature, token: uuid.NewString()}
			m.collectionAuthority[parent.ID] = authority
		}
		if collection.Capabilities != (QueueCollectionCapabilities{}) {
			collection.CommandToken = authority.token
		}
		liveAuthority[parent.ID] = struct{}{}
		collections = append(collections, collection)
	}
	for id := range m.collectionAuthority {
		if _, exists := liveAuthority[id]; !exists {
			delete(m.collectionAuthority, id)
			delete(m.collectionCommanding, id)
		}
	}
	return collections
}

// refreshQueueAuthorityLocked binds opaque tokens to the exact set of facts
// that authorizes an action. It runs before every QueueView is attached to an
// event, including job:update, so an intermediate lifecycle view never
// carries a token issued for an earlier capability set. Progress telemetry is
// intentionally absent from these signatures and therefore preserves tokens.
func (m *Manager) refreshQueueAuthorityLocked() {
	positions := make(map[string]int, len(m.order))
	for index, id := range m.order {
		positions[id] = index + 1
	}
	rows := make([]string, 0, len(m.all))
	for id, state := range m.all {
		if state == nil {
			continue
		}
		caps := m.queueCapabilitiesLocked(state, state.snap)
		cleanupPresent, cleanupQuarantined := m.cleanupStatusLocked(id)
		sig := fmt.Sprintf("%s|%s|%s|%s|%d|%t|%d|%t|%t|%t|%t|%t|%t|%t|%t|%t|%t|%t|%t|%t",
			state.snap.Status, state.snap.Lifecycle, state.snap.Phase, state.snap.Desired,
			state.authorityRevision, m.active[id] != nil, positions[id], m.closing, m.closed,
			caps.Pause, caps.Cancel, caps.Resume, caps.Retry, caps.DownloadAgain, caps.StartAgain,
			caps.OpenSource, caps.CopyLink, caps.Review, caps.Open, caps.Remove,
		)
		// Attempt identity and cleanup disposition are authority boundaries even
		// when the presentation lifecycle happens to be unchanged.
		sig += fmt.Sprintf("|%s|%t|%t", state.authorityAttemptID, cleanupPresent, cleanupQuarantined)
		if state.authoritySig != sig {
			state.commandToken = uuid.NewString()
			state.authoritySig = sig
		}
		rows = append(rows, id+"="+sig)
	}
	sort.Strings(rows)
	queueSig := fmt.Sprintf("%t|%t|%t|%t|%s", m.persistStatus.Available, m.persistStatus.Healthy, m.closing, m.closed, strings.Join(rows, ";"))
	if m.queueAuthoritySig != queueSig {
		m.queueCommandToken = uuid.NewString()
		m.queueAuthoritySig = queueSig
	}
}

func lifecycleForStatus(status Status) jobmodel.Lifecycle {
	switch status {
	case StatusPending:
		return jobmodel.LifecyclePending
	case StatusActive:
		return jobmodel.LifecycleActive
	case StatusPausing:
		return jobmodel.LifecyclePausing
	case StatusPaused:
		return jobmodel.LifecyclePaused
	case StatusCanceling:
		return jobmodel.LifecycleCanceling
	case StatusFailed:
		return jobmodel.LifecycleFailed
	case StatusCanceled:
		return jobmodel.LifecycleCanceled
	case StatusComplete:
		return jobmodel.LifecycleCompleted
	case StatusActionRequired:
		return jobmodel.LifecycleActionRequired
	default:
		return jobmodel.LifecycleActionRequired
	}
}

func queueThumbnailURL(snap JobSnapshot) string {
	if snap.Thumbnail != "" {
		return snap.Thumbnail
	}
	if len(snap.VideoID) != 11 {
		return ""
	}
	for _, r := range snap.VideoID {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ""
		}
	}
	return "https://i.ytimg.com/vi/" + snap.VideoID + "/hqdefault.jpg"
}

func queueMetadata(snap JobSnapshot) string {
	parts := []string{snap.Channel, snap.DurationLabel, snap.QualityLabel, snap.OptionsNote}
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			result = append(result, part)
		}
	}
	return strings.Join(result, " · ")
}

func queueSpeedLabel(bytesPerSecond float64) string {
	if bytesPerSecond <= 0 || math.IsNaN(bytesPerSecond) || math.IsInf(bytesPerSecond, 0) {
		return ""
	}
	units := []string{"B/s", "KiB/s", "MiB/s", "GiB/s"}
	value := bytesPerSecond
	unit := 0
	for unit < len(units)-1 && value >= 1024 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%.0f %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

func queueETALabel(seconds float64) string {
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return ""
	}
	// Do not surface an unbounded counter from malformed or stale telemetry.
	remaining := int64(math.Min(math.Ceil(seconds), 99*60*60+59*60+59))
	if remaining >= 60*60 {
		return fmt.Sprintf("%d:%02d:%02d", remaining/(60*60), (remaining/60)%60, remaining%60)
	}
	return fmt.Sprintf("%d:%02d", remaining/60, remaining%60)
}

func queueCapabilitiesFor(state *jobState, snap JobSnapshot) QueueJobCapabilities {
	if state == nil || state.commanding || state.settling {
		return QueueJobCapabilities{}
	}
	switch snap.Status {
	case StatusPending:
		return QueueJobCapabilities{Pause: true, Cancel: true}
	case StatusActive:
		return QueueJobCapabilities{Pause: snap.CanPause && !snap.Processing, Cancel: true}
	case StatusPaused:
		return QueueJobCapabilities{Resume: true, Cancel: true}
	case StatusFailed:
		failure := queueFailureFor(state, snap)
		caps := QueueJobCapabilities{Remove: true}
		caps.Retry = failure.Retryable && !retryExhausted(state)
		caps.StartAgain = failure.Category == "disk_full" || failure.Category == "permission_denied"
		caps.OpenSource = snap.URL != "" && (failure.Category == "authentication_required" || failure.Category == "resource_unavailable")
		caps.CopyLink = caps.OpenSource
		return caps
	case StatusCanceled:
		// A fresh admission must resolve the persisted server-owned plan after
		// restart. That resolver is not available in this manager yet, so do
		// not expose a command that would depend on an expired analysis cache.
		return QueueJobCapabilities{Remove: true}
	case StatusComplete:
		return QueueJobCapabilities{Open: snap.AbsolutePath != "", Remove: true}
	case StatusActionRequired:
		// Review is read-only and preserves the evidence-bearing row. Any next
		// step is separately authorized; the frontend never derives recovery
		// authority from the action-required reason or presentation copy.
		return QueueJobCapabilities{Review: true}
	default:
		return QueueJobCapabilities{}
	}
}

func queueFailureFor(state *jobState, snap JobSnapshot) QueueFailure {
	code := strings.TrimSpace(snap.ErrorReason)
	partial := snap.Bytes > 0
	if state != nil && state.fromStateV2 {
		if state.durable.LastErrorCode != "" {
			code = state.durable.LastErrorCode
		}
		partial = partial || state.durable.LastFailureCommittedBytes > 0
	}
	category := failureCategoryForCode(code)
	failure := QueueFailure{Category: category, PartialOutput: partial}
	switch category {
	case "network_interrupted":
		failure.MessageKey = "queue.failure.network_interrupted"
		failure.Heading = "Download interrupted"
		failure.Message = "The connection was interrupted before this download finished."
		failure.RecommendedAction = "Check your connection, then retry this item."
		failure.Retryable = true
	case "authentication_required":
		failure.MessageKey = "queue.failure.authentication_required"
		failure.Heading = "Sign-in required"
		failure.Message = "This video requires an authenticated session, which VidStow does not support in this release."
		failure.RecommendedAction = "Open the source to confirm access, or remove this item."
	case "resource_unavailable":
		failure.MessageKey = "queue.failure.resource_unavailable"
		failure.Heading = "Video unavailable"
		failure.Message = "This video may be private, removed, blocked, or otherwise unavailable."
		failure.RecommendedAction = "Open the source to check it, or remove this item."
	case "disk_full":
		failure.MessageKey = "queue.failure.disk_full"
		failure.Heading = "Not enough disk space"
		failure.Message = "VidStow could not finish writing this download."
		failure.RecommendedAction = "Free space or change the default folder, then start this item again."
	case "permission_denied":
		failure.MessageKey = "queue.failure.permission_denied"
		failure.Heading = "Folder is not writable"
		failure.Message = "VidStow does not have permission to write this download."
		failure.RecommendedAction = "Fix access or change the default folder, then start this item again."
	case "security_blocked":
		failure.MessageKey = "queue.failure.security_blocked"
		failure.Heading = "Download blocked"
		failure.Message = "VidStow stopped this download because a security check failed."
		failure.RecommendedAction = "Remove this item. If the problem persists, copy diagnostics from Settings."
	case "retry_exhausted":
		failure.MessageKey = "queue.failure.retry_exhausted"
		failure.Heading = "Retry limit reached"
		failure.Message = "VidStow could not make progress after the allowed recovery attempts."
		failure.RecommendedAction = "Remove this item and analyze the source again from Home."
	default:
		failure.MessageKey = "queue.failure.internal"
		failure.Heading = "Download failed"
		failure.Message = "VidStow could not complete this download."
		failure.RecommendedAction = "Retry this item. If it fails again, copy diagnostics from Settings."
		failure.Retryable = true
	}
	if failure.Retryable && state != nil && retryExhausted(state) {
		failure.Category = "retry_exhausted"
		failure.MessageKey = "queue.failure.retry_exhausted"
		failure.Heading = "Retry limit reached"
		failure.Message = "VidStow could not make progress after the allowed recovery attempts."
		failure.RecommendedAction = "Remove this item and analyze the source again from Home."
		failure.Retryable = false
	}
	return failure
}

func failureCategoryForCode(code string) string {
	switch strings.TrimSpace(code) {
	case "network", retryCodeMediaLinkExpired, retryCodeYouTubeChallengePreTransfer:
		return "network_interrupted"
	case "authentication":
		return "authentication_required"
	case "unsupported", "invalid_input":
		return "resource_unavailable"
	case "disk_full":
		return "disk_full"
	case "permission_denied":
		return "permission_denied"
	case "security":
		return "security_blocked"
	case retryCodeFreshDownloadRequired:
		return "retry_exhausted"
	default:
		return "internal"
	}
}

func (m *Manager) queueCapabilitiesLocked(state *jobState, snap JobSnapshot) QueueJobCapabilities {
	if m.persistStatus.Available && !m.persistStatus.Healthy || !m.persistStatus.Available && m.stateStore != nil {
		return QueueJobCapabilities{}
	}
	capabilities := queueCapabilitiesFor(state, snap)
	pendingCleanup, _ := m.cleanupStatusLocked(snap.ID)
	if pendingCleanup {
		// A retained row remains the visible owner of cleanup evidence. It can
		// only be removed after the durable tombstones have settled. Terminal
		// rows expose Review so a quarantined cleanup can be retried explicitly.
		capabilities.Remove = false
		capabilities.StartAgain = false
		switch snap.Status {
		case StatusCanceled, StatusFailed, StatusComplete:
			capabilities.Review = true
		}
	}
	return capabilities
}

func (m *Manager) cleanupStatusLocked(jobID string) (present, quarantined bool) {
	if m.stateStore == nil || jobID == "" {
		return false, false
	}
	for _, tombstone := range m.stateStore.Snapshot().Cleanup {
		if tombstone.JobID != jobID {
			continue
		}
		present = true
		quarantined = quarantined || tombstone.State == jobmodel.CleanupQuarantined
	}
	return present, quarantined
}

func (m *Manager) authorizeQueueCommand(id, token string, allowed func(QueueJobCapabilities) bool) (*jobState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshQueueAuthorityLocked()
	state := m.all[id]
	if state == nil || token == "" || token != state.commandToken || !allowed(m.queueCapabilitiesLocked(state, state.snap)) {
		return nil, errors.New("jobs: queue action is no longer available")
	}
	return state, nil
}

func (m *Manager) QueuePause(id, token string) error {
	if _, err := m.authorizeQueueCommand(id, token, func(c QueueJobCapabilities) bool { return c.Pause }); err != nil {
		return err
	}
	return m.Pause(id)
}
func (m *Manager) QueueCancel(id, token string) error {
	if _, err := m.authorizeQueueCommand(id, token, func(c QueueJobCapabilities) bool { return c.Cancel }); err != nil {
		return err
	}
	return m.Cancel(id)
}
func (m *Manager) QueueResume(id, token string) error {
	if _, err := m.authorizeQueueCommand(id, token, func(c QueueJobCapabilities) bool { return c.Resume }); err != nil {
		return err
	}
	return m.Resume(id)
}
func (m *Manager) QueueRetry(id, token string) error {
	if _, err := m.authorizeQueueCommand(id, token, func(c QueueJobCapabilities) bool { return c.Retry }); err != nil {
		return err
	}
	return m.Retry(id)
}

// QueueStartAgainURL removes an eligible failed row and its durable
// reservation before releasing the source URL for normal Home analysis. A
// replacement is therefore always a new standalone logical job.
func (m *Manager) QueueStartAgainURL(id, token string) (string, error) {
	m.mu.Lock()
	m.refreshQueueAuthorityLocked()
	state := m.all[id]
	if state == nil || token == "" || token != state.commandToken || !m.queueCapabilitiesLocked(state, state.snap).StartAgain {
		m.mu.Unlock()
		return "", errors.New("jobs: start again is no longer available")
	}
	state.commanding = true
	url := strings.TrimSpace(state.snap.URL)
	m.mu.Unlock()

	if url == "" {
		m.mu.Lock()
		state.commanding = false
		m.emitQueueLocked()
		m.mu.Unlock()
		return "", errors.New("jobs: source URL is unavailable")
	}
	if err := m.removeDurable(state); err != nil {
		m.mu.Lock()
		if m.all[id] == state {
			state.commanding = false
			m.emitQueueLocked()
		}
		m.mu.Unlock()
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.all[id] != state {
		return "", errors.New("jobs: failed row changed before it could be removed")
	}
	delete(m.all, id)
	m.removeFromOrderLocked(id)
	m.emitQueueLocked()
	return url, nil
}

func (m *Manager) QueueOpenSourceURL(id, token string) (string, error) {
	return m.queueSourceURL(id, token, func(c QueueJobCapabilities) bool { return c.OpenSource })
}

func (m *Manager) QueueCopySourceURL(id, token string) (string, error) {
	return m.queueSourceURL(id, token, func(c QueueJobCapabilities) bool { return c.CopyLink })
}

func (m *Manager) queueSourceURL(id, token string, allowed func(QueueJobCapabilities) bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshQueueAuthorityLocked()
	state := m.all[id]
	if state == nil || token == "" || token != state.commandToken || !allowed(m.queueCapabilitiesLocked(state, state.snap)) {
		return "", errors.New("jobs: queue action is no longer available")
	}
	url := strings.TrimSpace(state.snap.URL)
	if url == "" {
		return "", errors.New("jobs: source URL is unavailable")
	}
	return url, nil
}

func (m *Manager) QueueRemove(id, token string) error {
	if _, err := m.authorizeQueueCommand(id, token, func(c QueueJobCapabilities) bool { return c.Remove }); err != nil {
		return err
	}
	return m.Remove(id)
}

// QueueActionRequiredReview returns bounded recovery guidance for the exact row
// authorized by token. Opening Review does not mutate, discard, or reinterpret
// retained engine evidence.
func (m *Manager) QueueActionRequiredReview(id, token string) (ActionRequiredReview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshQueueAuthorityLocked()
	state := m.all[id]
	if state == nil || token == "" || token != state.commandToken || !m.queueCapabilitiesLocked(state, state.snap).Review {
		return ActionRequiredReview{}, errors.New("jobs: queue action is no longer available")
	}
	_, quarantined := m.cleanupStatusLocked(id)
	return actionRequiredReview(state, quarantined), nil
}

// QueueActionRequiredStartOverURL releases only the persisted source URL after
// rechecking the same backend authority. The caller must perform normal Home
// analysis and admission, which creates fresh job, attempt, session, and
// reservation identities; this command never reuses or deletes old evidence.
func (m *Manager) QueueActionRequiredStartOverURL(id, token string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshQueueAuthorityLocked()
	state := m.all[id]
	if state == nil || token == "" || token != state.commandToken || !m.queueCapabilitiesLocked(state, state.snap).Review {
		return "", errors.New("jobs: queue action is no longer available")
	}
	if state.snap.Status != StatusActionRequired {
		return "", errors.New("jobs: starting over is not available for this item")
	}
	_, quarantined := m.cleanupStatusLocked(id)
	review := actionRequiredReview(state, quarantined)
	if !review.CanStartOver {
		return "", errors.New("jobs: starting over is not available for this item")
	}
	return strings.TrimSpace(state.snap.URL), nil
}

// QueueActionRequiredDiscard uses the existing prepare-then-commit Cancel
// protocol. Unsafe or indeterminate engine evidence therefore leaves the row
// untouched; cleanup-pending outcomes remain durable and visible.
func (m *Manager) QueueActionRequiredDiscard(id, token string) error {
	state, err := m.authorizeQueueCommand(id, token, func(c QueueJobCapabilities) bool { return c.Review })
	if err != nil || state.snap.Status != StatusActionRequired {
		return errors.New("jobs: discarding saved data is no longer available")
	}
	m.mu.Lock()
	if m.all[id] != state || state.commanding || state.settling || m.active[id] != nil {
		m.mu.Unlock()
		return errors.New("jobs: discarding saved data is no longer available")
	}
	state.commanding = true
	m.mu.Unlock()
	return m.cancelIdle(state)
}

// QueueActionRequiredRetryFreshLink rotates away from uncertain session
// evidence atomically, retains it for durable cleanup, and starts the same row
// with a fresh engine session so media URLs are resolved again.
func (m *Manager) QueueActionRequiredRetryFreshLink(id, token string) error {
	state, err := m.authorizeQueueCommand(id, token, func(c QueueJobCapabilities) bool { return c.Review })
	if err != nil || state.snap.Status != StatusActionRequired || !canRetryActionRequiredFresh(state) {
		return errors.New("jobs: fresh-link retry is no longer available")
	}
	m.mu.Lock()
	if m.all[id] != state || state.commanding || state.settling {
		m.mu.Unlock()
		return errors.New("jobs: fresh-link retry is no longer available")
	}
	state.commanding = true
	m.mu.Unlock()
	if !freshRetryDestinationAvailable(state.durable) {
		m.mu.Lock()
		state.commanding = false
		m.emitQueueLocked()
		m.mu.Unlock()
		return errors.New("jobs: the reserved destination is no longer available; start over from Home to choose a new name")
	}

	previousSessionID := state.durable.SessionID
	freshSessionID := newSessionID()
	if err := m.commitDurable(state, func(job *jobmodel.DurableJob, document *jobmodel.State) error {
		upsertJobCleanupTombstone(document, job.ID, previousSessionID, job.OutputRoot, job.Reservation, "cleanup", time.Now().UTC())
		if document.NextQueueOrdinal == ^uint64(0) {
			return errors.New("jobs: queue ordinal exhausted")
		}
		job.Lifecycle = jobmodel.LifecyclePending
		job.Desired = jobmodel.DesiredRunning
		job.Phase = jobmodel.PhasePreparing
		job.AttemptID = uuid.NewString()
		job.SessionID = freshSessionID
		job.RetryMode = jobmodel.RetryModeRestartNewSession
		job.ActionRequiredCode = ""
		job.LastErrorCode = ""
		job.LastFailureCommittedBytes = 0
		job.ZeroProgressResumes = 0
		job.SessionRestarts++
		job.QueueOrdinal = document.NextQueueOrdinal
		document.NextQueueOrdinal++
		return nil
	}); err != nil {
		m.mu.Lock()
		state.commanding = false
		m.mu.Unlock()
		return err
	}
	m.discardRetiredSession(state, previousSessionID)

	m.mu.Lock()
	if m.closing || m.closed || m.all[id] != state || !state.commanding {
		state.commanding = false
		m.mu.Unlock()
		return errors.New("jobs: stale fresh-link retry")
	}
	state.snap.Status = StatusPending
	state.snap.Progress = 0
	state.snap.Bytes = 0
	state.snap.Total = 0
	state.snap.SpeedBps = 0
	state.snap.ETASeconds = 0
	state.snap.Message = "Queued with a fresh media link"
	state.snap.ErrorReason = ""
	state.snap.StartedAt = ""
	state.snap.CompletedAt = ""
	state.commanding = false
	m.order = append(m.order, id)
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	m.maybeStartNextLocked()
	m.emitQueueLocked()
	m.mu.Unlock()
	return nil
}

// QueueActionRequiredRetryRecovery re-inspects preserved evidence. Only a
// session the engine now classifies as reusable may re-enter ordinary Retry;
// uncertainty remains action-required and no identity is changed.
func (m *Manager) QueueActionRequiredRetryRecovery(id, token string) error {
	state, err := m.authorizeQueueCommand(id, token, func(c QueueJobCapabilities) bool { return c.Review })
	if err != nil || state.snap.Status != StatusActionRequired || !canRetryActionRequiredRecovery(state) {
		return errors.New("jobs: recovery retry is no longer available")
	}
	m.mu.Lock()
	if m.all[id] != state || state.commanding || state.settling {
		m.mu.Unlock()
		return errors.New("jobs: recovery retry is no longer available")
	}
	state.commanding = true
	m.mu.Unlock()
	clearCommand := func() {
		m.mu.Lock()
		if m.all[id] == state {
			state.commanding = false
			m.emitQueueLocked()
		}
		m.mu.Unlock()
	}
	summary, inspectErr := m.inspectResume(context.Background(), engineRootRef(state.durable.OutputRoot), state.durable.SessionID)
	if inspectErr != nil {
		clearCommand()
		return errors.New("jobs: saved data is still unavailable; it was preserved")
	}
	decision, _ := classifyRetryResume(summary)
	if decision != retryResumeReuse {
		clearCommand()
		return errors.New("jobs: saved data is still not safe to retry; it was preserved")
	}
	if err := m.commitDurable(state, func(job *jobmodel.DurableJob, _ *jobmodel.State) error {
		job.Lifecycle = jobmodel.LifecycleFailed
		job.Desired = jobmodel.DesiredPaused
		job.ActionRequiredCode = ""
		job.LastErrorCode = ""
		return nil
	}); err != nil {
		clearCommand()
		return err
	}
	m.mu.Lock()
	if m.all[id] != state {
		m.mu.Unlock()
		return errors.New("jobs: stale recovery retry")
	}
	state.snap.Status = StatusFailed
	state.snap.ErrorReason = ""
	state.snap.Message = "Ready to retry"
	state.commanding = false
	m.mu.Unlock()
	return m.Retry(id)
}

// QueueRetryCleanup re-enables explicitly reviewed quarantined tombstones.
// The periodic worker performs the actual engine inspection and discard.
func (m *Manager) QueueRetryCleanup(id, token string) error {
	state, err := m.authorizeQueueCommand(id, token, func(c QueueJobCapabilities) bool { return c.Review })
	if err != nil || state.snap.Status == StatusActionRequired {
		return errors.New("jobs: cleanup retry is no longer available")
	}
	store := m.stateStoreSnapshot()
	if store == nil {
		return errors.New("jobs: State v2 store is not configured")
	}
	now := time.Now().UTC()
	changed := false
	if err := m.transaction(store, nil, func(document *jobmodel.State) error {
		for index := range document.Cleanup {
			tombstone := &document.Cleanup[index]
			if tombstone.JobID == id && tombstone.State == jobmodel.CleanupQuarantined {
				tombstone.State = jobmodel.CleanupPending
				tombstone.LastErrorCode = "cleanup"
				tombstone.UpdatedAt = now
				changed = true
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if !changed {
		return errors.New("jobs: cleanup retry is no longer available")
	}
	m.mu.Lock()
	m.emitQueueLocked()
	m.mu.Unlock()
	return nil
}

func actionRequiredReview(state *jobState, cleanupQuarantined bool) ActionRequiredReview {
	code := strings.TrimSpace(state.snap.ErrorReason)
	if state.fromStateV2 && state.durable.ActionRequiredCode != "" {
		code = state.durable.ActionRequiredCode
	}
	message := "VidStow cannot safely continue this saved download automatically."
	switch code {
	case "session-lease-contended":
		message = "Another VidStow process may still be using this download’s saved data. Close the other process before starting over from Home."
	case "publication-reconciliation-required":
		message = "VidStow cannot prove whether the final file was published. Check your download folder before starting another copy."
	case "session-manifest-corrupt", "session-version-unknown", "session-path-unsafe":
		message = "The saved download data could not be validated, so VidStow will not resume or delete it automatically."
	case "recovery-session-unavailable", "session-reconciliation-required":
		message = "The saved download session could not be inspected safely, so VidStow has preserved it for review."
	case "migration-reanalysis-required", "migration-private-plan-unverified":
		message = "This item came from an older VidStow version and must be analyzed again before it can download."
	}
	url := strings.TrimSpace(state.snap.URL)
	if state.snap.Status != StatusActionRequired {
		message = "VidStow could not finish removing an older download session’s saved temporary data. The queue item remains visible so cleanup is never silently forgotten."
		return ActionRequiredReview{
			JobID: state.snap.ID, Title: state.snap.Title,
			Heading: "Temporary data is still preserved", Message: message,
			PreservationNotice: "Removing this queue item stays unavailable until cleanup succeeds. VidStow retries pending cleanup automatically after restart.",
			CanRetryCleanup:    cleanupQuarantined,
		}
	}
	canManageSession := state.fromStateV2 && state.durable.SessionID != "" && state.durable.OutputRoot.CanonicalPath != ""
	return ActionRequiredReview{
		JobID: state.snap.ID, Title: state.snap.Title,
		Heading: "This download needs your decision", Message: message,
		PreservationNotice: "The original queue item and its saved temporary data will stay in place unless you explicitly discard it. Starting over uses a new destination reservation and never reuses uncertain data.",
		CanStartOver:       url != "",
		CanRetryRecovery:   canManageSession && canRetryActionRequiredRecovery(state),
		CanRetryFreshLink:  canManageSession && canRetryActionRequiredFresh(state),
		CanDiscard:         canManageSession,
	}
}

func freshRetryDestinationAvailable(job jobmodel.DurableJob) bool {
	root, err := reservationfs.OpenRoot(job.OutputRoot.CanonicalPath)
	if err != nil {
		return false
	}
	defer root.Close()
	facts := root.Facts()
	if facts.Volume.CanonicalPath != job.OutputRoot.CanonicalPath || facts.Volume.Identity != job.OutputRoot.Identity {
		return false
	}
	for _, artifact := range job.Reservation.Artifacts {
		availability, err := facts.Probe.Probe(context.Background(), facts.Volume, artifact.Basename)
		if err != nil || availability != reservation.Available {
			return false
		}
	}
	return true
}

func canRetryActionRequiredRecovery(state *jobState) bool {
	if state == nil || !state.fromStateV2 || state.durable.SessionID == "" || state.durable.OutputRoot.CanonicalPath == "" {
		return false
	}
	switch state.durable.ActionRequiredCode {
	case "session-lease-contended", "recovery-session-unavailable", "output-root-unavailable":
		return true
	default:
		return false
	}
}

func canRetryActionRequiredFresh(state *jobState) bool {
	if state == nil || !state.fromStateV2 || strings.TrimSpace(state.snap.URL) == "" || state.durable.SessionID == "" || state.durable.OutputRoot.CanonicalPath == "" || len(state.durable.Reservation.Artifacts) == 0 || state.durable.SessionRestarts >= maxZeroProgressRestarts {
		return false
	}
	code := state.durable.ActionRequiredCode
	switch code {
	case "publication-reconciliation-required", "output-root-unavailable", "session-path-unsafe", "session-lease-contended", "migration-reanalysis-required", "migration-private-plan-unverified":
		return false
	default:
		return true
	}
}

func (m *Manager) authorizeCollectionCommand(id, token string, allowed func(QueueCollectionCapabilities) bool, childAllowed func(QueueJobCapabilities) bool) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	view := m.queueViewLocked()
	var collection *QueueCollection
	for index := range view.Collections {
		if view.Collections[index].ID == id {
			collection = &view.Collections[index]
			break
		}
	}
	if collection == nil || m.collectionCommanding[id] || token == "" || token != collection.CommandToken || !allowed(collection.Capabilities) {
		return nil, errors.New("jobs: collection action is no longer available")
	}
	children := make([]string, 0, len(collection.ChildJobIDs))
	for _, childID := range collection.ChildJobIDs {
		state := m.all[childID]
		if state == nil || !childAllowed(m.queueCapabilitiesLocked(state, state.snap)) {
			continue
		}
		children = append(children, childID)
	}
	if len(children) == 0 {
		return nil, errors.New("jobs: collection action has no eligible children")
	}
	authority := m.collectionAuthority[id]
	authority.token = uuid.NewString()
	m.collectionAuthority[id] = authority
	m.collectionCommanding[id] = true
	return children, nil
}

func (m *Manager) finishCollectionCommand(id string) {
	m.mu.Lock()
	if m.collectionCommanding[id] {
		delete(m.collectionCommanding, id)
		m.emitQueueLocked()
	}
	m.mu.Unlock()
}

func runCollectionCommand(children []string, action func(string) error) (int, error) {
	completed := 0
	for _, childID := range children {
		if err := action(childID); err != nil {
			return completed, fmt.Errorf("jobs: collection action stopped after %d children: %w", completed, err)
		}
		completed++
	}
	return completed, nil
}

func (m *Manager) QueuePauseCollection(id, token string) (int, error) {
	children, err := m.authorizeCollectionCommand(id, token, func(c QueueCollectionCapabilities) bool { return c.Pause }, func(c QueueJobCapabilities) bool { return c.Pause })
	if err != nil {
		return 0, err
	}
	defer m.finishCollectionCommand(id)
	return runCollectionCommand(children, m.Pause)
}

func (m *Manager) QueueCancelCollection(id, token string) (int, error) {
	children, err := m.authorizeCollectionCommand(id, token, func(c QueueCollectionCapabilities) bool { return c.Cancel }, func(c QueueJobCapabilities) bool { return c.Cancel })
	if err != nil {
		return 0, err
	}
	defer m.finishCollectionCommand(id)
	return runCollectionCommand(children, m.Cancel)
}

func (m *Manager) QueueResumeCollection(id, token string) (int, error) {
	children, err := m.authorizeCollectionCommand(id, token, func(c QueueCollectionCapabilities) bool { return c.Resume }, func(c QueueJobCapabilities) bool { return c.Resume })
	if err != nil {
		return 0, err
	}
	defer m.finishCollectionCommand(id)
	return runCollectionCommand(children, m.Resume)
}

func (m *Manager) QueueRetryCollection(id, token string) (int, error) {
	children, err := m.authorizeCollectionCommand(id, token, func(c QueueCollectionCapabilities) bool { return c.Retry }, func(c QueueJobCapabilities) bool { return c.Retry })
	if err != nil {
		return 0, err
	}
	defer m.finishCollectionCommand(id)
	return runCollectionCommand(children, m.Retry)
}

func (m *Manager) QueueRemoveCollection(id, token string) (int, error) {
	children, err := m.authorizeCollectionCommand(id, token, func(c QueueCollectionCapabilities) bool { return c.Remove }, func(c QueueJobCapabilities) bool { return c.Remove })
	if err != nil {
		return 0, err
	}
	defer m.finishCollectionCommand(id)
	return runCollectionCommand(children, m.Remove)
}

func (m *Manager) QueueDownloadAgainRequest(id, token string) (Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshQueueAuthorityLocked()
	state := m.all[id]
	if state == nil || token == "" || token != state.commandToken || !m.queueCapabilitiesLocked(state, state.snap).DownloadAgain {
		return Request{}, errors.New("jobs: queue action is no longer available")
	}
	snap := state.snap
	return Request{URL: snap.URL, VideoID: snap.VideoID, Title: snap.Title, Channel: snap.Channel, Quality: snap.Quality, PlanID: snap.PlanID, OutputDir: snap.OutputDir, Duration: snap.DurationLabel, Thumbnail: snap.Thumbnail}, nil
}

func (m *Manager) QueueOpenPath(id, token string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshQueueAuthorityLocked()
	state := m.all[id]
	if state == nil || token == "" || token != state.commandToken || !m.queueCapabilitiesLocked(state, state.snap).Open {
		return "", errors.New("jobs: queue action is no longer available")
	}
	return state.snap.AbsolutePath, nil
}

func (m *Manager) QueuePauseAll(token string) (int, error) {
	m.mu.Lock()
	view := m.queueViewLocked()
	allowed := token != "" && token == view.Capabilities.CommandToken && view.Capabilities.PauseAll
	if !allowed {
		m.mu.Unlock()
		return 0, errors.New("jobs: queue action is no longer available")
	}
	candidates := make([]pauseCandidate, 0, len(m.all))
	for id, state := range m.all {
		if state == nil || state.commanding || state.settling || !m.queueCapabilitiesLocked(state, state.snap).Pause {
			continue
		}
		state.commanding = true
		candidates = append(candidates, pauseCandidate{state: state, worker: m.active[id]})
	}
	// Consume the queue snapshot before any durable work. A second caller with
	// the same token cannot broaden or repeat this captured batch.
	m.queueCommandToken = uuid.NewString()
	m.mu.Unlock()
	return m.pauseAllV2(candidates), nil
}

func (m *Manager) QueueClearCompleted(token string) error {
	m.mu.Lock()
	view := m.queueViewLocked()
	allowed := token != "" && token == view.Capabilities.CommandToken && view.Capabilities.ClearCompleted
	if !allowed {
		m.mu.Unlock()
		return errors.New("jobs: queue action is no longer available")
	}
	candidates := make([]*jobState, 0)
	for _, state := range m.all {
		if state != nil && state.snap.Status == StatusComplete && !state.commanding && !state.settling {
			state.commanding = true
			candidates = append(candidates, state)
		}
	}
	if len(candidates) == 0 {
		m.queueCommandToken = uuid.NewString()
		m.mu.Unlock()
		return errors.New("jobs: no completed rows remain")
	}
	// Consume the snapshot token before releasing the manager authority.
	m.queueCommandToken = uuid.NewString()
	m.mu.Unlock()

	if store := m.stateStoreSnapshot(); store != nil {
		preconditions := make([]jobmodel.JobPrecondition, 0, len(candidates))
		ids := make(map[string]struct{}, len(candidates))
		for _, state := range candidates {
			if !state.fromStateV2 {
				continue
			}
			job := state.durable
			preconditions = append(preconditions, jobmodel.JobPrecondition{ID: job.ID, Revision: job.Revision, Lifecycle: job.Lifecycle, AttemptID: job.AttemptID, SessionID: job.SessionID, OutputRoot: job.OutputRoot})
			ids[job.ID] = struct{}{}
		}
		if err := m.transaction(store, preconditions, func(document *jobmodel.State) error {
			next := document.Jobs[:0]
			for _, job := range document.Jobs {
				if _, remove := ids[job.ID]; remove {
					if job.Lifecycle != jobmodel.LifecycleCompleted {
						return errors.New("jobs: completed row changed before removal")
					}
					continue
				}
				next = append(next, job)
			}
			document.Jobs = next
			return nil
		}); err != nil {
			m.mu.Lock()
			for _, state := range candidates {
				state.commanding = false
			}
			m.emitQueueLocked()
			m.mu.Unlock()
			return err
		}
	}
	m.mu.Lock()
	for _, state := range candidates {
		if m.all[state.snap.ID] != state || state.snap.Status != StatusComplete {
			for _, pending := range candidates {
				pending.commanding = false
			}
			m.emitQueueLocked()
			m.mu.Unlock()
			return errors.New("jobs: completed row changed before removal")
		}
		delete(m.all, state.snap.ID)
		m.removeFromOrderLocked(state.snap.ID)
	}
	m.emitQueueLocked()
	m.mu.Unlock()
	return nil
}

// Cancel stops the job if it is running and removes it from the queue.
// It marks terminal jobs (complete/failed/canceled) as canceled so the
// caller can rely on the state to flow.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	state, ok := m.all[id]
	if !ok || state.settling || state.commanding {
		m.mu.Unlock()
		return errors.New("jobs: lifecycle transition is settling")
	}
	switch state.snap.Status {
	case StatusComplete, StatusFailed, StatusCanceled, StatusPausing, StatusCanceling:
		m.mu.Unlock()
		return errors.New("jobs: cancel is no longer available")
	}
	state.commanding = true
	worker := m.active[id]
	if worker != nil {
		if worker.Arbiter == nil {
			state.commanding = false
			m.mu.Unlock()
			return errors.New("jobs: active worker is unavailable")
		}
		state.snap.Status = StatusCanceling
		state.snap.Message = "Canceling"
		state.snap.CanPause = false
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
		m.emitQueueLocked()
		m.mu.Unlock()

		reservation, err := worker.Arbiter.BeginCancel(context.Background())
		if err != nil {
			// Publication already won or another cancel was accepted. The
			// runner remains authoritative and will settle its own winner.
			m.mu.Lock()
			if current := m.all[id]; current == state && m.active[id] == worker && !state.settling {
				state.commanding = false
				state.snap.Status = StatusActive
				state.snap.Message = "Downloading"
				state.snap.CanPause = true
				m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
			}
			m.mu.Unlock()
			return fmt.Errorf("jobs: cancel was not accepted: %w", err)
		}
		if err := m.transitionDurable(state, jobmodel.LifecycleCanceling, jobmodel.DesiredCanceled, jobmodel.PhaseCleaningUp); err != nil {
			reservation.AbortCancel()
			m.mu.Lock()
			if m.active[id] == worker && !state.settling {
				state.commanding = false
				state.snap.Status = StatusActive
				state.snap.Message = "Downloading"
				state.snap.CanPause = true
				m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
			}
			m.mu.Unlock()
			return err
		}
		reservation.WinCancel()
		worker.Cancel(errCancelRequested)
		return nil
	}

	// Pending and paused rows have no local runner. They use the same
	// prepare-then-commit protocol, retaining a cleanup tombstone when the
	// engine reports cleanup evidence that cannot be removed immediately.
	m.detachedWG.Add(1)
	go func() {
		defer m.detachedWG.Done()
		_ = m.cancelIdle(state)
	}()
	m.mu.Unlock()
	return nil
}

func (m *Manager) cancelIdle(state *jobState) error {
	if state == nil {
		return errors.New("jobs: missing idle job")
	}
	handle, unavailable, prepareErr := m.prepareDiscardSession(state, state.durable.SessionID)
	if prepareErr != nil {
		// Preserve the row and surface the failure through the existing
		// runtime event stream; no destructive operation occurred.
		m.mu.Lock()
		state.commanding = false
		m.maybeStartNextLocked()
		m.emitQueueLocked()
		m.mu.Unlock()
		return prepareErr
	}
	if err := m.transitionDurable(state, jobmodel.LifecycleCanceling, jobmodel.DesiredCanceled, jobmodel.PhaseCleaningUp); err != nil {
		if handle != nil {
			_ = handle.Close()
		}
		m.mu.Lock()
		state.commanding = false
		m.maybeStartNextLocked()
		m.emitQueueLocked()
		m.mu.Unlock()
		return err
	}
	cleanupPending := false
	cleanupCode := ""
	if handle != nil {
		result, err := handle.Discard(context.Background())
		if err != nil || result.Disposition != engine.ResumeDiscarded {
			cleanupPending = true
			cleanupCode = "cleanup"
		}
	} else if !unavailable {
		cleanupPending = true
		cleanupCode = "cleanup"
	}
	m.mu.Lock()
	terminal := state.snap
	terminal.Status = StatusCanceled
	terminal.Message = "Canceled"
	terminal.ErrorReason = cleanupCode
	terminal.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	state.snap = terminal
	m.mu.Unlock()
	if err := m.settleDurable(state, jobmodel.LifecycleCanceled, jobmodel.DesiredCanceled, jobmodel.PhaseCleaningUp, terminal, cleanupCode, cleanupPending, -1); err != nil {
		m.mu.Lock()
		state.commanding = false
		m.maybeStartNextLocked()
		m.emitQueueLocked()
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	if current := m.all[state.snap.ID]; current == state {
		state.snap = terminal
		state.commanding = false
		m.removeFromOrderLocked(state.snap.ID)
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
		m.maybeStartNextLocked()
		m.emitQueueLocked()
	}
	m.mu.Unlock()
	return nil
}

// Pause suspends a pending or actively downloading job. Active processing
// stages cannot be paused safely; their downloaded inputs are retained and
// the control becomes available again only after processing completes.
func (m *Manager) Pause(id string) error {
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	state, ok := m.all[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("jobs: unknown job %q", id)
	}
	if state.settling || state.commanding {
		m.mu.Unlock()
		return errors.New("jobs: lifecycle transition is settling")
	}
	switch state.snap.Status {
	case StatusPending:
		state.commanding = true
		m.mu.Unlock()
		if err := m.transitionDurable(state, jobmodel.LifecyclePaused, jobmodel.DesiredPaused, jobmodel.PhasePreparing); err != nil {
			m.mu.Lock()
			state.commanding = false
			m.maybeStartNextLocked()
			m.emitQueueLocked()
			m.mu.Unlock()
			return err
		}
		m.mu.Lock()
		if m.all[id] == state && state.commanding && (!state.fromStateV2 || state.durable.Lifecycle == jobmodel.LifecyclePaused) {
			state.snap.Status = StatusPaused
			state.snap.Message = "Paused"
			state.snap.CanPause = false
			state.commanding = false
			m.removeFromOrderLocked(id)
			m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
			m.emitQueueLocked()
		} else if m.all[id] == state {
			state.commanding = false
			m.maybeStartNextLocked()
			m.emitQueueLocked()
		}
		m.mu.Unlock()
		return nil
	case StatusActive:
		if state.snap.Processing {
			m.mu.Unlock()
			return errors.New("jobs: pause is unavailable while media is being finalized")
		}
		worker := m.active[id]
		if worker == nil {
			m.mu.Unlock()
			return errors.New("jobs: active worker is unavailable")
		}
		state.snap.Status = StatusPausing
		state.snap.CanPause = false
		state.snap.Message = "Pausing"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
		m.mu.Unlock()
		if err := m.transitionDurable(state, jobmodel.LifecyclePausing, jobmodel.DesiredPaused, jobmodel.PhasePreparing); err != nil {
			m.mu.Lock()
			if m.active[id] == worker && !state.settling {
				state.snap.Status = StatusActive
				state.snap.CanPause = true
				state.snap.Message = "Downloading"
				m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
			}
			m.mu.Unlock()
			return err
		}
		worker.Cancel(engine.ErrPauseRequested)
		return nil
	case StatusPaused:
		m.mu.Unlock()
		return nil
	default:
		m.mu.Unlock()
		return errors.New("jobs: only pending or active jobs can be paused")
	}
}

// PauseAll pauses every pending job and every active job not currently in a
// media-processing critical section. It returns the number accepted.
func (m *Manager) PauseAll() int {
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return 0
	}
	legacyIDs := make([]string, 0, len(m.all))
	v2 := make([]pauseCandidate, 0, len(m.all))
	for id, state := range m.all {
		if state.commanding || state.settling {
			continue
		}
		if state.snap.Status != StatusPending && !(state.snap.Status == StatusActive && !state.snap.Processing) {
			continue
		}
		if state.fromStateV2 {
			state.commanding = true
			v2 = append(v2, pauseCandidate{state: state, worker: m.active[id]})
		} else {
			legacyIDs = append(legacyIDs, id)
		}
	}
	m.mu.Unlock()

	paused := m.pauseAllV2(v2)
	for _, id := range legacyIDs {
		if m.Pause(id) == nil {
			paused++
		}
	}
	return paused
}

type pauseCandidate struct {
	state  *jobState
	worker *worker
}

// pauseAllV2 applies the accepted mixed pending/active Pause All command in
// one State transaction. Active workers remain in m.active until their runner
// settles; only pending rows release FIFO eligibility immediately.
func (m *Manager) pauseAllV2(candidates []pauseCandidate) int {
	if len(candidates) == 0 {
		return 0
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].state.snap.ID < candidates[j].state.snap.ID })
	stateStore := m.stateStoreSnapshot()
	if stateStore == nil {
		for _, candidate := range candidates {
			m.mu.Lock()
			candidate.state.commanding = false
			m.maybeStartNextLocked()
			m.emitQueueLocked()
			m.mu.Unlock()
		}
		return 0
	}
	for index := range candidates {
		candidates[index].state.transitionMu.Lock()
	}
	defer func() {
		for index := len(candidates) - 1; index >= 0; index-- {
			candidates[index].state.transitionMu.Unlock()
		}
	}()
	preconditions := make([]jobmodel.JobPrecondition, len(candidates))
	for index, candidate := range candidates {
		job := candidate.state.durable
		preconditions[index] = jobmodel.JobPrecondition{ID: job.ID, Revision: job.Revision, Lifecycle: job.Lifecycle, AttemptID: job.AttemptID, SessionID: job.SessionID, OutputRoot: job.OutputRoot}
	}
	committed := make([]jobmodel.DurableJob, len(candidates))
	err := m.transaction(stateStore, preconditions, func(document *jobmodel.State) error {
		for index, candidate := range candidates {
			found := false
			for jobIndex := range document.Jobs {
				if document.Jobs[jobIndex].ID != candidate.state.durable.ID {
					continue
				}
				job := &document.Jobs[jobIndex]
				if job.Lifecycle == jobmodel.LifecyclePending {
					job.Lifecycle = jobmodel.LifecyclePaused
				} else if job.Lifecycle == jobmodel.LifecycleActive {
					job.Lifecycle = jobmodel.LifecyclePausing
				} else {
					return errors.New("jobs: Pause All found a stale lifecycle")
				}
				job.Desired = jobmodel.DesiredPaused
				job.Phase = jobmodel.PhasePreparing
				job.Revision++
				job.UpdatedAt = time.Now().UTC()
				committed[index] = *job
				found = true
				break
			}
			if !found {
				return errors.New("jobs: Pause All row disappeared")
			}
		}
		return nil
	})
	if err != nil {
		m.mu.Lock()
		for _, candidate := range candidates {
			candidate.state.commanding = false
		}
		m.maybeStartNextLocked()
		m.emitQueueLocked()
		m.mu.Unlock()
		return 0
	}
	m.mu.Lock()
	accepted := 0
	for index, candidate := range candidates {
		state := candidate.state
		state.durable = committed[index]
		state.authorityRevision = committed[index].Revision
		state.authorityAttemptID = committed[index].AttemptID
		state.snap.Lifecycle = committed[index].Lifecycle
		state.snap.Phase = committed[index].Phase
		state.snap.Desired = committed[index].Desired
		if candidate.worker == nil {
			state.snap.Status = StatusPaused
			state.snap.Message = "Paused"
			state.snap.CanPause = false
			m.removeFromOrderLocked(state.snap.ID)
		} else {
			state.snap.Status = StatusPausing
			state.snap.Message = "Pausing"
			state.snap.CanPause = false
		}
		accepted++
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	}
	m.emitQueueLocked()
	m.mu.Unlock()
	for _, candidate := range candidates {
		if candidate.worker != nil {
			candidate.worker.Cancel(engine.ErrPauseRequested)
		} else {
			m.mu.Lock()
			candidate.state.commanding = false
			m.mu.Unlock()
		}
	}
	return accepted
}

// Resume returns a paused job to the FIFO. The engine's default partial-file
// behavior resumes byte-range downloads instead of discarding existing data.
func (m *Manager) Resume(id string) error {
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	state, ok := m.all[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("jobs: unknown job %q", id)
	}
	if state.snap.Status != StatusPaused || state.commanding {
		m.mu.Unlock()
		return errors.New("jobs: only paused jobs can be resumed")
	}
	state.commanding = true
	m.mu.Unlock()
	if state.fromStateV2 {
		if err := m.commitDurable(state, func(job *jobmodel.DurableJob, document *jobmodel.State) error {
			job.Lifecycle = jobmodel.LifecyclePending
			job.Desired = jobmodel.DesiredRunning
			job.Phase = jobmodel.PhasePreparing
			job.AttemptID = uuid.NewString()
			if document.NextQueueOrdinal == ^uint64(0) {
				return errors.New("jobs: queue ordinal exhausted")
			}
			job.QueueOrdinal = document.NextQueueOrdinal
			document.NextQueueOrdinal++
			job.RetryMode = jobmodel.RetryModeResumeValidated
			return nil
		}); err != nil {
			m.mu.Lock()
			state.commanding = false
			m.mu.Unlock()
			return err
		}
	}
	m.mu.Lock()
	if m.closing || m.closed {
		state.commanding = false
		m.mu.Unlock()
		return ErrClosed
	}
	if m.all[id] != state || state.snap.Status != StatusPaused || !state.commanding {
		state.commanding = false
		m.mu.Unlock()
		return errors.New("jobs: stale resume request")
	}
	state.snap.Status = StatusPending
	state.snap.Message = "Queued"
	state.snap.Processing = false
	state.snap.CanPause = false
	state.commanding = false
	m.order = append(m.order, id)
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	m.maybeStartNextLocked()
	m.emitQueueLocked()
	m.mu.Unlock()
	return nil
}

// Shutdown stops admission, durably records paused/pausing intent, and waits
// for manager-owned workers only until the shared shutdown deadline. Rows that
// cannot settle remain pausing and are reconciled on the next startup.
func (m *Manager) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closing = true
	v2Candidates := make([]pauseCandidate, 0, len(m.all))
	legacyActive := make([]*worker, 0, len(m.active))
	for id, state := range m.all {
		if state == nil || state.commanding || state.settling {
			continue
		}
		worker := m.active[id]
		if state.fromStateV2 && (state.snap.Status == StatusPending || state.snap.Status == StatusActive) {
			state.commanding = true
			v2Candidates = append(v2Candidates, pauseCandidate{state: state, worker: worker})
			continue
		}
		if worker != nil {
			state.snap.Status = StatusPausing
			state.snap.CanPause = false
			state.snap.Message = "Pausing"
			legacyActive = append(legacyActive, worker)
		}
	}
	m.mu.Unlock()

	// The batch is the durable acceptance point. Active workers remain in the
	// occupancy map until their runner exits, even though admission is stopped.
	m.pauseAllV2(v2Candidates)
	for _, worker := range legacyActive {
		if worker != nil && worker.Cancel != nil {
			worker.Cancel(engine.ErrPauseRequested)
		}
	}

	m.mu.Lock()
	done := make([]<-chan struct{}, 0, len(m.active))
	for _, worker := range m.active {
		if worker != nil && worker.Done != nil {
			done = append(done, worker.Done)
		}
	}
	m.mu.Unlock()
	var shutdownErr error
	for _, finished := range done {
		select {
		case <-finished:
		case <-ctx.Done():
			shutdownErr = ctx.Err()
			break
		}
		if shutdownErr != nil {
			break
		}
	}
	if !m.usingStateV2() && ctx.Err() == nil {
		if err := m.FlushPersistence(); err != nil {
			shutdownErr = err
		}
	}
	return shutdownErr
}

type retryResumeDecision uint8

const (
	retryResumeReuse retryResumeDecision = iota + 1
	retryResumeActionRequired

	retryCodeYouTubeChallengePreTransfer = "youtube-challenge-pre-transfer"
	// retryCodeMediaLinkExpired marks a download-phase HTTP 403 from the
	// signed googlevideo URL: the captured link is dead, so resuming the same
	// session can never make progress.
	retryCodeMediaLinkExpired = "media-link-expired"
	// retryCodeFreshDownloadRequired marks a row whose escalation budget is
	// spent; the only remaining escape is removing it and downloading again.
	retryCodeFreshDownloadRequired = "retry-fresh-download-required"

	// A validated resume that commits no new bytes is pinned to a dead signed
	// URL. One such failure is evidence enough to demand a fresh session.
	zeroProgressResumeThreshold = 1
	// Retry stops auto-escalating after this many fresh-session restarts and
	// tells the user to download the item again instead.
	maxZeroProgressRestarts = 2
)

const (
	mediaLinkExpiredMessage     = "The download link expired mid-download. Retry will restart it with a fresh link."
	downloadStoppedMidMessage   = "The download stopped mid-download and part of the file is saved. Retry will resume it, or restart it with a fresh link if the download link has expired."
	freshDownloadRequiredNotice = "Retry can no longer restart this download. Remove the item and download it again from Home."
)

// classifyRetryResume authorizes reuse only for an available, validated
// manifest. Every other result preserves the old authority for explicit
// reconciliation. In particular, unavailable_root is not proof of absence:
// the engine also uses it for lease/open failures after finding a workspace.
func classifyRetryResume(summary engine.ResumeSummary) (retryResumeDecision, string) {
	if summary.LeaseContended {
		return retryResumeActionRequired, "session-lease-contended"
	}
	if string(summary.Publication) == "committed" || string(summary.Publication) == "indeterminate" {
		return retryResumeActionRequired, "publication-reconciliation-required"
	}
	if string(summary.Cleanup) == "indeterminate" || string(summary.Status) == "needs_reconciliation" {
		return retryResumeActionRequired, "session-reconciliation-required"
	}

	classes := append([]engine.ResumeInspectionClass{summary.Classification}, summary.Classifications...)
	seen := make(map[engine.ResumeInspectionClass]struct{}, len(classes))
	hasAvailable := false
	for _, class := range classes {
		if class == "" {
			continue
		}
		if _, duplicate := seen[class]; duplicate {
			continue
		}
		seen[class] = struct{}{}
		switch string(class) {
		case "available":
			hasAvailable = true
		case "unavailable_root":
			return retryResumeActionRequired, "recovery-session-unavailable"
		case "publication_indeterminate", "manifest_commit_indeterminate":
			return retryResumeActionRequired, "publication-reconciliation-required"
		case "unknown_manifest_version":
			return retryResumeActionRequired, "session-version-unknown"
		case "corrupt_manifest":
			return retryResumeActionRequired, "session-manifest-corrupt"
		case "unsafe_path":
			return retryResumeActionRequired, "session-path-unsafe"
		case "lease_contention":
			return retryResumeActionRequired, "session-lease-contended"
		case "missing_lease", "needs_reconciliation", "discard_pending":
			return retryResumeActionRequired, "session-reconciliation-required"
		default:
			return retryResumeActionRequired, "session-reconciliation-required"
		}
	}

	if summary.HasManifest && hasAvailable && len(seen) == 1 {
		return retryResumeReuse, ""
	}
	return retryResumeActionRequired, "session-reconciliation-required"
}

func canRestartPreTransferFailure(state *jobState, summary engine.ResumeSummary) bool {
	if state == nil || state.durable.LastErrorCode != retryCodeYouTubeChallengePreTransfer || state.snap.Bytes != 0 {
		return false
	}
	if summary.HasManifest || summary.LeaseContended || summary.Classification != "" || len(summary.Classifications) != 0 {
		return false
	}
	return string(summary.Publication) == "" && string(summary.Cleanup) == "" && string(summary.Status) == ""
}

// committedBytesFromSummary sums the durable per-track checkpoints of a
// session. It is the ground truth for "did this attempt make progress",
// independent of progress telemetry that a dead URL never emits.
func committedBytesFromSummary(summary engine.ResumeSummary) int64 {
	var total int64
	for _, component := range summary.Components {
		if component.CommittedBytes > 0 {
			total += component.CommittedBytes
		}
	}
	return total
}

// shouldRestartZeroProgressFailure reports whether the validated session the
// retry would resume is pinned to a dead signed media URL. Callers must have
// classified the session as reusable first; this decision only escalates a
// certain reuse, never an uncertain session. Two signals qualify: the failure
// itself was a download-phase 403, or the previous resume attempt committed no
// new bytes relative to the failure before it.
func shouldRestartZeroProgressFailure(state *jobState, summary engine.ResumeSummary) bool {
	if state == nil {
		return false
	}
	if state.durable.LastErrorCode == retryCodeMediaLinkExpired {
		return true
	}
	return state.durable.ZeroProgressResumes >= zeroProgressResumeThreshold && committedBytesFromSummary(summary) > 0
}

// retryExhausted reports the durable "download again from Home" marker. The
// restart count alone must not exhaust a row: a fresh session that made
// progress and then failed transiently is still a valid resume.
func retryExhausted(state *jobState) bool {
	if state == nil {
		return false
	}
	return state.durable.LastErrorCode == retryCodeFreshDownloadRequired || state.snap.ErrorReason == retryCodeFreshDownloadRequired
}

// shouldSettleFreshDownloadRequired reports that Retry can no longer help:
// the restart budget is spent and this failure would otherwise demand another
// escalation — a dead link (download-phase 403) or a resume that froze the
// checkpoint exactly where the previous failure left it. It applies the same
// condition the retry-time decline gate uses.
func shouldSettleFreshDownloadRequired(state *jobState, err error, committedBytes int64, sawPostprocess bool) bool {
	if state == nil || state.durable.SessionRestarts < maxZeroProgressRestarts {
		return false
	}
	if isExpiredMediaLinkError(err) {
		return true
	}
	return committedBytes > 0 && !sawPostprocess && committedBytes == state.durable.LastFailureCommittedBytes
}

func (m *Manager) requireRetryAction(state *jobState, code string) error {
	if code == "" {
		code = "session-reconciliation-required"
	}
	if err := m.commitDurable(state, func(job *jobmodel.DurableJob, _ *jobmodel.State) error {
		job.Lifecycle = jobmodel.LifecycleActionRequired
		job.Desired = jobmodel.DesiredPaused
		job.ActionRequiredCode = code
		job.LastErrorCode = code
		return nil
	}); err != nil {
		m.mu.Lock()
		state.commanding = false
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.all[state.snap.ID] != state || state.snap.Status != StatusFailed || !state.commanding {
		state.commanding = false
		return errors.New("jobs: stale retry reconciliation")
	}
	state.snap.Status = StatusActionRequired
	state.snap.Message = "Retry needs review because saved download evidence could not be verified."
	state.snap.ErrorReason = code
	state.snap.CanPause = false
	state.commanding = false
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	m.emitQueueLocked()
	return nil
}

// declineZeroProgressRetry stops auto-escalation once the restart budget is
// spent. The row stays failed with durable guidance copy, because the only
// remaining escape is removing the item and downloading it again from Home.
func (m *Manager) declineZeroProgressRetry(state *jobState) error {
	if err := m.commitDurable(state, func(job *jobmodel.DurableJob, _ *jobmodel.State) error {
		job.LastErrorCode = retryCodeFreshDownloadRequired
		job.ActionRequiredCode = ""
		return nil
	}); err != nil {
		m.mu.Lock()
		state.commanding = false
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.all[state.snap.ID] != state || state.snap.Status != StatusFailed || !state.commanding {
		state.commanding = false
		return errors.New("jobs: stale retry rejection")
	}
	state.commanding = false
	state.snap.Message = freshDownloadRequiredNotice
	state.snap.ErrorReason = retryCodeFreshDownloadRequired
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	m.emitQueueLocked()
	return errors.New("jobs: retry escalation exhausted; remove the item and download it again")
}

// sessionCommittedBytes reads the session's durable checkpoints. It returns
// -1 when the evidence is unavailable, which leaves any recorded failure
// bookkeeping untouched rather than guessing zero.
func (m *Manager) sessionCommittedBytes(state *jobState) int64 {
	if state == nil || !state.fromStateV2 || state.durable.SessionID == "" || state.durable.OutputRoot.CanonicalPath == "" {
		return -1
	}
	summary, err := m.inspectResume(context.Background(), engineRootRef(state.durable.OutputRoot), state.durable.SessionID)
	if err != nil {
		return -1
	}
	return committedBytesFromSummary(summary)
}

// Retry re-queues a failed job under the same logical ID. It always creates a
// new attempt and FIFO ordinal after inspection authorizes either validated
// reuse or a fresh session. Uncertain evidence is retained as Action required
// and never silently rotated away.
func (m *Manager) Retry(id string) error {
	var state *jobState
	for {
		m.mu.Lock()
		if m.closing || m.closed {
			m.mu.Unlock()
			return ErrClosed
		}
		var ok bool
		state, ok = m.all[id]
		if !ok {
			m.mu.Unlock()
			return fmt.Errorf("jobs: unknown job %q", id)
		}
		if state.settling {
			done := state.done
			m.mu.Unlock()
			if done == nil {
				return errors.New("jobs: lifecycle transition is settling")
			}
			// Durable terminal acceptance happens just before the runtime
			// mirror releases the worker. Wait outside m.mu so a retry cannot
			// observe a failed State row with a still-claimed transition token.
			<-done
			continue
		}
		if state.commanding {
			m.mu.Unlock()
			return errors.New("jobs: lifecycle transition is settling")
		}
		if state.snap.Status == StatusCanceled {
			m.mu.Unlock()
			return errors.New("jobs: canceled jobs require DownloadAgain")
		}
		if state.snap.Status != StatusFailed {
			m.mu.Unlock()
			return fmt.Errorf("jobs: only failed jobs can be retried")
		}
		state.commanding = true
		m.mu.Unlock()
		break
	}

	if state.fromStateV2 {
		if retryExhausted(state) {
			return m.declineZeroProgressRetry(state)
		}
		sessionID := state.durable.SessionID
		retryMode := jobmodel.RetryModeResumeValidated
		escalatedRestart := false
		if sessionID == "" || state.durable.OutputRoot.CanonicalPath == "" {
			sessionID = newSessionID()
			retryMode = jobmodel.RetryModeRestartNewSession
		} else {
			summary, inspectErr := m.inspectResume(context.Background(), engineRootRef(state.durable.OutputRoot), sessionID)
			if inspectErr != nil {
				if canRestartPreTransferFailure(state, engine.ResumeSummary{}) {
					sessionID = newSessionID()
					retryMode = jobmodel.RetryModeRestartNewSession
				} else {
					return m.requireRetryAction(state, "recovery-session-unavailable")
				}
			} else {
				decision, actionCode := classifyRetryResume(summary)
				if decision != retryResumeReuse {
					if canRestartPreTransferFailure(state, summary) {
						sessionID = newSessionID()
						retryMode = jobmodel.RetryModeRestartNewSession
					} else {
						return m.requireRetryAction(state, actionCode)
					}
				} else {
					needsFreshSession := shouldRestartZeroProgressFailure(state, summary)
					if needsFreshSession && state.durable.SessionRestarts >= maxZeroProgressRestarts {
						return m.declineZeroProgressRetry(state)
					}
					if needsFreshSession {
						// Rotate the durable row onto a fresh session first. Discard
						// of the retired workspace is best-effort afterward; a
						// mid-life tombstone cannot be persisted while the job is
						// still live, and discarding before commit can strand the
						// row as action-required if the rotation fails.
						sessionID = newSessionID()
						retryMode = jobmodel.RetryModeRestartNewSession
						escalatedRestart = true
					}
				}
			}
		}
		previousSessionID := state.durable.SessionID
		if err := m.commitDurable(state, func(job *jobmodel.DurableJob, document *jobmodel.State) error {
			if escalatedRestart && previousSessionID != "" && previousSessionID != sessionID {
				upsertJobCleanupTombstone(document, job.ID, previousSessionID, job.OutputRoot, job.Reservation, "cleanup", time.Now().UTC())
			}
			job.Lifecycle = jobmodel.LifecyclePending
			job.Desired = jobmodel.DesiredRunning
			job.Phase = jobmodel.PhasePreparing
			job.AttemptID = uuid.NewString()
			job.SessionID = sessionID
			job.RetryMode = retryMode
			if escalatedRestart {
				job.LastFailureCommittedBytes = 0
				job.ZeroProgressResumes = 0
				job.SessionRestarts++
			}
			if document.NextQueueOrdinal == ^uint64(0) {
				return errors.New("jobs: queue ordinal exhausted")
			}
			job.QueueOrdinal = document.NextQueueOrdinal
			document.NextQueueOrdinal++
			return nil
		}); err != nil {
			m.mu.Lock()
			state.commanding = false
			m.mu.Unlock()
			return err
		}
		if escalatedRestart && previousSessionID != "" && previousSessionID != sessionID {
			m.discardRetiredSession(state, previousSessionID)
		}
	}

	m.mu.Lock()
	if m.closing || m.closed || m.all[id] != state || state.snap.Status != StatusFailed || !state.commanding {
		state.commanding = false
		m.mu.Unlock()
		return errors.New("jobs: stale retry request")
	}
	state.snap.Status = StatusPending
	state.snap.Progress = 0
	state.snap.Bytes = 0
	state.snap.Total = 0
	state.snap.SpeedBps = 0
	state.snap.ETASeconds = 0
	state.snap.Message = ""
	state.snap.ErrorReason = ""
	state.snap.StartedAt = ""
	state.snap.CompletedAt = ""
	state.startBps = time.Time{}
	state.startByt = 0
	state.commanding = false
	m.order = append(m.order, id)
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	m.maybeStartNextLocked()
	m.emitQueueLocked()
	m.mu.Unlock()
	return nil
}

// DownloadAgain creates a fresh logical row for a canceled job. The original
// canceled row is retained as history and no old session identity is reused.
// The reservation is cloned into a new group inside the same State
// transaction; a caller that needs a new suffix should use V1 admission
// before calling SubmitAdmitted for the replacement row.
func (m *Manager) DownloadAgain(id string) (string, error) {
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return "", ErrClosed
	}
	state, ok := m.all[id]
	if !ok {
		m.mu.Unlock()
		return "", fmt.Errorf("jobs: unknown job %q", id)
	}
	if state.snap.Status != StatusCanceled || state.commanding {
		m.mu.Unlock()
		return "", errors.New("jobs: only canceled jobs can be downloaded again")
	}
	state.commanding = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.all[id] == state {
			state.commanding = false
		}
		m.mu.Unlock()
	}()

	newID := uuid.NewString()
	newAttempt := uuid.NewString()
	newSession := newSessionID()
	if state.fromStateV2 {
		store := m.stateStoreSnapshot()
		if store == nil {
			return "", errors.New("jobs: State v2 store is not configured")
		}
		state.transitionMu.Lock()
		defer state.transitionMu.Unlock()
		var durable jobmodel.DurableJob
		err := m.transaction(store, []jobmodel.JobPrecondition{{
			ID: state.durable.ID, Revision: state.durable.Revision, Lifecycle: state.durable.Lifecycle,
			AttemptID: state.durable.AttemptID, SessionID: state.durable.SessionID, OutputRoot: state.durable.OutputRoot,
		}}, func(document *jobmodel.State) error {
			if document.NextQueueOrdinal == ^uint64(0) {
				return errors.New("jobs: queue ordinal exhausted")
			}
			clone := state.durable
			clone.ID = newID
			clone.Revision = 1
			clone.AttemptID = newAttempt
			clone.SessionID = newSession
			clone.QueueOrdinal = document.NextQueueOrdinal
			clone.Lifecycle = jobmodel.LifecyclePending
			clone.Phase = jobmodel.PhasePreparing
			clone.Desired = jobmodel.DesiredRunning
			clone.RetryMode = jobmodel.RetryModeNone
			clone.ActionRequiredCode = ""
			clone.LastErrorCode = ""
			clone.LastFailureCommittedBytes = 0
			clone.ZeroProgressResumes = 0
			clone.SessionRestarts = 0
			clone.CreatedAt = time.Now().UTC()
			clone.UpdatedAt = clone.CreatedAt
			clone.Reservation.GroupID = newID
			document.Jobs = append(document.Jobs, clone)
			document.NextQueueOrdinal++
			durable = clone
			return nil
		})
		if err != nil {
			return "", err
		}
		plan := state.plan
		newState := &jobState{
			snap:               state.snap,
			plan:               plan,
			outputTemplate:     state.outputTemplate,
			options:            state.options.Clone(),
			durable:            durable,
			fromStateV2:        true,
			done:               make(chan struct{}),
			authorityRevision:  durable.Revision,
			authorityAttemptID: durable.AttemptID,
		}
		newState.snap.ID = newID
		newState.snap.Status = StatusPending
		newState.snap.Message = "Queued"
		newState.snap.Lifecycle = durable.Lifecycle
		newState.snap.Phase = durable.Phase
		newState.snap.Desired = durable.Desired
		newState.snap.OccupiesSlot = false
		newState.snap.CanPause = false
		newState.snap.Processing = false
		newState.snap.CreatedAt = durable.CreatedAt.UTC().Format(time.RFC3339)
		newState.snap.StartedAt = ""
		newState.snap.CompletedAt = ""
		newState.snap.Progress = 0
		newState.snap.Bytes = 0
		newState.snap.Total = 0
		newState.snap.SpeedBps = 0
		newState.snap.ETASeconds = 0
		newState.snap.ErrorReason = ""
		m.mu.Lock()
		if m.closing || m.closed {
			m.mu.Unlock()
			return "", ErrClosed
		}
		m.all[newID] = newState
		m.order = append(m.order, newID)
		m.emitLocked(Event{Name: EventJobUpdate, Job: newState.snap})
		m.maybeStartNextLocked()
		m.emitQueueLocked()
		m.mu.Unlock()
		return newID, nil
	}

	request := Request{
		URL: state.snap.URL, VideoID: state.snap.VideoID, Title: state.snap.Title,
		Channel: state.snap.Channel, Quality: state.snap.Quality, PlanID: state.snap.PlanID,
		OutputDir: state.snap.OutputDir, Duration: state.snap.DurationLabel, Thumbnail: state.snap.Thumbnail,
	}
	return m.submit(newID, request, state.plan, stringPtr(state.outputTemplate))
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func newSessionID() string { return strings.ReplaceAll(uuid.NewString(), "-", "") }

// Remove drops a terminal job from the manager entirely.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	state, ok := m.all[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("jobs: unknown job %q", id)
	}
	switch state.snap.Status {
	case StatusComplete, StatusFailed, StatusCanceled:
	default:
		m.mu.Unlock()
		return errors.New("jobs: only terminal jobs can be removed")
	}
	if pendingCleanup, _ := m.cleanupStatusLocked(id); pendingCleanup {
		m.mu.Unlock()
		return errors.New("jobs: saved temporary data cleanup is still pending")
	}
	if state.commanding || state.settling {
		m.mu.Unlock()
		return errors.New("jobs: lifecycle transition is settling")
	}
	if state.fromStateV2 {
		state.commanding = true
		m.mu.Unlock()
		if err := m.removeDurable(state); err != nil {
			m.mu.Lock()
			state.commanding = false
			m.mu.Unlock()
			return err
		}
		m.mu.Lock()
		if m.all[id] == state {
			delete(m.all, id)
			m.removeFromOrderLocked(id)
			m.emitQueueLocked()
		}
		m.mu.Unlock()
		return nil
	}
	delete(m.all, id)
	m.removeFromOrderLocked(id)
	m.emitQueueLocked()
	m.mu.Unlock()
	return nil
}

// ClearTerminal removes every job in a terminal state.
func (m *Manager) ClearTerminal() {
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return
	}
	v2IDs := make([]string, 0)
	for id, state := range m.all {
		switch state.snap.Status {
		case StatusComplete, StatusFailed, StatusCanceled:
			if state.fromStateV2 {
				v2IDs = append(v2IDs, id)
			} else {
				delete(m.all, id)
				m.removeFromOrderLocked(id)
			}
		}
	}
	m.emitQueueLocked()
	m.mu.Unlock()
	for _, id := range v2IDs {
		_ = m.Remove(id)
	}
}

// Find returns a snapshot by id.
func (m *Manager) Find(id string) (JobSnapshot, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.all[id]
	if !ok {
		return JobSnapshot{}, false
	}
	return state.snap, true
}

// Active returns the currently running job id, if any.
func (m *Manager) Active() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.active {
		return id
	}
	return ""
}

// HasActive reports whether any worker still owns a download slot. It is the
// native close gate; durable lifecycle labels alone never claim occupancy.
func (m *Manager) HasActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active) != 0
}

// QuitSummary returns the counts needed by the ordinary pause-and-quit
// confirmation. It is safe to call from a Wails event handler.
func (m *Manager) QuitSummary() QuitSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	summary := QuitSummary{ActiveDownloads: len(m.active)}
	for id, state := range m.all {
		if m.active[id] != nil {
			continue
		}
		switch state.snap.Status {
		case StatusPending, StatusPaused, StatusPausing, StatusCanceling:
			summary.WaitingOrPausedDownloads++
		}
	}
	return summary
}

// SetConcurrency updates the number of simultaneous downloads. Existing jobs
// are allowed to finish when the limit is lowered.
func (m *Manager) SetConcurrency(value int) int {
	if value < 1 {
		value = 1
	}
	if value > MaxDownloadConcurrency {
		value = MaxDownloadConcurrency
	}
	m.mu.Lock()
	if m.closing || m.closed {
		current := m.concurrency
		m.mu.Unlock()
		return current
	}
	m.concurrency = value
	m.maybeStartNextLocked()
	m.emitQueueLocked()
	m.mu.Unlock()
	return value
}

func (m *Manager) Concurrency() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.concurrency
}

// maybeStartNextLocked fills every available download slot from the FIFO.
// Caller must hold m.mu.
func (m *Manager) maybeStartNextLocked() {
	if m.closing || m.closed {
		return
	}
	for len(m.active) < m.concurrency && len(m.order) > 0 {
		id := m.order[0]
		state, ok := m.all[id]
		if !ok {
			m.order = m.order[1:]
			continue
		}
		if state.commanding || state.settling {
			// A FIFO-head row is in a durable lifecycle transition. Do not
			// bypass it or start it before its winner is reflected in memory.
			break
		}
		if state.snap.Status != StatusPending {
			m.order = m.order[1:]
			continue
		}
		ctx, cancel := context.WithCancelCause(context.Background())
		worker := &worker{
			JobID:     id,
			AttemptID: state.durable.AttemptID,
			SessionID: state.durable.SessionID,
			Cancel:    cancel,
			Ctx:       ctx,
			Arbiter:   engine.NewPublicationArbiter(),
			Done:      make(chan struct{}),
		}
		state.worker = worker
		state.done = worker.Done
		if state.fromStateV2 {
			// Prevent Pause/Cancel from racing the pending-to-active State
			// transaction. The flag is cleared immediately before the runner
			// starts, after durable activation succeeds.
			state.commanding = true
		}
		state.snap.Status = StatusActive
		state.snap.OccupiesSlot = true
		if state.fromStateV2 {
			state.snap.Lifecycle = jobmodel.LifecycleActive
			state.snap.Desired = jobmodel.DesiredRunning
			state.snap.Phase = jobmodel.PhasePreparing
		}
		state.snap.StartedAt = time.Now().UTC().Format(time.RFC3339)
		state.snap.Message = "Preparing"
		state.snap.CanPause = true
		m.active[id] = worker
		m.order = m.order[1:]
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
		go m.startWorker(state, worker)
	}
}

// startWorker commits the durable pending-to-active transition before the
// engine runner is invoked. It runs outside m.mu so State v2 lock acquisition
// cannot block queue inspection or lifecycle commands.
func (m *Manager) startWorker(state *jobState, worker *worker) {
	if state.fromStateV2 {
		if err := m.commitDurable(state, func(job *jobmodel.DurableJob, _ *jobmodel.State) error {
			if job.Lifecycle != jobmodel.LifecyclePending || job.Desired != jobmodel.DesiredRunning {
				return errActivationSuperseded
			}
			job.Lifecycle = jobmodel.LifecycleActive
			job.Phase = jobmodel.PhasePreparing
			return nil
		}); err != nil {
			m.mu.Lock()
			if current := m.active[state.snap.ID]; current == worker {
				delete(m.active, state.snap.ID)
				state.worker = nil
				state.commanding = false
				state.snap.Status = StatusFailed
				state.snap.OccupiesSlot = false
				state.snap.Lifecycle = jobmodel.LifecyclePending
				state.snap.Message = "Could not start"
				state.snap.ErrorReason = "persistence"
				state.snap.CanPause = false
				state.snap.CompletedAt = time.Now().UTC().Format(time.RFC3339)
				m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
				m.maybeStartNextLocked()
				m.emitQueueLocked()
				close(worker.Done)
			}
			m.mu.Unlock()
			return
		}
	}
	m.mu.Lock()
	if m.active[state.snap.ID] != worker || state.worker != worker {
		m.mu.Unlock()
		close(worker.Done)
		return
	}
	state.commanding = false
	m.mu.Unlock()
	m.run(state, worker)
}

// subtitleEngineOptions maps UI subtitle preferences onto the engine request.
// Embedding always converts to VTT: WebM containers only accept VTT tracks and
// MP4-family embedding re-encodes to mov_text internally, so one normalized
// format covers every plan VidStow offers. An empty mode disables subtitles.
func subtitleEngineOptions(options jobmodel.OutputOptions) engine.SubtitleOptions {
	if options.SubtitleMode == "" {
		return engine.SubtitleOptions{}
	}
	subs := engine.SubtitleOptions{
		WriteManual:    true,
		WriteAutomatic: options.SubtitleAutoCaptions,
		Languages:      options.SubtitleLanguages,
	}
	if options.SubtitleMode == jobmodel.SubtitleModeEmbed {
		subs.Embed = true
		subs.ConvertFormat = "vtt"
	} else {
		subs.ConvertFormat = options.SubtitleFormat
	}
	return subs
}

func (m *Manager) run(state *jobState, worker *worker) {
	defer close(worker.Done)
	started := time.Now()

	m.mu.Lock()
	req := engine.Request{
		URL:            state.snap.URL,
		OutputDir:      state.snap.OutputDir,
		Format:         state.snap.Quality.ytdlpFormat(),
		OutputTemplate: state.snap.Quality.outputTemplate(),
		Overwrite:      true,
		Playlist:       engine.PlaylistOptions{Disabled: true},
		Filesystem: engine.FilesystemOptions{
			FfmpegLocation:          m.ffmpegLocation,
			PreservePartialOnCancel: true,
		},
	}
	if state.plan != nil {
		req.Format = state.plan.Selector
		if state.outputTemplate != "" {
			req.OutputTemplate = state.outputTemplate
		} else {
			req.OutputTemplate = OutputTemplateForPlan(*state.plan)
		}
		if state.plan.Kind == outputplan.KindVideo {
			req.MergeOutputFormat = strings.ToLower(state.plan.Container)
		}
		if state.plan.Container == "MP3" {
			req.Postprocessors = []engine.Postprocessor{{
				ExtractAudio: &engine.ExtractAudioPostprocessor{
					Codec:   "mp3",
					Bitrate: fmt.Sprintf("%dk", state.plan.AudioBitrateKbps),
				},
			}}
		}
	}
	if !state.options.IsZero() {
		req.Subtitles = subtitleEngineOptions(state.options)
		if state.options.EmbedMetadata {
			req.EmbedMetadata = true
		}
		if state.options.EmbedMetadata || state.options.EmbedChapters {
			embedChapters := state.options.EmbedChapters
			req.EmbedChapters = &embedChapters
		}
		if state.options.EmbedThumbnail {
			req.Thumbnails = engine.ThumbnailOptions{Write: true, Embed: true}
		}
	}
	if state.fromStateV2 {
		req.OutputDir = state.durable.OutputRoot.CanonicalPath
		req.Overwrite = false
		req.Filesystem.Resume = engine.ResumeOptions{
			SessionID:          worker.SessionID,
			PublicationArbiter: worker.Arbiter,
			CommitTargets:      resumeCommitTargets(state.durable.Reservation),
		}
	}
	ctx := worker.Ctx
	runner := m.runDownload
	m.mu.Unlock()

	processingHeld := false
	sawPostprocess := false
	sawDownload := false
	handler := func(ctx context.Context, ev engine.Event) error {
		if ev.Kind == engine.EventDownloadStarting {
			sawDownload = true
		}
		if ev.Kind == engine.EventPostprocessStarting && !processingHeld {
			select {
			case m.processing <- struct{}{}:
				processingHeld = true
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if ev.Kind == engine.EventPostprocessStarting {
			sawPostprocess = true
		}
		m.handleEventAttempt(state, worker, ev)
		if ev.Kind == engine.EventPostprocessCompleted && processingHeld {
			<-m.processing
			processingHeld = false
		}
		return nil
	}

	result, err := runner(ctx, req, handler)
	diagnostic := terminalDownloadDiagnostic(err, sawDownload, sawPostprocess, time.Since(started))
	if processingHeld {
		<-m.processing
	}

	cause := context.Cause(ctx)
	paused := errors.Is(cause, engine.ErrPauseRequested)
	canceled := errors.Is(cause, errCancelRequested)
	if !paused && !canceled && errors.Is(cause, context.Canceled) {
		canceled = true
	}
	if paused || canceled {
		diagnostic = nil
	}

	// Checkpoint evidence is read outside m.mu because inspection does disk
	// I/O. -1 (unknown) keeps prior failure bookkeeping untouched.
	failureCommitted := int64(-1)
	if err != nil && !paused && !canceled {
		failureCommitted = m.sessionCommittedBytes(state)
	}

	m.mu.Lock()
	if state.worker != worker || m.active[state.snap.ID] != worker {
		m.mu.Unlock()
		return
	}
	state.settling = true
	terminal := state.snap
	if paused {
		terminal.Status = StatusPaused
		terminal.Message = "Paused"
		terminal.ErrorReason = ""
		terminal.CompletedAt = ""
	} else if canceled {
		terminal.Status = StatusCanceled
		terminal.Message = "Canceled"
		terminal.ErrorReason = "canceled"
		terminal.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	} else if err != nil {
		terminal.Status = StatusFailed
		terminal.Message = failureMessage(err, failureCommitted, sawPostprocess)
		terminal.ErrorReason = errorReason(err)
		if shouldSettleFreshDownloadRequired(state, err, failureCommitted, sawPostprocess) {
			terminal.Message = freshDownloadRequiredNotice
			terminal.ErrorReason = retryCodeFreshDownloadRequired
		}
		terminal.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		if terminal.Bytes == 0 {
			terminal.Progress = 0
		} else if terminal.Total > 0 {
			terminal.Progress = clampFloat(float64(terminal.Bytes) / float64(terminal.Total))
		} else {
			terminal.Progress = 0
		}
	} else {
		terminal.Status = StatusComplete
		terminal.Message = "Completed"
		terminal.Progress = 1
		terminal.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		if result.Filename != "" {
			terminal.Filename = filepath.Base(result.Filename)
			if abs, absErr := filepath.Abs(result.Filename); absErr == nil {
				terminal.AbsolutePath = abs
			}
			if terminal.Bytes == 0 {
				terminal.Bytes = result.Bytes
			}
		}
		if terminal.Bytes == 0 {
			terminal.Bytes = result.Bytes
		}
		if terminal.Title == "" {
			terminal.Title = terminal.Filename
		}
	}
	terminal.CanPause = false
	terminal.Processing = false
	state.snap = terminal
	m.mu.Unlock()

	cleanupPending := false
	cleanupCode := ""
	if canceled {
		cleanupPending, cleanupCode = m.discardSession(state)
	}
	var settleErr error
	if state.fromStateV2 {
		switch {
		case paused:
			settleErr = m.settleDurable(state, jobmodel.LifecyclePaused, jobmodel.DesiredPaused, jobmodel.PhasePreparing, terminal, "", false, -1)
		case canceled:
			settleErr = m.settleDurable(state, jobmodel.LifecycleCanceled, jobmodel.DesiredCanceled, jobmodel.PhaseCleaningUp, terminal, cleanupCode, cleanupPending, -1)
		case err != nil:
			settleErr = m.settleDurable(state, jobmodel.LifecycleFailed, jobmodel.DesiredRunning, jobmodel.PhasePreparing, terminal, terminal.ErrorReason, false, failureCommitted)
		default:
			settleErr = m.settleDurable(state, jobmodel.LifecycleCompleted, jobmodel.DesiredRunning, jobmodel.PhaseReadyToPublish, terminal, "", false, -1)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if state.worker != worker || m.active[state.snap.ID] != worker {
		return
	}
	if settleErr != nil && state.fromStateV2 {
		state.snap.Status = StatusFailed
		state.snap.Message = "Could not save lifecycle state"
		state.snap.ErrorReason = "persistence"
		diagnostic = &Diagnostic{Stage: "persistence", Category: "state_unavailable", Duration: time.Since(started)}
	}
	state.settling = false
	state.commanding = false
	state.snap.CanPause = false
	state.snap.Processing = false
	state.snap.OccupiesSlot = false
	delete(m.active, state.snap.ID)
	state.worker = nil
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap, Diagnostic: diagnostic})
	m.maybeStartNextLocked()
	m.emitQueueLocked()
}

// terminalDownloadDiagnostic maps only typed engine facts. Unknown errors keep
// a coarse stage-specific category rather than serializing or matching text.
func terminalDownloadDiagnostic(err error, sawDownload, sawPostprocess bool, duration time.Duration) *Diagnostic {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		stage := "extraction"
		if sawDownload {
			stage = "media_transfer"
		}
		return &Diagnostic{Stage: stage, Category: "network_timeout", Duration: duration}
	}
	if errors.Is(err, context.Canceled) || engine.IsCategory(err, engine.ErrorCancelled) {
		return nil
	}
	problem := &Diagnostic{Stage: "media_transfer", Category: "transfer_failed", Duration: duration}
	if sawPostprocess {
		problem.Stage, problem.Category = "postprocessing", "ffmpeg_failed"
		return problem
	}
	if !sawDownload {
		problem.Stage, problem.Category = "extraction", "extractor_failed"
		var typed *engine.Error
		if errors.As(err, &typed) {
			switch typed.Category {
			case engine.ErrorAuthentication:
				problem.Category = "authentication_required"
			case engine.ErrorUnsupported:
				problem.Category = "unsupported_resource"
			}
		}
		return problem
	}
	if code, ok := engine.DownloadHTTPStatusCode(err); ok {
		switch code {
		case http.StatusForbidden:
			problem.Category = "http_403"
		case http.StatusTooManyRequests:
			problem.Category = "http_429"
		}
	}
	return problem
}

func literalOutputTemplate(basename string) (string, error) {
	if err := reservation.ValidateBasename(basename); err != nil {
		return "", fmt.Errorf("jobs: admitted output basename: %w", err)
	}
	encoded := strings.ReplaceAll(basename, "%", "%%")
	if len(encoded) > reservation.MaxBasenameBytes*2 {
		return "", errors.New("jobs: admitted output template exceeds size bound")
	}
	return encoded, nil
}

func (m *Manager) handleEvent(state *jobState, ev engine.Event) {
	m.handleEventAttempt(state, nil, ev)
}

func (m *Manager) handleEventAttempt(state *jobState, worker *worker, ev engine.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if worker != nil && (state.worker != worker || worker.Ctx.Err() != nil) {
		return
	}

	switch ev.Kind {
	case engine.EventDownloadStarting:
		state.snap.Message = "Starting download"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	case engine.EventDownloadProgress:
		if ev.Bytes > 0 {
			state.snap.Bytes = ev.Bytes
		}
		if ev.Total > 0 {
			state.snap.Total = ev.Total
		}
		if state.snap.Total > 0 {
			state.snap.Progress = clampFloat(float64(state.snap.Bytes) / float64(state.snap.Total))
		}
		// ETA / speed are computed locally from a short rolling window.
		now := time.Now()
		if state.startBps.IsZero() {
			state.startBps = now
			state.startByt = state.snap.Bytes
		}
		elapsed := now.Sub(state.startBps).Seconds()
		if elapsed >= 0.5 {
			delta := state.snap.Bytes - state.startByt
			if delta > 0 {
				state.snap.SpeedBps = float64(delta) / elapsed
				if state.snap.Total > 0 && state.snap.SpeedBps > 0 {
					remaining := float64(state.snap.Total - state.snap.Bytes)
					state.snap.ETASeconds = remaining / state.snap.SpeedBps
				}
			}
			state.startBps = now
			state.startByt = state.snap.Bytes
		}
		state.snap.Message = "Downloading"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	case engine.EventDownloadRetry, engine.EventExtractorRetry:
		state.snap.Message = "Retrying"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	case engine.EventDownloadCompleted:
		if ev.Bytes > 0 {
			state.snap.Bytes = ev.Bytes
		}
		state.snap.Message = "Finalising"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	case engine.EventPostprocessStarting, engine.EventPostprocessProgress:
		state.snap.Processing = true
		state.snap.CanPause = false
		state.snap.Message = "Finalising"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	case engine.EventPostprocessCompleted:
		state.snap.Processing = false
		state.snap.CanPause = state.snap.Status == StatusActive
		state.snap.Message = "Finalising"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	case engine.EventDownloadCancelled:
		state.snap.Message = "Canceled"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	case engine.EventJavaScriptChallenge:
		// Secret-free engine diagnostics are available to dedicated event
		// consumers, but must not replace the job's user-facing status text.
	default:
		if ev.Message != "" {
			state.snap.Message = humanMessage(ev.Message)
			m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
		}
	}
}

// emitLocked queues an immutable event for ordered delivery. Caller must hold
// m.mu, which makes append order the authoritative sequence. The dispatcher
// never calls listeners while either manager lock is held, so a listener can
// safely ask the manager for a fresh snapshot without deadlocking.
func (m *Manager) emitLocked(ev Event) {
	if ev.Name == EventJobUpdate {
		// Progress is presentation state, but it still needs an authoritative
		// QueueView. Do not rotate authority for telemetry-only updates.
		m.queueRevision++
		view := m.queueViewLocked()
		ev.QueueView = &view
	}
	m.schedulePersistLocked()
	m.enqueueEvent(ev)
}

func (m *Manager) enqueueEvent(ev Event) {
	if m.listener == nil {
		return
	}
	m.eventMu.Lock()
	if m.eventClosed {
		m.eventMu.Unlock()
		return
	}
	m.pendingEvents = append(m.pendingEvents, ev)
	m.eventMu.Unlock()
	select {
	case m.eventSignal <- struct{}{}:
	default:
	}
}

// emitQueueLocked emits a queue:update event with the current display
// list. Caller must hold m.mu.
func (m *Manager) emitQueueLocked() {
	m.queueRevision++
	view := m.queueViewLocked()
	m.emitLocked(Event{Name: EventQueue, Queue: m.snapshotLocked(), QueueView: &view})
}

func (m *Manager) snapshotLocked() []JobSnapshot {
	out := make([]JobSnapshot, 0, len(m.all))
	for _, state := range m.all {
		out = append(out, state.snap)
	}
	sort.Slice(out, func(i, j int) bool {
		// Active first, then pending in creation order, then terminal
		// jobs by completed-at descending.
		ri, rj := statusRank(out[i].Status), statusRank(out[j].Status)
		if ri != rj {
			return ri < rj
		}
		if out[i].Status == StatusActive || out[i].Status == StatusPending {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].CompletedAt > out[j].CompletedAt
	})
	return out
}

func (m *Manager) removeFromOrderLocked(id string) {
	for i, candidate := range m.order {
		if candidate == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}

func statusRank(s Status) int {
	switch s {
	case StatusActive:
		return 0
	case StatusPausing, StatusCanceling:
		return 0
	case StatusPaused:
		return 1
	case StatusPending:
		return 2
	case StatusFailed:
		return 3
	case StatusCanceled:
		return 4
	case StatusActionRequired:
		return 5
	case StatusComplete:
		return 6
	}
	return 5
}

func ensureDir(dir string) error {
	if dir == "" {
		return nil
	}
	if strings.HasPrefix(dir, "~") {
		if home, err := userHomeDir(); err == nil {
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
		}
	}
	return mkdirAll(dir, 0o755)
}

func humanError(err error) string {
	if err == nil {
		return "Failed"
	}
	switch {
	case errors.Is(err, syscall.ENOSPC):
		return "Not enough disk space"
	case errors.Is(err, os.ErrPermission):
		return "The download folder is not writable"
	}
	var typed *engine.Error
	if errors.As(err, &typed) {
		switch typed.Category {
		case engine.ErrorUnsupported:
			if isYouTubeChallengeTimeout(err) {
				return "YouTube challenge timed out — retry"
			}
			return "This link is not supported"
		case engine.ErrorAuthentication:
			return "Sign-in is required for this video"
		case engine.ErrorInvalidInput:
			return "The link is not valid"
		case engine.ErrorNetwork:
			return "Network error"
		case engine.ErrorCancelled:
			return "Canceled"
		case engine.ErrorSecurity:
			return "The download was blocked by a security check"
		}
	}
	return humanMessage(err.Error())
}

func isYouTubeChallengeTimeout(err error) bool {
	message := err.Error()
	return strings.Contains(message, "JavaScript challenge solver unavailable") &&
		strings.Contains(message, "EJS helper timeout") &&
		strings.Contains(message, "JavaScript execution timed out")
}

// isExpiredMediaLinkError matches only a typed 403 from the media downloader,
// so it cannot fire for extraction, API, post-processing, or coincidental error
// text. A 403 from the signed googlevideo URL means the captured link is dead.
func isExpiredMediaLinkError(err error) bool {
	code, ok := engine.DownloadHTTPStatusCode(err)
	return ok && code == http.StatusForbidden
}

// failureMessage humanizes a failed download attempt. Mid-transfer failures
// explain what retry will do: a dead link restarts with a fresh session (the
// manager escalates those on the next retry), anything else with saved bytes
// resumes first and only restarts once a resume stops making progress.
// committedBytes < 0 means checkpoint evidence is unknown. Post-processing
// failures keep their own copy because their bytes are complete.
func failureMessage(err error, committedBytes int64, sawPostprocess bool) string {
	if isExpiredMediaLinkError(err) {
		return mediaLinkExpiredMessage
	}
	if committedBytes > 0 && !sawPostprocess {
		return downloadStoppedMidMessage
	}
	return humanError(err)
}

func humanMessage(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) > 240 {
		return s[:237] + "..."
	}
	return s
}

func errorReason(err error) string {
	if isYouTubeChallengeTimeout(err) {
		return retryCodeYouTubeChallengePreTransfer
	}
	if isExpiredMediaLinkError(err) {
		return retryCodeMediaLinkExpired
	}
	switch {
	case errors.Is(err, syscall.ENOSPC):
		return "disk_full"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	}
	var typed *engine.Error
	if errors.As(err, &typed) {
		return string(typed.Category)
	}
	return "internal"
}

func clampFloat(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func isKnownQuality(quality Quality) bool {
	for _, candidate := range AllQualities {
		if candidate == quality {
			return true
		}
	}
	return false
}

// PlaylistSummary is a lightweight flat-playlist preview. Child formats are
// deliberately not extracted until their individual queue jobs run.
type PlaylistSummary struct {
	ID          string                 `json:"id"`
	URL         string                 `json:"url"`
	Title       string                 `json:"title"`
	Channel     string                 `json:"channel"`
	Thumbnail   string                 `json:"thumbnail"`
	EntryCount  int                    `json:"entryCount"`
	Available   int                    `json:"available"`
	Unavailable int                    `json:"unavailable"`
	Entries     []PlaylistEntrySummary `json:"entries"`
}

type PlaylistEntrySummary struct {
	Index     int    `json:"index"`
	VideoID   string `json:"videoId"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	Duration  string `json:"duration,omitempty"`
	Thumbnail string `json:"thumbnail,omitempty"`
	Available bool   `json:"available"`
}

// InfoSummary is the metadata displayed on the Home page after analyse.
type InfoSummary struct {
	Title           string             `json:"title"`
	Channel         string             `json:"channel"`
	Duration        string             `json:"duration"`
	DurationSeconds int64              `json:"durationSeconds"`
	Thumbnail       string             `json:"thumbnail"`
	VideoID         string             `json:"videoId"`
	URL             string             `json:"url"`
	ViewCount       int64              `json:"viewCount"`
	UploadDate      string             `json:"uploadDate"`
	Description     string             `json:"description"`
	MediaType       string             `json:"mediaType,omitempty"`
	Access          AccessSummary      `json:"access"`
	Subtitles       []SubtitleLanguage `json:"subtitles,omitempty"`
	Plans           []outputplan.Plan  `json:"plans"`
}

// SubtitleLanguage is one caption track reported by analysis, used to render
// the language picker. Only the code, display name, and origin cross the
// desktop boundary; track URLs and payloads are dropped.
type SubtitleLanguage struct {
	Code string `json:"code"`
	Name string `json:"name,omitempty"`
	Auto bool   `json:"auto,omitempty"`
}

// AccessSummary is informational extraction metadata, not a product gate.
// Selectable outputs remain exactly the curated plans returned by Analyze.
type AccessSummary struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// AnalyzePlaylist obtains bounded URL-result metadata without extracting each
// child. The cached summary prevents the renderer from later submitting
// invented entry identities.
func (m *Manager) AnalyzePlaylist(ctx context.Context, rawURL string) (PlaylistSummary, error) {
	if strings.TrimSpace(rawURL) == "" {
		return PlaylistSummary{}, errors.New("analyze playlist: empty url")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return PlaylistSummary{}, ErrClosed
	}
	ffmpegLocation, lifecycleCtx, runner := m.ffmpegLocation, m.lifecycleCtx, m.runAnalyze
	m.analysisWG.Add(1)
	m.mu.Unlock()
	analysisCtx, cancel := context.WithCancel(ctx)
	stopLifecycle := context.AfterFunc(lifecycleCtx, cancel)
	defer func() { stopLifecycle(); cancel(); m.analysisWG.Done() }()
	result, err := runner(analysisCtx, engine.Request{
		URL: rawURL, Simulate: true,
		Playlist:   engine.PlaylistOptions{Flat: true, End: MaxPlaylistEntries},
		Filesystem: engine.FilesystemOptions{FfmpegLocation: ffmpegLocation},
	})
	if err != nil {
		return PlaylistSummary{}, err
	}
	summary, err := summarizePlaylist(result, rawURL)
	if err != nil {
		return PlaylistSummary{}, err
	}
	expectedID := playlistIDFromURL(rawURL)
	if expectedID == "" || summary.ID != expectedID {
		return PlaylistSummary{}, errors.New("analyze playlist: playlist identity mismatch")
	}
	if len(summary.Entries) == 0 {
		return PlaylistSummary{}, errors.New("analyze playlist: no downloadable videos found")
	}
	m.mu.Lock()
	if !m.closing && !m.closed {
		now := time.Now()
		for id, cached := range m.playlistCache {
			if !now.Before(cached.expiresAt) {
				delete(m.playlistCache, id)
			}
		}
		if len(m.playlistCache) >= maxCachedPlaylistPreviews {
			oldestID, oldestExpiry := "", time.Time{}
			for id, cached := range m.playlistCache {
				if oldestID == "" || cached.expiresAt.Before(oldestExpiry) {
					oldestID, oldestExpiry = id, cached.expiresAt
				}
			}
			delete(m.playlistCache, oldestID)
		}
		m.playlistCache[summary.ID] = cachedPlaylist{summary: summary, expiresAt: now.Add(30 * time.Minute)}
	}
	m.mu.Unlock()
	return summary, nil
}

// ResolvePlaylistSelection verifies renderer-selected positions against the
// unexpired backend preview. It returns canonical, available entries in
// playlist order and never accepts caller-provided child URLs or identities.
func (m *Manager) ResolvePlaylistSelection(playlistID string, selected []int) (PlaylistSummary, []PlaylistEntrySummary, error) {
	if strings.TrimSpace(playlistID) == "" || len(selected) == 0 || len(selected) > MaxPlaylistEntries {
		return PlaylistSummary{}, nil, errors.New("jobs: invalid playlist selection")
	}
	requested := make(map[int]struct{}, len(selected))
	for _, index := range selected {
		if index <= 0 {
			return PlaylistSummary{}, nil, errors.New("jobs: invalid playlist position")
		}
		if _, duplicate := requested[index]; duplicate {
			return PlaylistSummary{}, nil, errors.New("jobs: duplicate playlist position")
		}
		requested[index] = struct{}{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing || m.closed {
		return PlaylistSummary{}, nil, ErrClosed
	}
	cached, ok := m.playlistCache[playlistID]
	if !ok || !time.Now().Before(cached.expiresAt) {
		delete(m.playlistCache, playlistID)
		return PlaylistSummary{}, nil, errors.New("jobs: playlist preview expired; analyze the playlist again")
	}
	summary := cached.summary
	summary.Entries = append([]PlaylistEntrySummary(nil), cached.summary.Entries...)
	entries := make([]PlaylistEntrySummary, 0, len(requested))
	previewIndexes := make(map[int]struct{}, len(summary.Entries))
	for _, entry := range summary.Entries {
		if _, duplicate := previewIndexes[entry.Index]; duplicate {
			return PlaylistSummary{}, nil, errors.New("jobs: playlist preview contains duplicate positions")
		}
		previewIndexes[entry.Index] = struct{}{}
		if _, wanted := requested[entry.Index]; !wanted {
			continue
		}
		if !entry.Available || entry.URL == "" || !videoIDPattern.MatchString(entry.VideoID) {
			return PlaylistSummary{}, nil, errors.New("jobs: selected playlist entry is unavailable")
		}
		entries = append(entries, entry)
		delete(requested, entry.Index)
	}
	if len(requested) != 0 || len(entries) != len(selected) {
		return PlaylistSummary{}, nil, errors.New("jobs: playlist selection no longer matches the preview")
	}
	return summary, entries, nil
}

func summarizePlaylist(result engine.Result, rawURL string) (PlaylistSummary, error) {
	var parent map[string]any
	if len(result.InfoJSON) > 0 && json.Unmarshal(result.InfoJSON, &parent) != nil {
		return PlaylistSummary{}, errors.New("analyze playlist: invalid metadata")
	}
	summary := PlaylistSummary{URL: rawURL, ID: metadataText(parent, "id"), Title: metadataText(parent, "title"), Channel: metadataText(parent, "channel"), Thumbnail: metadataText(parent, "thumbnail")}
	if summary.Channel == "" {
		summary.Channel = metadataText(parent, "uploader")
	}
	entries := result.Entries
	if len(entries) > MaxPlaylistEntries {
		entries = entries[:MaxPlaylistEntries]
	}
	for position, child := range entries {
		var info map[string]any
		if json.Unmarshal(child.InfoJSON, &info) != nil {
			continue
		}
		index := int(metadataInteger(info["playlist_index"]))
		if index <= 0 {
			index = position + 1
		}
		id, title, reportedURL := metadataText(info, "id"), metadataText(info, "title"), metadataText(info, "url")
		childURL, available := canonicalPlaylistChildURL(id, reportedURL)
		thumbnail := metadataText(info, "thumbnail")
		if thumbnail == "" && videoIDPattern.MatchString(id) {
			thumbnail = "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg"
		}
		entry := PlaylistEntrySummary{Index: index, VideoID: id, URL: childURL, Title: title, Thumbnail: thumbnail, Available: available}
		if summary.Thumbnail == "" && available {
			summary.Thumbnail = thumbnail
		}
		if duration := metadataInteger(info["duration"]); duration > 0 {
			entry.Duration = formatDuration(duration)
		}
		if entry.Title == "" {
			if available {
				entry.Title = "Untitled video"
			} else {
				entry.Title = "Unavailable video"
			}
		}
		summary.Entries = append(summary.Entries, entry)
		if available {
			summary.Available++
		} else {
			summary.Unavailable++
		}
	}
	summary.EntryCount = len(summary.Entries)
	if summary.ID == "" {
		return PlaylistSummary{}, errors.New("analyze playlist: missing playlist identity")
	}
	if summary.Title == "" {
		summary.Title = "Untitled playlist"
	}
	return summary, nil
}

var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func playlistIDFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Query().Get("list")
}

func canonicalPlaylistChildURL(videoID, reportedURL string) (string, bool) {
	if !videoIDPattern.MatchString(videoID) {
		return "", false
	}
	parsed, err := url.Parse(reportedURL)
	if err != nil || parsed.Scheme != "https" || parsed.Path != "/watch" || parsed.Query().Get("v") != videoID {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	if host != "youtube.com" {
		return "", false
	}
	return "https://www.youtube.com/watch?v=" + url.QueryEscape(videoID), true
}

// Analyze calls engine.Run with Simulate=true to extract metadata for
// the Home page preview. It uses NoPlaylist so watch?v=...&list= links
// still surface as a single video and caches private plans for single-video
// admission.
func (m *Manager) Analyze(ctx context.Context, rawURL string) (InfoSummary, error) {
	summary, privatePlans, err := m.AnalyzeForAdmission(ctx, rawURL)
	if err != nil {
		return InfoSummary{}, err
	}
	if summary.VideoID != "" && len(privatePlans) > 0 {
		m.cachePlans(summary.VideoID, privatePlans)
	}
	return summary, nil
}

// AnalyzeForAdmission returns private curated plans to trusted Go callers.
// It is not a Wails binding and its selectors must never cross the renderer
// boundary. Collection orchestration carries the selected plan directly into
// atomic admission instead of relying on the bounded single-video plan cache.
func (m *Manager) AnalyzeForAdmission(ctx context.Context, rawURL string) (InfoSummary, []outputplan.Plan, error) {
	if rawURL == "" {
		return InfoSummary{}, nil, errors.New("analyze: empty url")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return InfoSummary{}, nil, ErrClosed
	}
	ffmpegLocation := m.ffmpegLocation
	lifecycleCtx := m.lifecycleCtx
	runner := m.runAnalyze
	m.analysisWG.Add(1)
	m.mu.Unlock()
	analysisCtx, cancel := context.WithCancelCause(ctx)
	stopLifecycle := context.AfterFunc(lifecycleCtx, func() { cancel(context.Canceled) })
	defer func() {
		stopLifecycle()
		cancel(context.Canceled)
		m.analysisWG.Done()
	}()
	req := engine.Request{
		URL:      rawURL,
		Simulate: true,
		Playlist: engine.PlaylistOptions{Disabled: true},
		Filesystem: engine.FilesystemOptions{
			FfmpegLocation: ffmpegLocation,
		},
	}
	result, err := runner(analysisCtx, req)
	if err != nil {
		return InfoSummary{}, nil, err
	}
	summary, privatePlans, err := summarizeAnalysis(result.InfoJSON, rawURL)
	if err != nil {
		return InfoSummary{}, nil, err
	}
	return summary, privatePlans, nil
}

func summarizeAnalysis(raw json.RawMessage, rawURL string) (InfoSummary, []outputplan.Plan, error) {
	var info map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &info); err != nil {
			return InfoSummary{}, nil, fmt.Errorf("analyze: decode metadata: %w", err)
		}
	}
	summary := InfoSummary{URL: rawURL}
	if v, ok := info["id"].(string); ok {
		summary.VideoID = v
	}
	if v, ok := info["title"].(string); ok {
		summary.Title = v
	}
	if v, ok := info["channel"].(string); ok {
		summary.Channel = v
	} else if v, ok := info["uploader"].(string); ok {
		summary.Channel = v
	}
	if v, ok := info["duration"].(float64); ok {
		summary.DurationSeconds = int64(v)
		summary.Duration = formatDuration(summary.DurationSeconds)
	} else if v, ok := info["duration_string"].(string); ok {
		summary.Duration = v
	}
	if v, ok := info["thumbnail"].(string); ok {
		summary.Thumbnail = v
	}
	summary.ViewCount = metadataInteger(info["view_count"])
	if v, ok := info["upload_date"].(string); ok {
		summary.UploadDate = v
	}
	if v, ok := info["description"].(string); ok {
		summary.Description = v
	}
	if mediaType := strings.ToLower(strings.TrimSpace(metadataText(info, "media_type"))); mediaType != "" {
		summary.MediaType = mediaType
	}
	summary.Access = summarizeAccess(info)
	summary.Subtitles = summarizeSubtitleLanguages(info)
	plans := outputplan.Build(info, summary.DurationSeconds)
	summary.Plans = publicPlans(plans)
	return summary, plans, nil
}

// Analysis subtitle lists are bounded: YouTube reports well over a hundred
// auto-generated languages, far beyond anything a picker should render.
const (
	maxManualSubtitleLanguages    = 40
	maxAutomaticSubtitleLanguages = 60
)

// summarizeSubtitleLanguages extracts the language codes the engine reported
// in the info dict's "subtitles" and "automatic_captions" collections. Manual
// tracks come first so the picker can favour them; entries are sorted by code
// and every collection is capped.
func summarizeSubtitleLanguages(info map[string]any) []SubtitleLanguage {
	manual := subtitleLanguageCollection(info["subtitles"], false, maxManualSubtitleLanguages)
	automatic := subtitleLanguageCollection(info["automatic_captions"], true, maxAutomaticSubtitleLanguages)
	if len(manual) == 0 && len(automatic) == 0 {
		return nil
	}
	result := make([]SubtitleLanguage, 0, len(manual)+len(automatic))
	result = append(result, manual...)
	result = append(result, automatic...)
	return result
}

func subtitleLanguageCollection(raw any, auto bool, limit int) []SubtitleLanguage {
	collection, ok := raw.(map[string]any)
	if !ok || len(collection) == 0 {
		return nil
	}
	codes := make([]string, 0, len(collection))
	for code := range collection {
		if jobmodel.ValidSubtitleLanguage(code) {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	if len(codes) > limit {
		codes = codes[:limit]
	}
	result := make([]SubtitleLanguage, 0, len(codes))
	for _, code := range codes {
		language := SubtitleLanguage{Code: code, Auto: auto}
		if tracks, ok := collection[code].([]any); ok && len(tracks) > 0 {
			if track, ok := tracks[0].(map[string]any); ok {
				if name, ok := track["name"].(string); ok {
					language.Name = boundedText(name, 64)
				}
			}
		}
		result = append(result, language)
	}
	return result
}

func boundedText(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func metadataInteger(value any) int64 {
	switch value := value.(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	}
	return 0
}

func publicPlans(plans []outputplan.Plan) []outputplan.Plan {
	result := make([]outputplan.Plan, len(plans))
	copy(result, plans)
	for index := range result {
		result[index].Selector = ""
		result[index].SourceFormatIDs = nil
		result[index].Available = true
	}
	return result
}

func summarizeAccess(info map[string]any) AccessSummary {
	availability := strings.ToLower(strings.TrimSpace(metadataText(info, "availability")))
	switch availability {
	case "public":
		return AccessSummary{Code: "public", Label: "Publicly accessible"}
	case "unlisted":
		return AccessSummary{Code: "unlisted", Label: "Accessible with this link"}
	case "private":
		return AccessSummary{Code: "restricted", Label: "Restricted access"}
	case "needs_auth", "subscriber_only", "premium_only", "age_restricted", "login_required":
		return AccessSummary{Code: "restricted", Label: "Sign-in or access may be required"}
	case "", "unknown":
		return AccessSummary{Code: "unknown", Label: "Access status not reported"}
	default:
		return AccessSummary{Code: "unknown", Label: "Access status not reported"}
	}
}

func metadataText(info map[string]any, key string) string {
	value, _ := info[key].(string)
	return value
}

func (m *Manager) cachePlans(videoID string, plans []outputplan.Plan) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing || m.closed {
		return
	}
	if len(m.planCache) >= 32 {
		for key := range m.planCache {
			delete(m.planCache, key)
			break
		}
	}
	m.planCache[videoID] = cachedPlans{plans: plans, expiresAt: time.Now().Add(30 * time.Minute)}
}

// ResolvePlan resolves a UI-visible plan ID to the private engine selector
// created by the most recent analysis of that video.
func (m *Manager) ResolvePlan(videoID, planID string) (outputplan.Plan, error) {
	if videoID == "" || planID == "" {
		return outputplan.Plan{}, errors.New("jobs: video and output plan are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing || m.closed {
		return outputplan.Plan{}, ErrClosed
	}
	cached, ok := m.planCache[videoID]
	if !ok || time.Now().After(cached.expiresAt) {
		delete(m.planCache, videoID)
		return outputplan.Plan{}, errors.New("jobs: output options expired; analyze the video again")
	}
	for _, plan := range cached.plans {
		if plan.ID == planID {
			return plan, nil
		}
	}
	return outputplan.Plan{}, errors.New("jobs: output option is no longer available")
}

// formatDuration renders seconds as a human-readable label.
func formatDuration(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
