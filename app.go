package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/tejasa97/vidstow/internal/admission"
	"github.com/tejasa97/vidstow/internal/ffmpegdetect"
	"github.com/tejasa97/vidstow/internal/jobmodel"
	"github.com/tejasa97/vidstow/internal/jobs"
	"github.com/tejasa97/vidstow/internal/recovery"
	"github.com/tejasa97/vidstow/internal/reservationfs"
	"github.com/tejasa97/vidstow/internal/store"
	"github.com/tejasa97/vidstow/internal/urlcheck"
	"github.com/tejasa97/youtube_dlp/engine"
	"github.com/tejasa97/youtube_dlp/engine/value"
)

var errRecoveryRequired = errors.New("vidstow: recovery required")

type shutdownLifecycle interface {
	Shutdown(context.Context) error
	Close(...context.Context) error
}

var (
	defaultStatePath         = store.DefaultPath
	openStateV2              = store.OpenV2
	prepareStartupStateRoots = prepareStartupRoots
	reconcileStartupState    = func(ctx context.Context, state *store.V2Store) (jobmodel.State, error) {
		return recovery.Reconcile(ctx, state, recovery.Options{})
	}
	restoreStartupManager = func(manager *jobs.Manager, snapshot jobmodel.State) error { return manager.RestoreStateV2(snapshot) }
	startStartupCleanup   = recovery.StartCleanupWorker
	logAppErrorf          = wailsruntime.LogErrorf
	emitAppEvent          = wailsruntime.EventsEmit
)

// App is the Wails-bound root. Every exported method is reachable from
// the frontend via the generated bindings in wailsjs/go/main/App.js.
type App struct {
	ctx             context.Context
	store           *store.V2Store
	jobs            *jobs.Manager
	coordinator     *admission.Coordinator
	statePath       string
	startupStatus   store.StartupStatus
	cleanupCancel   context.CancelFunc
	cleanupDone     <-chan struct{}
	shutdownManager shutdownLifecycle
	closeState      func() error
	mu              sync.Mutex
	lastFFmpeg      ffmpegdetect.Status
	quitMu          sync.Mutex
	quitPermit      bool
	quitRequestOpen bool
	quitDeadline    time.Time
}

// NewApp constructs the App. The Wails bind() call wires every public
// method to the JS side.
func NewApp() *App {
	return &App{startupStatus: store.StartupStatus{Mode: store.StartupRecoveryRequired, Reason: store.RecoveryCorruptState}}
}

// startup is called once by Wails after the window is ready.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	statePath, err := defaultStatePath()
	if err != nil {
		a.setStartupStatus(store.StartupStatus{Mode: store.StartupRecoveryRequired, Reason: store.RecoveryUnsafePermissions})
		logAppErrorf(ctx, "desktop: store path: %v", err)
		return
	}
	a.startupAt(ctx, statePath)
}

