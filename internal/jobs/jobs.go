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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/tejasa97/vidstow/internal/outputplan"
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
)

// ErrClosed is returned when an operation would start new manager activity
// after Close has begun.
var ErrClosed = errors.New("jobs: manager is closed")

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

// Status is the lifecycle state of a job.
type Status string

const (
	StatusPending  Status = "pending"
	StatusActive   Status = "active"
	StatusPaused   Status = "paused"
	StatusComplete Status = "complete"
	StatusFailed   Status = "failed"
	StatusCanceled Status = "canceled"
)

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
}

// JobSnapshot is the immutable view of a job exposed to the UI.
type JobSnapshot struct {
	ID              string          `json:"id"`
	URL             string          `json:"url"`
	VideoID         string          `json:"videoID"`
	Title           string          `json:"title"`
	Channel         string          `json:"channel"`
	Quality         Quality         `json:"quality"`
	QualityLabel    string          `json:"qualityLabel"`
	PlanID          string          `json:"planId,omitempty"`
	OutputKind      outputplan.Kind `json:"outputKind,omitempty"`
	Container       string          `json:"container,omitempty"`
	VideoCodec      string          `json:"videoCodec,omitempty"`
	AudioCodec      string          `json:"audioCodec,omitempty"`
	ApproxBytes     int64           `json:"approxBytes,omitempty"`
	SizeApproximate bool            `json:"sizeApproximate,omitempty"`
	RequiresFFmpeg  bool            `json:"requiresFfmpeg,omitempty"`
	CanPause        bool            `json:"canPause,omitempty"`
	Processing      bool            `json:"processing,omitempty"`
	OutputDir       string          `json:"outputDir"`
	DurationLabel   string          `json:"durationLabel"`
	Thumbnail       string          `json:"thumbnail"`
	Status          Status          `json:"status"`
	CreatedAt       string          `json:"createdAt"`
	StartedAt       string          `json:"startedAt,omitempty"`
	CompletedAt     string          `json:"completedAt,omitempty"`
	Bytes           int64           `json:"bytes"`
	Total           int64           `json:"total"`
	Progress        float64         `json:"progress"`
	SpeedBps        float64         `json:"speedBps"`
	ETASeconds      float64         `json:"etaSeconds"`
	Filename        string          `json:"filename"`
	AbsolutePath    string          `json:"absolutePath"`
	Message         string          `json:"message"`
	ErrorReason     string          `json:"errorReason,omitempty"`
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
	Persistence *PersistenceStatus `json:"persistence,omitempty"`
}

// PersistenceStatus reports whether the durable queue can currently be
// saved. Message is intentionally generic: persistence errors can contain
// private filesystem paths, which must never cross the desktop boundary.
type PersistenceStatus struct {
	Available bool   `json:"available"`
	Healthy   bool   `json:"healthy"`
	Message   string `json:"message,omitempty"`
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
	client             *engine.Client
	listener           Listener
	eventSignal        chan struct{}
	eventMu            sync.Mutex
	pendingEvents      []Event
	eventStop          chan struct{}
	eventDone          chan struct{}
	eventClosed        bool
	closeOnce          sync.Once
	closeDone          chan struct{}
	closeErr           error
	closing            bool
	closed             bool
	lifecycleCtx       context.Context
	lifecycleCancel    context.CancelFunc
	analysisWG         sync.WaitGroup
	runDownload        downloadRunner
	runAnalyze         analyzeRunner
	ffmpegLocation     string
	mu                 sync.Mutex
	all                map[string]*jobState
	order              []string
	active             map[string]struct{}
	concurrency        int
	processing         chan struct{}
	planCache          map[string]cachedPlans
	persistence        Persistence
	persistenceDurable bool
	persistMu          sync.Mutex
	persistStatus      PersistenceStatus
	persistSignal      chan struct{}
	persistStop        chan struct{}
	persistDone        chan struct{}
}

type cachedPlans struct {
	plans     []outputplan.Plan
	expiresAt time.Time
}

type downloadRunner func(context.Context, engine.Request, engine.EventHandler) (engine.Result, error)

type analyzeRunner func(context.Context, engine.Request) (engine.Result, error)

type jobState struct {
	snap           JobSnapshot
	plan           *outputplan.Plan
	cancel         context.CancelFunc
	ctx            context.Context
	done           chan struct{}
	startBps       time.Time
	startByt       int64
	pauseRequested bool
}

