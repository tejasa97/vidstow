package store

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tejasa97/vidstow/internal/jobmodel"
)

type legacyStateV1 struct {
	Version  int                    `json:"version"`
	Settings legacySettingsV1       `json:"settings"`
	History  []legacyHistoryV1      `json:"history"`
	Jobs     []legacyPersistedJobV1 `json:"jobs"`
}

type legacyHistoryV1 struct {
	ID            string `json:"id"`
	VideoID       string `json:"videoId"`
	Title         string `json:"title"`
	Channel       string `json:"channel"`
	Quality       string `json:"quality"`
	Container     string `json:"container"`
	VideoCodec    string `json:"videoCodec"`
	AudioCodec    string `json:"audioCodec"`
	Filename      string `json:"filename"`
	AbsolutePath  string `json:"absolutePath"`
	SizeBytes     int64  `json:"sizeBytes"`
	CompletedAt   string `json:"completedAt"`
	DurationLabel string `json:"durationLabel"`
	Thumbnail     string `json:"thumbnail,omitempty"`
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
	Snapshot        legacySnapshotV1 `json:"snapshot"`
	Plan            legacyPlanV1     `json:"plan"`
	PrivateSelector string           `json:"privateSelector,omitempty"`
}

type legacySnapshotV1 struct {
	ID              string  `json:"id"`
	URL             string  `json:"url"`
	VideoID         string  `json:"videoID"`
	Title           string  `json:"title"`
	Channel         string  `json:"channel"`
	Quality         string  `json:"quality"`
	PlanID          string  `json:"planId"`
	OutputDir       string  `json:"outputDir"`
	DurationLabel   string  `json:"durationLabel"`
	Thumbnail       string  `json:"thumbnail"`
	Status          string  `json:"status"`
	CreatedAt       string  `json:"createdAt"`
	Filename        string  `json:"filename"`
	QualityLabel    string  `json:"qualityLabel"`
	OutputKind      string  `json:"outputKind"`
	Container       string  `json:"container"`
	VideoCodec      string  `json:"videoCodec"`
	AudioCodec      string  `json:"audioCodec"`
	ApproxBytes     int64   `json:"approxBytes"`
	SizeApproximate bool    `json:"sizeApproximate"`
	RequiresFFmpeg  bool    `json:"requiresFfmpeg"`
	CanPause        bool    `json:"canPause"`
	Processing      bool    `json:"processing"`
	StartedAt       string  `json:"startedAt"`
	CompletedAt     string  `json:"completedAt"`
	Bytes           int64   `json:"bytes"`
	Total           int64   `json:"total"`
	Progress        float64 `json:"progress"`
	SpeedBps        float64 `json:"speedBps"`
	ETASeconds      float64 `json:"etaSeconds"`
	AbsolutePath    string  `json:"absolutePath"`
	Message         string  `json:"message"`
	ErrorReason     string  `json:"errorReason"`
}

type legacyPlanV1 struct {
	ID                string `json:"id"`
	Kind              string `json:"kind"`
	Label             string `json:"label"`
	Container         string `json:"container"`
	VideoCodec        string `json:"videoCodec"`
	AudioCodec        string `json:"audioCodec"`
	RequiresFFmpeg    bool   `json:"requiresFfmpeg"`
	Resolution        string `json:"resolution"`
	Width             int64  `json:"width"`
	Height            int64  `json:"height"`
	ApproxBytes       int64  `json:"approxBytes"`
	SizeIsApproximate bool   `json:"sizeIsApproximate"`
	AudioBitrateKbps  int    `json:"audioBitrateKbps"`
	Recommended       bool   `json:"recommended"`
	Available         bool   `json:"available"`
}