func (a *App) startupAt(ctx context.Context, statePath string) {
	a.ctx = ctx
	a.statePath = statePath
	st, status, openErr := openStateV2(statePath)
	if openErr != nil || !status.Healthy() || status.Warning != "" || st == nil {
		if openErr != nil {
			logAppErrorf(ctx, "desktop: open State v2: %v", openErr)
		}
		if st != nil {
			_ = st.Close()
		}
		if status.Warning != "" {
			status = store.StartupStatus{Mode: store.StartupRecoveryRequired, Reason: store.RecoveryIndeterminate}
		}
		a.setStartupStatus(status)
		return
	}
	a.store = st
	a.setStartupStatus(status)

	if err := prepareStartupStateRoots(st.Snapshot()); err != nil {
		logAppErrorf(ctx, "desktop: validate output roots: %v", err)
		_ = st.Close()
		a.store = nil
		a.setStartupStatus(store.StartupStatus{Mode: store.StartupRecoveryRequired, Reason: store.RecoveryUnsafePermissions})
		return
	}
	committed, err := reconcileStartupState(ctx, st)
	if err != nil {
		logAppErrorf(ctx, "desktop: reconcile startup state: %v", err)
		_ = st.Close()
		a.store = nil
		a.setStartupStatus(store.StartupStatus{Mode: store.StartupRecoveryRequired, Reason: store.RecoveryIndeterminate})
		return
	}

	a.jobs = jobs.New(nil, func(ev jobs.Event) {
		// Events are dispatched on a background goroutine by the jobs
		// package; Wails runtime is safe to call from any goroutine.
		emitAppEvent(a.ctx, ev.Name, ev)
		// Completion/history were committed together by the manager. This is a
		// read-only refresh event, not a second history writer.
		if ev.Name == jobs.EventJobUpdate && isTerminal(ev.Job.Status) {
			emitAppEvent(a.ctx, "history:update", st.History())
		}
	})
	if err := a.jobs.SetStateStore(st); err != nil {
		logAppErrorf(ctx, "desktop: configure State v2 manager: %v", err)
		_ = st.Close()
		a.jobs = nil
		a.store = nil
		a.setStartupStatus(store.StartupStatus{Mode: store.StartupRecoveryRequired, Reason: store.RecoveryIndeterminate})
		return
	}
	if err := restoreStartupManager(a.jobs, committed); err != nil {
		logAppErrorf(ctx, "desktop: restore State v2 manager: %v", err)
		_ = st.Close()
		a.jobs = nil
		a.store = nil
		a.setStartupStatus(store.StartupStatus{Mode: store.StartupRecoveryRequired, Reason: store.RecoveryIndeterminate})
		return
	}
	a.coordinator, err = admission.NewCoordinator(admission.Dependencies{
		Store: st, Resolver: a.jobs, Queue: a.jobs,
	})
	if err != nil {
		logAppErrorf(ctx, "desktop: configure State v2 admission: %v", err)
		_ = st.Close()
		a.jobs = nil
		a.store = nil
		a.setStartupStatus(store.StartupStatus{Mode: store.StartupRecoveryRequired, Reason: store.RecoveryIndeterminate})
		return
	}

	settings := a.store.Settings()
	a.jobs.SetConcurrency(settings.DownloadConcurrency)
	a.jobs.SetFFmpegLocation(settings.FFmpegPath)
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	a.cleanupCancel = cleanupCancel
	a.cleanupDone = startStartupCleanup(cleanupCtx, st, recovery.DefaultCleanupInterval)
	a.setFFmpegStatus(ffmpegdetect.Probe(ctx, settings.FFmpegPath))
}

// shutdown is called by Wails after the native close gate has permitted the
// window to exit. One deadline is shared by cleanup, workers, and manager
// close; a stuck process cannot turn quit into an unbounded join.
func (a *App) shutdown(ctx context.Context) {
	shutdownCtx, cancel := a.shutdownContext(ctx)
	defer cancel()
	a.stopCleanup(shutdownCtx)
	managerClosed := true
	var manager shutdownLifecycle
	if a.jobs != nil {
		manager = a.jobs
	}
	if a.shutdownManager != nil {
		manager = a.shutdownManager
	}
	if manager != nil {
		if err := manager.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			logAppErrorf(ctx, "desktop: pause queue during shutdown: %v", err)
		}
		if err := manager.Close(shutdownCtx); err != nil {
			managerClosed = false
			logAppErrorf(ctx, "desktop: close queue during shutdown: %v", err)
		}
	}
	if managerClosed {
		if err := a.closeStateV2(); err != nil {
			logAppErrorf(ctx, "desktop: close State v2: %v", err)
		}
	}
}

func (a *App) closeStateV2() error {
	if a.closeState != nil {
		return a.closeState()
	}
	if a.store == nil {
		return nil
	}
	return a.store.Close()
}

// ---------------------------------------------------------------------------
// Bound methods
// ---------------------------------------------------------------------------

// GetSettings returns the persisted settings.
func (a *App) GetSettings() store.Settings {
	if a.store == nil {
		return store.Settings{}
	}
	return a.store.Settings()
}