// New creates a Manager. listener may be nil for headless tests.
func New(client *engine.Client, listener Listener) *Manager {
	if client == nil {
		client = newFocusedClient()
	}
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	manager := &Manager{
		client:          client,
		listener:        listener,
		closeDone:       make(chan struct{}),
		eventDone:       make(chan struct{}),
		lifecycleCtx:    lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
		runDownload:     defaultDownloadRunner,
		runAnalyze:      client.Run,
		all:             make(map[string]*jobState),
		active:          make(map[string]struct{}),
		concurrency:     DefaultDownloadConcurrency,
		processing:      make(chan struct{}, MaxProcessingConcurrency),
		planCache:       make(map[string]cachedPlans),
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

func defaultDownloadRunner(ctx context.Context, req engine.Request, handler engine.EventHandler) (engine.Result, error) {
	client := newFocusedClient(engine.WithEventHandler(handler))
	defer client.Close()
	return client.Run(ctx, req)
}

// newFocusedClient is the single Desktop composition factory. Analysis keeps
// one instance and each download creates one with only its event handler
// differing, so both paths receive the complete YouTube provider family.
func newFocusedClient(options ...engine.Option) *engine.Client {
	return engine.NewClient(provideryoutube.NewComposition(), options...)
}

// Close stops new manager activity, flushes persistence, drains every event
// accepted before dispatcher shutdown, and releases the analysis client.
// It is idempotent; concurrent callers receive the same final error.
func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.closeErr = m.close()
		close(m.closeDone)
	})
	<-m.closeDone
	return m.closeErr
}