func migrateV1(path string, data []byte) (jobmodel.State, error) {
	var legacy legacyStateV1
	if err := decodeStrict(data, &legacy); err != nil {
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
	for _, old := range legacy.History {
		state.History = append(state.History, jobmodel.HistoryEntry{ID: old.ID, VideoID: old.VideoID, Title: old.Title, Channel: old.Channel, Quality: old.Quality, Container: old.Container, VideoCodec: old.VideoCodec, AudioCodec: old.AudioCodec, Filename: old.Filename, AbsolutePath: old.AbsolutePath, SizeBytes: old.SizeBytes, CompletedAt: normalizeLegacyTimestamp(old.CompletedAt), DurationLabel: old.DurationLabel})
	}

	for _, old := range legacy.Jobs {
		job := migrateLegacyJob(old)
		job.QueueOrdinal = state.NextQueueOrdinal
		state.NextQueueOrdinal++
		state.Jobs = append(state.Jobs, job)
	}
	return state, nil
}

func migrateLegacyJob(old legacyPersistedJobV1) jobmodel.DurableJob {
	now := utcNow().UTC()
	created, err := time.Parse(time.RFC3339, old.Snapshot.CreatedAt)
	if err != nil || created.IsZero() {
		created = now
	} else {
		created = created.UTC()
		if !validTime(created) {
			created = now
		}
	}
	updated := now
	if updated.Before(created) {
		updated = created
	}
	job := jobmodel.DurableJob{
		ID:        nonEmptyID(old.Snapshot.ID),
		Revision:  1,
		AttemptID: uuid.NewString(),
		SessionID: newSessionID(),
		Lifecycle: jobmodel.LifecyclePaused,
		Phase:     jobmodel.PhasePreparing,
		Desired:   jobmodel.DesiredPaused,
		Request: jobmodel.PersistedRequest{
			SourceURL: safeSourceURL(old.Snapshot.URL, old.Snapshot.VideoID), VideoID: old.Snapshot.VideoID,
			Title: old.Snapshot.Title, Channel: old.Snapshot.Channel, Quality: old.Snapshot.Quality,
			PlanID: old.Snapshot.PlanID, Duration: old.Snapshot.DurationLabel,
		},
		Plan: jobmodel.PersistedPlan{
			ID: old.Plan.ID, Kind: old.Plan.Kind, Label: old.Plan.Label, Container: old.Plan.Container,
			VideoCodec: old.Plan.VideoCodec, AudioCodec: old.Plan.AudioCodec, RequiresFFmpeg: old.Plan.RequiresFFmpeg, PrivateSelector: safePrivateSelector(old.PrivateSelector),
		},
		RetryMode: jobmodel.RetryModeRestartNewSession,
		CreatedAt: created,
		UpdatedAt: updated,
	}
	// V0 has no public engine renderer or root-identity API. A legacy filename
	// cannot prove the exact artifact set or reservation, so every migrated row
	// awaits re-analysis/admission instead of inventing publication authority.
	job.Lifecycle = jobmodel.LifecycleActionRequired
	job.Phase = jobmodel.PhasePreparing
	job.ActionRequiredCode = "migration-reanalysis-required"
	if old.PrivateSelector != "" && job.Plan.PrivateSelector == "" {
		job.ActionRequiredCode = "migration-private-plan-unverified"
	}
	return job
}

func normalizeLegacyTimestamp(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.UTC().Format(time.RFC3339Nano)
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

func safeSourceURL(raw, videoID string) string {
	// Legacy UI requests are page URLs. Keep only the canonical public watch
	// identity reconstructed from the validated video ID; discard tracking,
	// fragments, userinfo, and every other query parameter.
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil || parsed.Port() != "" || parsed.Opaque != "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "youtube.com" && host != "www.youtube.com" && host != "m.youtube.com" && host != "youtu.be" {
		return ""
	}
	if !safeVideoID(videoID) {
		return ""
	}
	query, queryErr := url.ParseQuery(parsed.RawQuery)
	if queryErr != nil {
		return ""
	}
	if host == "youtu.be" {
		if strings.Trim(parsed.Path, "/") != videoID {
			return ""
		}
		if len(query["v"]) != 0 {
			return ""
		}
	} else {
		if parsed.Path != "/watch" || len(query["v"]) != 1 || query["v"][0] != videoID {
			return ""
		}
	}
	return "https://www.youtube.com/watch?v=" + videoID
}

func safePrivateSelector(value string) string {
	if !validPrivateSelector(value) {
		return ""
	}
	return value
}

func newSessionID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

func nonEmptyID(value string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return uuid.NewString()
}

func backupPreV2(path string, data []byte) error {
	backup := path + ".pre-v2.bak"
	if !validPath(backup) {
		return errors.New("store: pre-v2 backup path exceeds limit")
	}
	f, err := createPrivateExclusive(backup)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := openPrivateRead(backup)
		if readErr != nil {
			return fmt.Errorf("store: inspect pre-v2 backup: %w", readErr)
		}
		defer existing.Close()
		old, readErr := io.ReadAll(io.LimitReader(existing, maxStateBytes+1))
		if readErr != nil || len(old) > maxStateBytes {
			return fmt.Errorf("store: read pre-v2 backup: %w", readErr)
		}
		if !bytes.Equal(old, data) {
			return errors.New("store: pre-v2 backup conflicts with source state")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: create pre-v2 backup: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(backup)
		return fmt.Errorf("store: set pre-v2 backup permissions: %w", err)
	}
	var written int
	if written, err = f.Write(data); err == nil && written != len(data) {
		err = io.ErrShortWrite
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