// UpdateSettings persists new settings and re-probes ffmpeg so the UI
// stays accurate.
func (a *App) UpdateSettings(next store.Settings) (store.Settings, error) {
	if err := a.requireReady(); err != nil {
		return store.Settings{}, err
	}
	if strings.TrimSpace(next.DownloadFolder) == "" {
		return store.Settings{}, errors.New("download folder is required")
	}
	expanded, err := expandHome(next.DownloadFolder)
	if err != nil {
		return store.Settings{}, err
	}
	root, err := reservationfs.EnsureOpenRoot(expanded)
	if err != nil {
		return store.Settings{}, fmt.Errorf("could not validate download folder: %w", err)
	}
	rootVolume := root.Facts().Volume
	if _, err := engine.ValidateOutputRoot(rootVolume.CanonicalPath); err != nil {
		_ = root.Close()
		return store.Settings{}, fmt.Errorf("could not validate engine output folder: %w", err)
	}
	if err := root.Close(); err != nil {
		return store.Settings{}, fmt.Errorf("could not close download folder: %w", err)
	}
	next.DownloadFolder = expanded
	if next.DownloadConcurrency < 1 || next.DownloadConcurrency > jobs.MaxDownloadConcurrency {
		return store.Settings{}, fmt.Errorf("download concurrency must be between 1 and %d", jobs.MaxDownloadConcurrency)
	}
	if err := a.store.SetSettings(next); err != nil {
		return store.Settings{}, err
	}
	a.jobs.SetFFmpegLocation(next.FFmpegPath)
	a.jobs.SetConcurrency(next.DownloadConcurrency)
	a.setFFmpegStatus(ffmpegdetect.Probe(a.ctx, next.FFmpegPath))
	wailsruntime.EventsEmit(a.ctx, "settings:update", a.store.Settings())
	return a.store.Settings(), nil
}

// PickDownloadFolder opens a native folder picker and returns the
// chosen path (empty string if the user cancelled).
func (a *App) PickDownloadFolder() (string, error) {
	if err := a.requireReady(); err != nil {
		return "", err
	}
	return wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title:                "Choose download folder",
		DefaultDirectory:     a.store.Settings().DownloadFolder,
		CanCreateDirectories: true,
	})
}

// PickFFmpegPath opens a file picker for a binary and returns the
// selected path. The caller validates the path with ConfigureFFmpeg.
func (a *App) PickFFmpegPath() (string, error) {
	if err := a.requireReady(); err != nil {
		return "", err
	}
	pattern := "ffmpeg"
	if runtime.GOOS == "windows" {
		pattern = "ffmpeg.exe"
	}
	return wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "Choose ffmpeg binary",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "ffmpeg binary", Pattern: pattern},
			{DisplayName: "All files", Pattern: "*.*"},
		},
	})
}

// GetFFmpegStatus returns the current ffmpeg probe result.
func (a *App) GetFFmpegStatus() ffmpegdetect.Status { return a.ffmpegStatus() }

// GetBuildInfo returns the release identity used by About and diagnostics.
func (a *App) GetBuildInfo() BuildInfo { return currentBuildInfo() }

// GetPersistenceStatus returns durable queue health for startup UI state.
func (a *App) GetPersistenceStatus() jobs.PersistenceStatus {
	if a.jobs == nil {
		return jobs.PersistenceStatus{Available: false, Healthy: false, Message: "Download state requires recovery."}
	}
	return a.jobs.PersistenceStatus()
}

// GetStartupStatus is the first app contract the frontend should read. A
// recovery-required result is authoritative and never accompanied by an
// ephemeral queue manager.
func (a *App) GetStartupStatus() store.StartupStatus { return a.startupStatusSnapshot() }

// ProbeFFmpeg re-runs detection and broadcasts the result.
func (a *App) ProbeFFmpeg() ffmpegdetect.Status {
	if a.store == nil {
		return a.ffmpegStatus()
	}
	status := ffmpegdetect.Probe(a.ctx, a.store.Settings().FFmpegPath)
	a.setFFmpegStatus(status)
	wailsruntime.EventsEmit(a.ctx, "ffmpeg:update", status)
	return status
}

// ConfigureFFmpeg validates a path, persists it, and re-probes.
func (a *App) ConfigureFFmpeg(path string) (ffmpegdetect.Status, error) {
	if err := a.requireReady(); err != nil {
		return ffmpegdetect.Status{}, err
	}
	status := ffmpegdetect.ConfigurePath(a.ctx, path)
	if !status.Available {
		a.setFFmpegStatus(status)
		wailsruntime.EventsEmit(a.ctx, "ffmpeg:update", status)
		return status, errors.New(status.Message)
	}
	settings := a.store.Settings()
	settings.FFmpegPath = status.Path
	if err := a.store.SetSettings(settings); err != nil {
		return status, err
	}
	a.jobs.SetFFmpegLocation(status.Path)
	a.setFFmpegStatus(status)
	wailsruntime.EventsEmit(a.ctx, "ffmpeg:update", status)
	wailsruntime.EventsEmit(a.ctx, "settings:update", a.store.Settings())
	return status, nil
}

