package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tejasa97/vidstow/internal/jobmodel"
)

type legacyStateV1 struct {
	Version  int                     `json:"version"`
	Settings legacySettingsV1        `json:"settings"`
	History  []jobmodel.HistoryEntry `json:"history"`
	Jobs     []legacyPersistedJobV1  `json:"jobs"`
}

type legacySettingsV1 struct {
	DownloadFolder        string `json:"downloadFolder"`
	FFmpegPath            string `json:"ffmpegPath"`
	WindowWidth           int    `json:"windowWidth"`
	WindowHeight          int    `json:"windowHeight"`
	DownloadConcurrency   int    `json:"downloadConcurrency"`
	PerVideoSubfolder     bool   `json:"perVideoSubfolder"`
	ConfirmBeforeDownload bool   `json:"confirmBeforeDownload"`
	// RestoreInterruptedJobs is intentionally read and discarded.
	RestoreInterruptedJobs bool `json:"restoreInterruptedJobs"`
}

type legacyPersistedJobV1 struct {
	Snapshot legacySnapshotV1 `json:"snapshot"`
	Plan     legacyPlanV1     `json:"plan"`
}

type legacySnapshotV1 struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	VideoID       string `json:"videoID"`
	Title         string `json:"title"`
	Channel       string `json:"channel"`
	Quality       string `json:"quality"`
	PlanID        string `json:"planId"`
	OutputDir     string `json:"outputDir"`
	DurationLabel string `json:"durationLabel"`
	Thumbnail     string `json:"thumbnail"`
	Status        string `json:"status"`
	CreatedAt     string `json:"createdAt"`
	Filename      string `json:"filename"`
}

type legacyPlanV1 struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Label          string `json:"label"`
	Container      string `json:"container"`
	VideoCodec     string `json:"videoCodec"`
	AudioCodec     string `json:"audioCodec"`
	RequiresFFmpeg bool   `json:"requiresFfmpeg"`
}

func migrateV1(path string, data []byte) (jobmodel.State, error) {
	var legacy legacyStateV1
	if err := json.Unmarshal(data, &legacy); err != nil {
		return jobmodel.State{}, fmt.Errorf("store: decode v1: %w", err)
	}
	if legacy.Version != 1 && legacy.Version != 0 {
		return jobmodel.State{}, errors.New("store: not a v1 state")
	}
	if err := backupPreV2(path, data); err != nil {
		return jobmodel.State{}, err
	}

	state := defaultStateV2()
	state.Settings = jobmodel.Settings{
		DownloadFolder:        legacy.Settings.DownloadFolder,
		FFmpegPath:            legacy.Settings.FFmpegPath,
		WindowWidth:           legacy.Settings.WindowWidth,
		WindowHeight:          legacy.Settings.WindowHeight,
		DownloadConcurrency:   legacy.Settings.DownloadConcurrency,
		PerVideoSubfolder:     legacy.Settings.PerVideoSubfolder,
		ConfirmBeforeDownload: legacy.Settings.ConfirmBeforeDownload,
	}
	state.Settings = normalizeV2Settings(state.Settings)
	state.History = append([]jobmodel.HistoryEntry(nil), legacy.History...)

	for _, old := range legacy.Jobs {
		job := migrateLegacyJob(old)
		job.QueueOrdinal = state.NextQueueOrdinal
		state.NextQueueOrdinal++
		state.Jobs = append(state.Jobs, job)
	}
	return state, nil
}