func (m *Manager) close() error {
	m.mu.Lock()
	m.closing = true
	if m.lifecycleCancel != nil {
		m.lifecycleCancel()
	}
	workerDone := make([]<-chan struct{}, 0, len(m.active))
	for id := range m.active {
		state := m.all[id]
		if state == nil {
			continue
		}
		state.pauseRequested = true
		state.snap.CanPause = false
		if state.cancel != nil {
			state.cancel()
		}
		if state.done != nil {
			workerDone = append(workerDone, state.done)
		}
	}
	m.mu.Unlock()

	// Existing workers must publish their final state before persistence and
	// the event dispatcher are shut down. No worker can start another job while
	// closing is true, so this is a complete join of manager-owned work.
	for _, finished := range workerDone {
		<-finished
	}
	m.analysisWG.Wait()

	m.mu.Lock()
	stop, persistDone := m.persistStop, m.persistDone
	m.persistStop = nil
	m.persistSignal = nil
	m.mu.Unlock()

	if stop != nil {
		close(stop)
		<-persistDone
	}
	// Stop the debounce loop before the final write so its result is the one
	// returned to shutdown and no later background flush can obscure it.
	flushErr := m.FlushPersistence()
	m.stopDispatcher()
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	if m.client != nil {
		m.client.Close()
	}
	return flushErr
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

func (m *Manager) stopDispatcher() {
	if m.listener == nil {
		return
	}
	m.eventMu.Lock()
	m.eventClosed = true
	m.eventMu.Unlock()
	close(m.eventStop)
	<-m.eventDone
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
	if req.URL == "" {
		return "", errors.New("jobs: empty url")
	}
	var selectedPlan *outputplan.Plan
	if req.PlanID != "" {
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
	if err := ensureDir(req.OutputDir); err != nil {
		return "", fmt.Errorf("jobs: prepare output dir: %w", err)
	}

	id := uuid.NewString()
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
		},
		plan: selectedPlan,
		done: make(chan struct{}),
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

	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return "", ErrClosed
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

// Cancel stops the job if it is running and removes it from the queue.
// It marks terminal jobs (complete/failed/canceled) as canceled so the
// caller can rely on the state to flow.
func (m *Manager) Cancel(id string) {
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return
	}
	state, ok := m.all[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	switch state.snap.Status {
	case StatusComplete, StatusFailed, StatusCanceled:
		m.mu.Unlock()
		return
	}
	if _, active := m.active[id]; active {
		state.pauseRequested = false
		if state.cancel != nil {
			state.cancel()
		}
		state.snap.Message = "Canceling"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
		m.emitQueueLocked()
	} else {
		if state.cancel != nil {
			state.cancel()
		}
		state.snap.Status = StatusCanceled
		state.snap.Message = "Canceled"
		state.snap.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		m.all[id] = state
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
		// remove from pending ordering so the row disappears from the
		// queue pane immediately.
		m.removeFromOrderLocked(id)
		m.maybeStartNextLocked()
		m.emitQueueLocked()
	}
	m.mu.Unlock()
}

// Pause suspends a pending or actively downloading job. Active processing
// stages cannot be paused safely; their downloaded inputs are retained and
// the control becomes available again only after processing completes.
func (m *Manager) Pause(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing || m.closed {
		return ErrClosed
	}
	state, ok := m.all[id]
	if !ok {
		return fmt.Errorf("jobs: unknown job %q", id)
	}
	switch state.snap.Status {
	case StatusPending:
		state.snap.Status = StatusPaused
		state.snap.Message = "Paused"
		state.snap.CanPause = false
		m.removeFromOrderLocked(id)
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
		m.emitQueueLocked()
		return nil
	case StatusActive:
		if state.snap.Processing {
			return errors.New("jobs: pause is unavailable while media is being finalized")
		}
		state.pauseRequested = true
		state.snap.CanPause = false
		state.snap.Message = "Pausing"
		if state.cancel != nil {
			state.cancel()
		}
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
		return nil
	case StatusPaused:
		return nil
	default:
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
	ids := make([]string, 0, len(m.all))
	for id, state := range m.all {
		if state.snap.Status == StatusPending || (state.snap.Status == StatusActive && !state.snap.Processing) {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	paused := 0
	for _, id := range ids {
		if m.Pause(id) == nil {
			paused++
		}
	}
	return paused
}

// Resume returns a paused job to the FIFO. The engine's default partial-file
// behavior resumes byte-range downloads instead of discarding existing data.
func (m *Manager) Resume(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing || m.closed {
		return ErrClosed
	}
	state, ok := m.all[id]
	if !ok {
		return fmt.Errorf("jobs: unknown job %q", id)
	}
	if state.snap.Status != StatusPaused {
		return errors.New("jobs: only paused jobs can be resumed")
	}
	state.snap.Status = StatusPending
	state.snap.Message = "Queued"
	state.snap.Processing = false
	state.snap.CanPause = false
	state.pauseRequested = false
	state.ctx = nil
	state.cancel = nil
	state.done = make(chan struct{})
	m.order = append(m.order, id)
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	m.maybeStartNextLocked()
	m.emitQueueLocked()
	return nil
}

// Shutdown converts running work to paused work, waits briefly for workers to
// leave their critical sections, and durably records the remaining queue.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return
	}
	done := make([]<-chan struct{}, 0, len(m.active))
	for id := range m.active {
		state := m.all[id]
		if state == nil {
			continue
		}
		state.pauseRequested = true
		state.snap.CanPause = false
		state.snap.Message = "Pausing"
		if state.cancel != nil {
			state.cancel()
		}
		done = append(done, state.done)
	}
	m.mu.Unlock()
	for _, finished := range done {
		select {
		case <-finished:
		case <-ctx.Done():
			m.FlushPersistence()
			return
		}
	}
	m.FlushPersistence()
}

// Retry re-queues a failed or canceled job. It rebuilds the request
// fields from the stored snapshot so the caller only needs the id.
func (m *Manager) Retry(id string) error {
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
	if state.snap.Status != StatusFailed && state.snap.Status != StatusCanceled {
		m.mu.Unlock()
		return fmt.Errorf("jobs: only failed or canceled jobs can be retried")
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
	state.done = make(chan struct{})
	m.order = append(m.order, id)
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	m.maybeStartNextLocked()
	m.emitQueueLocked()
	m.mu.Unlock()
	return nil
}

// Remove drops a terminal job from the manager entirely.
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return
	}
	state, ok := m.all[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	switch state.snap.Status {
	case StatusComplete, StatusFailed, StatusCanceled:
	default:
		m.mu.Unlock()
		return
	}
	delete(m.all, id)
	m.removeFromOrderLocked(id)
	m.emitQueueLocked()
	m.mu.Unlock()
}

// ClearTerminal removes every job in a terminal state.
func (m *Manager) ClearTerminal() {
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return
	}
	for id, state := range m.all {
		switch state.snap.Status {
		case StatusComplete, StatusFailed, StatusCanceled:
			delete(m.all, id)
			m.removeFromOrderLocked(id)
		}
	}
	m.emitQueueLocked()
	m.mu.Unlock()
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
		if state.snap.Status != StatusPending {
			m.order = m.order[1:]
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		state.ctx = ctx
		state.cancel = cancel
		state.snap.Status = StatusActive
		state.snap.StartedAt = time.Now().UTC().Format(time.RFC3339)
		state.snap.Message = "Preparing"
		state.snap.CanPause = true
		m.active[id] = struct{}{}
		m.order = m.order[1:]
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
		go m.run(state)
	}
}

func (m *Manager) run(state *jobState) {
	defer close(state.done)

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
		req.OutputTemplate = fmt.Sprintf("%%(title)s [%%(id)s] [%s].%%(ext)s", state.plan.Label)
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
	ctx := state.ctx
	runner := m.runDownload
	m.mu.Unlock()

	processingHeld := false
	handler := func(ctx context.Context, ev engine.Event) error {
		if ev.Kind == engine.EventPostprocessStarting && !processingHeld {
			select {
			case m.processing <- struct{}{}:
				processingHeld = true
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		m.handleEvent(state, ev)
		if ev.Kind == engine.EventPostprocessCompleted && processingHeld {
			<-m.processing
			processingHeld = false
		}
		return nil
	}

	result, err := runner(ctx, req, handler)
	if processingHeld {
		<-m.processing
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if state.ctx.Err() != nil {
		if state.pauseRequested {
			state.snap.Status = StatusPaused
			state.snap.Message = "Paused"
			state.snap.ErrorReason = ""
			state.snap.CompletedAt = ""
		} else {
			state.snap.Status = StatusCanceled
			state.snap.Message = "Canceled"
			state.snap.ErrorReason = "canceled"
		}
	} else if err != nil {
		state.snap.Status = StatusFailed
		state.snap.Message = humanError(err)
		state.snap.ErrorReason = errorReason(err)
		if state.snap.Bytes == 0 {
			state.snap.Progress = 0
		} else if state.snap.Total > 0 {
			state.snap.Progress = clampFloat(float64(state.snap.Bytes) / float64(state.snap.Total))
		} else {
			state.snap.Progress = 0
		}
	} else {
		state.snap.Status = StatusComplete
		state.snap.Message = "Completed"
		state.snap.Progress = 1
		if result.Filename != "" {
			state.snap.Filename = filepath.Base(result.Filename)
			if abs, absErr := filepath.Abs(result.Filename); absErr == nil {
				state.snap.AbsolutePath = abs
			}
			if state.snap.Bytes == 0 {
				state.snap.Bytes = result.Bytes
			}
		}
		if state.snap.Bytes == 0 {
			state.snap.Bytes = result.Bytes
		}
		if state.snap.Title == "" {
			state.snap.Title = state.snap.Filename
		}
	}
	state.snap.CanPause = false
	state.snap.Processing = false
	if state.snap.Status != StatusPaused {
		state.snap.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	}
	delete(m.active, state.snap.ID)
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	m.maybeStartNextLocked()
	m.emitQueueLocked()
}

func (m *Manager) handleEvent(state *jobState, ev engine.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state.ctx != nil && state.ctx.Err() != nil && state.snap.Status == StatusActive {
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
	m.emitLocked(Event{Name: EventQueue, Queue: m.snapshotLocked()})
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
	case StatusPaused:
		return 1
	case StatusPending:
		return 2
	case StatusFailed:
		return 3
	case StatusCanceled:
		return 4
	case StatusComplete:
		return 5
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

// InfoSummary is the metadata displayed on the Home page after analyse.
type InfoSummary struct {
	Title           string            `json:"title"`
	Channel         string            `json:"channel"`
	Duration        string            `json:"duration"`
	DurationSeconds int64             `json:"durationSeconds"`
	Thumbnail       string            `json:"thumbnail"`
	VideoID         string            `json:"videoId"`
	URL             string            `json:"url"`
	ViewCount       int64             `json:"viewCount"`
	UploadDate      string            `json:"uploadDate"`
	Description     string            `json:"description"`
	Access          AccessSummary     `json:"access"`
	Plans           []outputplan.Plan `json:"plans"`
}

// AccessSummary is informational extraction metadata, not a product gate.
// Selectable outputs remain exactly the curated plans returned by Analyze.
type AccessSummary struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// Analyze calls engine.Run with Simulate=true to extract metadata for
// the Home page preview. It uses NoPlaylist so watch?v=...&list= links
// still surface as a single video.
func (m *Manager) Analyze(ctx context.Context, rawURL string) (InfoSummary, error) {
	if rawURL == "" {
		return InfoSummary{}, errors.New("analyze: empty url")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.closing || m.closed {
		m.mu.Unlock()
		return InfoSummary{}, ErrClosed
	}
	ffmpegLocation := m.ffmpegLocation
	lifecycleCtx := m.lifecycleCtx
	runner := m.runAnalyze
	m.analysisWG.Add(1)
	m.mu.Unlock()
	analysisCtx, cancel := context.WithCancel(ctx)
	stopLifecycle := context.AfterFunc(lifecycleCtx, cancel)
	defer func() {
		stopLifecycle()
		cancel()
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
		return InfoSummary{}, err
	}
	summary, privatePlans, err := summarizeAnalysis(result.InfoJSON, rawURL)
	if err != nil {
		return InfoSummary{}, err
	}
	if summary.VideoID != "" && len(privatePlans) > 0 {
		m.cachePlans(summary.VideoID, privatePlans)
	}
	return summary, nil
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
	summary.Access = summarizeAccess(info)
	plans := outputplan.Build(info, summary.DurationSeconds)
	summary.Plans = publicPlans(plans)
	return summary, plans, nil
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