// ClearFFmpegPath removes the configured path so the app falls back to
// PATH discovery.
func (a *App) ClearFFmpegPath() ffmpegdetect.Status {
	if a.store == nil || a.jobs == nil {
		return a.ffmpegStatus()
	}
	settings := a.store.Settings()
	settings.FFmpegPath = ""
	_ = a.store.SetSettings(settings)
	a.jobs.SetFFmpegLocation("")
	status := ffmpegdetect.Probe(a.ctx, "")
	a.setFFmpegStatus(status)
	wailsruntime.EventsEmit(a.ctx, "ffmpeg:update", status)
	wailsruntime.EventsEmit(a.ctx, "settings:update", a.store.Settings())
	return status
}

// ---------------------------------------------------------------------------
// URL validation & analyse
// ---------------------------------------------------------------------------

// ValidateURL returns either an accepted single-video Result or an
// error whose Reason() explains why it was rejected.
func (a *App) ValidateURL(raw string) (urlcheck.Result, error) {
	return urlcheck.Validate(raw)
}

// AnalyzeURL fetches metadata for one single YouTube video.
func (a *App) AnalyzeURL(raw string) (jobs.InfoSummary, error) {
	if err := a.requireReady(); err != nil {
		return jobs.InfoSummary{}, err
	}
	res, err := urlcheck.Validate(raw)
	if err != nil {
		return jobs.InfoSummary{}, err
	}
	if res.Kind != urlcheck.KindSingleVideo {
		return jobs.InfoSummary{}, errors.New("choose whether to analyze the video or playlist")
	}
	// EJS preprocessing has a 55-second execution budget. Leave additional
	// room for the watch page and player-script requests around that phase.
	ctx, cancel := context.WithTimeout(a.ctx, 75*time.Second)
	defer cancel()
	summary, err := a.jobs.Analyze(ctx, res.URL)
	if err != nil {
		wailsruntime.LogErrorf(a.ctx, "desktop: analyze video: %v", err)
		return jobs.InfoSummary{}, errors.New(friendlyAnalyzeError(err))
	}
	if summary.Title == "" {
		summary.Title = "Untitled video"
	}
	summary.URL = res.URL
	summary.VideoID = res.VideoID
	return summary, nil
}

// AnalyzePlaylist fetches a bounded flat preview without resolving every
// child's formats. Playlist format selection happens per child at admission.
func (a *App) AnalyzePlaylist(raw string) (jobs.PlaylistSummary, error) {
	if err := a.requireReady(); err != nil {
		return jobs.PlaylistSummary{}, err
	}
	res, err := urlcheck.Validate(raw)
	if err != nil {
		return jobs.PlaylistSummary{}, err
	}
	if res.Kind != urlcheck.KindPlaylist {
		return jobs.PlaylistSummary{}, errors.New("choose the playlist from this link first")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 75*time.Second)
	defer cancel()
	summary, err := a.jobs.AnalyzePlaylist(ctx, res.PlaylistURL)
	if err != nil {
		wailsruntime.LogErrorf(a.ctx, "desktop: analyze playlist: %v", err)
		return jobs.PlaylistSummary{}, errors.New(friendlyAnalyzeError(err))
	}
	summary.URL = res.PlaylistURL
	return summary, nil
}

// ---------------------------------------------------------------------------
// Queue
// ---------------------------------------------------------------------------