func migrateLegacyJob(old legacyPersistedJobV1) jobmodel.DurableJob {
	now := utcNow()
	created, err := time.Parse(time.RFC3339, old.Snapshot.CreatedAt)
	if err != nil || created.IsZero() {
		created = now
	}
	job := jobmodel.DurableJob{
		ID:        nonEmptyID(old.Snapshot.ID),
		Revision:  1,
		AttemptID: uuid.NewString(),
		SessionID: uuid.NewString(),
		Lifecycle: jobmodel.LifecyclePaused,
		Phase:     jobmodel.PhasePreparing,
		Desired:   jobmodel.DesiredPaused,
		Request: jobmodel.PersistedRequest{
			SourceURL: safeSourceURL(old.Snapshot.URL), VideoID: old.Snapshot.VideoID,
			Title: old.Snapshot.Title, Channel: old.Snapshot.Channel, Quality: old.Snapshot.Quality,
			PlanID: old.Snapshot.PlanID, Duration: old.Snapshot.DurationLabel,
		},
		Plan: jobmodel.PersistedPlan{
			ID: old.Plan.ID, Kind: old.Plan.Kind, Label: old.Plan.Label, Container: old.Plan.Container,
			VideoCodec: old.Plan.VideoCodec, AudioCodec: old.Plan.AudioCodec, RequiresFFmpeg: old.Plan.RequiresFFmpeg,
		},
		RetryMode: jobmodel.RetryModeRestartNewSession,
		CreatedAt: created,
		UpdatedAt: now,
	}
	if root, basename, ok := legacyReservation(old.Snapshot.OutputDir, old.Snapshot.Filename); ok {
		job.OutputRoot = root
		job.Reservation = jobmodel.ReservationSet{
			GroupID: job.ID, Directory: root,
			Artifacts: []jobmodel.ReservedArtifact{{Kind: "primary", Identity: "primary", Basename: basename}},
		}
	} else {
		job.Lifecycle = jobmodel.LifecycleActionRequired
		job.Phase = jobmodel.PhaseReadyToPublish
		job.ActionRequiredCode = "migration-destination-unverified"
	}
	return job
}

func normalizeV2Settings(settings jobmodel.Settings) jobmodel.Settings {
	defaults := defaultStateV2().Settings
	if settings.DownloadFolder == "" {
		settings.DownloadFolder = defaults.DownloadFolder
	}
	if settings.WindowWidth <= 0 {
		settings.WindowWidth = defaults.WindowWidth
	}
	if settings.WindowHeight <= 0 {
		settings.WindowHeight = defaults.WindowHeight
	}
	if settings.DownloadConcurrency < 1 {
		settings.DownloadConcurrency = defaults.DownloadConcurrency
	}
	if settings.DownloadConcurrency > 10 {
		settings.DownloadConcurrency = 10
	}
	return settings
}

func legacyReservation(outputDir, filename string) (jobmodel.OutputRootRef, string, bool) {
	if strings.TrimSpace(outputDir) == "" || strings.TrimSpace(filename) == "" {
		return jobmodel.OutputRootRef{}, "", false
	}
	root, err := filepath.Abs(outputDir)
	if err != nil {
		return jobmodel.OutputRootRef{}, "", false
	}
	basename := filepath.Base(filename)
	if basename == "." || basename == string(filepath.Separator) || basename != filename || strings.ContainsAny(basename, `\\/`) {
		return jobmodel.OutputRootRef{}, "", false
	}
	canonical := filepath.Clean(root)
	return jobmodel.OutputRootRef{CanonicalPath: canonical, Identity: canonical}, basename, true
}

func safeSourceURL(raw string) string {
	// Legacy UI requests are page URLs. Query strings, fragments, and userinfo
	// can carry credentials and are not needed for durable identity.
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.RawQuery, parsed.Fragment, parsed.User = "", "", nil
	return parsed.String()
}

func nonEmptyID(value string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return uuid.NewString()
}

func backupPreV2(path string, data []byte) error {
	backup := path + ".pre-v2.bak"
	f, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("store: create pre-v2 backup: %w", err)
	}
	if err := f.Chmod(0o600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(backup)
		return fmt.Errorf("store: write pre-v2 backup: %w", err)
	}
	if err := syncParent(filepath.Dir(path)); err != nil {
		return fmt.Errorf("store: sync pre-v2 backup parent: %w", err)
	}
	return nil
}