// StartDownload enqueues a download and starts the FIFO worker.
func (a *App) StartDownload(req jobs.Request) (string, error) {
	if err := a.requireReady(); err != nil {
		return "", err
	}
	if req.URL == "" {
		return "", errors.New("empty url")
	}
	res, err := urlcheck.Validate(req.URL)
	if err != nil {
		return "", err
	}
	if res.Kind != urlcheck.KindSingleVideo {
		return "", errors.New("select video-only before starting this download")
	}
	req.URL = res.VideoURL
	if req.VideoID == "" {
		req.VideoID = res.VideoID
	}
	settings := a.store.Settings()
	if req.OutputDir == "" {
		req.OutputDir = settings.DownloadFolder
	}
	if settings.PerVideoSubfolder {
		req.OutputDir = filepath.Join(req.OutputDir, videoSubfolder(req.Title, req.VideoID))
	}
	if req.Quality == "" {
		req.Quality = jobs.QualityBest
	}
	if req.PlanID == "" {
		return "", errors.New("an analyzed output plan is required before starting a download")
	}
	plan, resolveErr := a.jobs.ResolvePlan(req.VideoID, req.PlanID)
	if resolveErr != nil {
		return "", resolveErr
	}
	if plan.RequiresFFmpeg && !a.ffmpegStatus().Available {
		return "", errors.New("this output needs FFmpeg; install FFmpeg or choose an original audio format")
	}
	req.OutputDir, err = canonicalOutputRequestPath(req.OutputDir)
	if err != nil {
		return "", err
	}
	root, err := reservationfs.EnsureOpenRoot(req.OutputDir)
	if err != nil {
		return "", fmt.Errorf("could not create output folder: %w", err)
	}
	defer root.Close()
	metadata := value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String(req.Title)},
		value.Field{Key: "id", Value: value.String(req.VideoID)},
		value.Field{Key: "channel", Value: value.String(req.Channel)},
	))
	result, err := a.coordinator.Admit(a.ctx, root, admission.Request{Queue: req, Metadata: metadata})
	if err != nil {
		return "", err
	}
	return result.Job.ID, nil
}

// ListJobs returns the current queue + history snapshot.
func (a *App) ListJobs() []jobs.JobSnapshot {
	if a.jobs == nil {
		return nil
	}
	return a.jobs.List()
}

// GetQueueView is the authoritative V4 frontend contract. The legacy
// ListJobs binding remains for non-queue compatibility surfaces only.
func (a *App) GetQueueView() jobs.QueueView {
	if a.jobs == nil {
		return jobs.QueueView{Persistence: jobs.PersistenceStatus{Available: false, Healthy: false, Message: "Download state requires recovery."}}
	}
	return a.jobs.QueueView()
}

func (a *App) PauseQueueJob(id, token string) error {
	if err := a.requireReady(); err != nil {
		return err
	}
	return a.jobs.QueuePause(id, token)
}

func (a *App) CancelQueueJob(id, token string) error {
	if err := a.requireReady(); err != nil {
		return err
	}
	return a.jobs.QueueCancel(id, token)
}

func (a *App) ResumeQueueJob(id, token string) error {
	if err := a.requireReady(); err != nil {
		return err
	}
	return a.jobs.QueueResume(id, token)
}

func (a *App) RetryQueueJob(id, token string) error {
	if err := a.requireReady(); err != nil {
		return err
	}
	return a.jobs.QueueRetry(id, token)
}

func (a *App) RemoveQueueJob(id, token string) error {
	if err := a.requireReady(); err != nil {
		return err
	}
	return a.jobs.QueueRemove(id, token)
}

func (a *App) OpenQueueJob(id, token string) error {
	if err := a.requireReady(); err != nil {
		return err
	}
	path, err := a.jobs.QueueOpenPath(id, token)
	if err != nil {
		return err
	}
	return a.OpenFile(path)
}

func (a *App) PauseAllQueueJobs(token string) (int, error) {
	if err := a.requireReady(); err != nil {
		return 0, err
	}
	return a.jobs.QueuePauseAll(token)
}

func (a *App) ClearCompletedQueueJobs(token string) error {
	if err := a.requireReady(); err != nil {
		return err
	}
	return a.jobs.QueueClearCompleted(token)
}

// CancelJob cancels an active or pending job.
func (a *App) CancelJob(id string) {
	if a.jobs != nil {
		a.jobs.Cancel(id)
	}
}

// PauseJob preserves partial bytes and suspends a pending or active download.
func (a *App) PauseJob(id string) error {
	if err := a.requireReady(); err != nil {
		return err
	}
	return a.jobs.Pause(id)
}

// PauseAllJobs pauses every job currently safe to suspend.
func (a *App) PauseAllJobs() int {
	if a.jobs == nil {
		return 0
	}
	return a.jobs.PauseAll()
}

// ResumeJob returns a paused download to the queue.
func (a *App) ResumeJob(id string) error {
	if err := a.requireReady(); err != nil {
		return err
	}
	return a.jobs.Resume(id)
}

// RetryJob re-queues a failed or canceled job.
func (a *App) RetryJob(id string) error {
	if err := a.requireReady(); err != nil {
		return err
	}
	return a.jobs.Retry(id)
}

// RemoveJob drops a terminal job from the manager.
func (a *App) RemoveJob(id string) {
	if a.jobs != nil {
		a.jobs.Remove(id)
	}
}

// ClearCompletedJobs removes every terminal job from the in-memory queue.
func (a *App) ClearCompletedJobs() {
	if a.jobs != nil {
		a.jobs.ClearTerminal()
	}
}

// ---------------------------------------------------------------------------
// Downloads (history)
// ---------------------------------------------------------------------------

// ListDownloads returns the persisted history with live file-presence status.
func (a *App) ListDownloads() []store.HistoryEntry {
	if a.store == nil {
		return nil
	}
	return a.store.History()
}

// RemoveDownload deletes one history entry without touching the media file.
func (a *App) RemoveDownload(id string) error {
	if err := a.requireReady(); err != nil {
		return err
	}
	removed, err := a.store.RemoveHistory(id)
	if err == nil && removed {
		wailsruntime.EventsEmit(a.ctx, "history:update", a.store.History())
	}
	return err
}

// DeleteDownloadFile deletes the media file for one history entry and then
// removes that history row. History-only removal stays on RemoveDownload.
func (a *App) DeleteDownloadFile(id string) error {
	if err := a.requireReady(); err != nil {
		return err
	}
	deleted, err := a.store.DeleteHistoryFile(id)
	if err != nil {
		return err
	}
	if deleted {
		wailsruntime.EventsEmit(a.ctx, "history:update", a.store.History())
	}
	return nil
}

// ClearDownloads empties the persisted history.
func (a *App) ClearDownloads() error {
	if err := a.requireReady(); err != nil {
		return err
	}
	if err := a.store.ClearHistory(); err != nil {
		return err
	}
	wailsruntime.EventsEmit(a.ctx, "history:update", a.store.History())
	return nil
}

// OpenFile opens a downloaded file with the OS default application.
// It uses xdg-open / open / rundll32 so the platform handler is the
// one users already trust.
func (a *App) OpenFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("empty path")
	}
	expanded, err := expandHome(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(expanded); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("That downloaded file is no longer on disk.")
		}
		return err
	}
	return launchWithOS(expanded)
}

// RevealInFinder reveals a path in Finder / Explorer / file manager.
func (a *App) RevealInFinder(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("empty path")
	}
	expanded, err := expandHome(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(expanded); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("That downloaded file is no longer on disk.")
		}
		return err
	}
	return revealWithOS(expanded)
}

// CopyDiagnostics writes a small, sanitised diagnostic report to the
// clipboard. It never includes the URL or any file path beyond the
// folder basename.
func (a *App) CopyDiagnostics() (string, error) {
	status := a.ffmpegStatus()
	build := currentBuildInfo()
	report := strings.Builder{}
	report.WriteString("VidStow diagnostics\n")
	report.WriteString("App: VidStow v" + build.Version + " (" + build.OS + "/" + build.Architecture + ", " + build.GoVersion + ")\n")
	report.WriteString("Engine: youtube_dlp " + build.EngineVersion + "\n")
	if a.store != nil {
		report.WriteString("Download folder: " + filepath.Base(a.store.Settings().DownloadFolder) + "\n")
	} else {
		report.WriteString("Startup: recovery required (" + string(a.startupStatusSnapshot().Reason) + ")\n")
	}
	// Privacy: do not include the absolute FFmpeg path. The basename
	// tells support which binary the user picked without disclosing
	// home-directory layout. If no configured path is present we still
	// note whether detection succeeded.
	if status.Path != "" {
		report.WriteString("FFmpeg: " + status.Message + " (" + filepath.Base(status.Path) + ")")
	} else {
		report.WriteString("FFmpeg: " + status.Message)
	}
	report.WriteString("\n")
	if a.jobs != nil {
		report.WriteString(fmt.Sprintf("Queue depth: %d\n", len(a.jobs.List())))
	} else {
		report.WriteString("Queue depth: unavailable until recovery completes\n")
	}
	text := report.String()
	if err := wailsruntime.ClipboardSetText(a.ctx, text); err != nil {
		return "", err
	}
	return text, nil
}

func (a *App) requireReady() error {
	if a.store == nil || a.jobs == nil || a.coordinator == nil || !a.startupStatusSnapshot().Healthy() {
		return errRecoveryRequired
	}
	if status := a.store.Status(); status.Warning != "" {
		return errRecoveryRequired
	}
	return nil
}

func (a *App) setStartupStatus(status store.StartupStatus) {
	a.mu.Lock()
	a.startupStatus = status
	a.mu.Unlock()
}

func (a *App) startupStatusSnapshot() store.StartupStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.startupStatus
}

func canonicalOutputRequestPath(path string) (string, error) {
	expanded, err := expandHome(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	if expanded == "" {
		return "", errors.New("output directory is required")
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// prepareStartupRoots validates the settings root and every durable root
// referenced by a job/tombstone before the recovery pass can inspect or clean
// sessions. Missing per-job roots are preserved for a later user repair;
// replaced, linked, or identity-mismatched roots fail closed.
func prepareStartupRoots(state jobmodel.State) error {
	settingsRoot, err := canonicalOutputRequestPath(state.Settings.DownloadFolder)
	if err != nil {
		return err
	}
	// The settings root is the app's own destination and may be created, but
	// only through no-follow-safe primitives so a symlinked/replaced parent is
	// rejected before any component below it is created. Per-job roots below
	// are never created here: a missing one is preserved for user repair.
	if _, err := reservationfs.EnsureRoot(settingsRoot); err != nil {
		return err
	}
	refs := map[string]jobmodel.OutputRootRef{
		stateKeyRoot(settingsRoot, ""): {CanonicalPath: settingsRoot},
	}
	for _, job := range state.Jobs {
		if job.OutputRoot.CanonicalPath != "" {
			refs[stateKeyRoot(job.OutputRoot.CanonicalPath, job.OutputRoot.Identity)] = job.OutputRoot
		}
	}
	for _, tombstone := range state.Cleanup {
		if tombstone.OutputRoot.CanonicalPath != "" {
			refs[stateKeyRoot(tombstone.OutputRoot.CanonicalPath, tombstone.OutputRoot.Identity)] = tombstone.OutputRoot
		}
	}
	for _, ref := range refs {
		if ref.CanonicalPath == "" {
			continue
		}
		root, openErr := reservationfs.OpenRoot(ref.CanonicalPath)
		if openErr != nil {
			if errors.Is(openErr, os.ErrNotExist) {
				continue
			}
			return openErr
		}
		facts := root.Facts()
		closeErr := root.Close()
		if closeErr != nil {
			return closeErr
		}
		if ref.Identity != "" && (facts.Volume.CanonicalPath != ref.CanonicalPath || facts.Volume.Identity != ref.Identity) {
			return errors.New("output root identity changed")
		}
		if ref.EngineIdentity != "" {
			engineFacts, engineErr := engine.ValidateOutputRoot(ref.CanonicalPath)
			if engineErr != nil || engineFacts.Identity != ref.EngineIdentity {
				return errors.New("engine output root identity changed")
			}
		}
	}
	return nil
}

func stateKeyRoot(path, identity string) string { return identity + "\x00" + path }

func (a *App) shutdownContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	a.quitMu.Lock()
	deadline := a.quitDeadline
	a.quitMu.Unlock()
	if !deadline.IsZero() {
		return context.WithDeadline(parent, deadline)
	}
	return context.WithTimeout(parent, jobs.DefaultShutdownTimeout)
}

func (a *App) stopCleanup(ctx context.Context) {
	if a.cleanupCancel != nil {
		a.cleanupCancel()
		a.cleanupCancel = nil
	}
	if a.cleanupDone == nil {
		return
	}
	select {
	case <-a.cleanupDone:
	case <-ctx.Done():
	}
	a.cleanupDone = nil
}

// beforeClose is the Wails OnBeforeClose gate. Returning true prevents the
// native close and emits one backend-authored confirmation payload. The
// explicit PauseDownloadsAndQuit action grants a one-shot permit before it
// calls runtime.Quit, so the Wails callback is not recursive.
func (a *App) beforeClose(ctx context.Context) bool {
	a.quitMu.Lock()
	if a.quitPermit {
		a.quitPermit = false
		a.quitRequestOpen = false
		a.quitMu.Unlock()
		return false
	}
	if a.quitRequestOpen {
		a.quitMu.Unlock()
		return true
	}
	manager := a.jobs
	if manager == nil || !manager.HasActive() {
		a.quitMu.Unlock()
		return false
	}
	a.quitRequestOpen = true
	a.quitMu.Unlock()
	wailsruntime.EventsEmit(ctx, "quit:request", manager.QuitSummary())
	return true
}

// KeepWorking dismisses an outstanding native close confirmation.
func (a *App) KeepWorking() {
	a.quitMu.Lock()
	a.quitRequestOpen = false
	a.quitMu.Unlock()
}

// PauseDownloadsAndQuit performs ordinary pause-and-quit under one shared
// deadline, then grants the one-shot native close permit.
func (a *App) PauseDownloadsAndQuit() error {
	if a.jobs == nil {
		return errRecoveryRequired
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	deadline := time.Now().Add(jobs.DefaultShutdownTimeout)
	a.quitMu.Lock()
	a.quitDeadline = deadline
	a.quitRequestOpen = false
	a.quitMu.Unlock()
	shutdownCtx, cancel := context.WithDeadline(parent, deadline)
	err := a.jobs.Shutdown(shutdownCtx)
	a.stopCleanup(shutdownCtx)
	cancel()
	a.quitMu.Lock()
	a.quitPermit = true
	a.quitMu.Unlock()
	if a.ctx != nil {
		wailsruntime.Quit(a.ctx)
	}
	return err
}

// OpenDataFolder is safe in recovery-required mode and performs no State or
// session mutation.
func (a *App) OpenDataFolder() error {
	directory := filepath.Dir(a.statePath)
	if directory == "." || directory == "" {
		return errors.New("data folder is unavailable")
	}
	if _, err := os.Stat(directory); err != nil {
		return err
	}
	return launchWithOS(directory)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func isSupportedQuality(q jobs.Quality) bool {
	for _, candidate := range jobs.AllQualities {
		if candidate == q {
			return true
		}
	}
	return false
}

func isTerminal(s jobs.Status) bool {
	return s == jobs.StatusComplete || s == jobs.StatusFailed || s == jobs.StatusCanceled
}

func (a *App) setFFmpegStatus(status ffmpegdetect.Status) {
	a.mu.Lock()
	a.lastFFmpeg = status
	a.mu.Unlock()
}

func (a *App) ffmpegStatus() ffmpegdetect.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastFFmpeg
}

func videoSubfolder(title, videoID string) string {
	name := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return -1
		}
		return r
	}, strings.TrimSpace(title))
	runes := []rune(strings.Trim(name, " ."))
	if len(runes) > 72 {
		runes = runes[:72]
	}
	name = strings.Trim(string(runes), " .")
	if name == "" {
		name = "Video"
	}
	if videoID != "" {
		name += " [" + videoID + "]"
	}
	return name
}

func expandHome(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}

func launchWithOS(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

func revealWithOS(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", "-R", path).Start()
	case "windows":
		return exec.Command("explorer", "/select,", path).Start()
	default:
		dir := filepath.Dir(path)
		return exec.Command("xdg-open", dir).Start()
	}
}

func friendlyAnalyzeError(err error) string {
	if err == nil {
		return "We could not read that video. Try again in a moment."
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Video analysis timed out — retry"
	}
	var typed *engine.Error
	if errors.As(err, &typed) {
		switch typed.Category {
		case engine.ErrorUnsupported:
			if isYouTubeChallengeTimeout(err) {
				return "YouTube challenge timed out — retry"
			}
			return "That link is not a supported single YouTube video."
		case engine.ErrorAuthentication:
			return "That video requires sign-in and is not available in this version."
		case engine.ErrorInvalidInput:
			return "That YouTube link is not valid."
		case engine.ErrorNetwork:
			return "We could not reach YouTube. Check your connection and try again."
		case engine.ErrorCancelled:
			return "Video analysis was canceled."
		case engine.ErrorSecurity:
			return "This video was blocked by a security check."
		}
	}
	return "We could not read that video. Try again in a moment."
}

func isYouTubeChallengeTimeout(err error) bool {
	message := err.Error()
	return strings.Contains(message, "JavaScript challenge solver unavailable") &&
		strings.Contains(message, "EJS helper timeout") &&
		strings.Contains(message, "JavaScript execution timed out")
}
