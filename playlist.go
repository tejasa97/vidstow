package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tejasa97/vidstow/internal/admission"
	"github.com/tejasa97/vidstow/internal/jobmodel"
	"github.com/tejasa97/vidstow/internal/jobs"
	"github.com/tejasa97/vidstow/internal/outputplan"
	"github.com/tejasa97/vidstow/internal/reservationfs"
	"github.com/tejasa97/vidstow/internal/urlcheck"
	"github.com/tejasa97/youtube_dlp/engine/value"
)

const playlistAnalysisConcurrency = 4

type StartPlaylistRequest struct {
	URL           string             `json:"url"`
	PlaylistID    string             `json:"playlistId"`
	Quality       jobs.Quality       `json:"quality"`
	AudioBitrate  int                `json:"audioBitrate,omitempty"`
	SelectedItems []int              `json:"selectedItems"`
	Options       jobs.OutputOptions `json:"options,omitempty"`
}

type playlistChildAnalyzer interface {
	AnalyzeForAdmission(context.Context, string) (jobs.InfoSummary, []outputplan.Plan, error)
}

type analyzedPlaylistChild struct {
	entry   jobs.PlaylistEntrySummary
	summary jobs.InfoSummary
	plan    outputplan.Plan
}

// StartPlaylistDownload resolves the renderer's bounded index selection
// through the trusted preview, analyzes every canonical child on the backend,
// chooses curated plans, then delegates one atomic collection to State v2.
func (a *App) StartPlaylistDownload(req StartPlaylistRequest) (string, error) {
	if err := a.requireReady(); err != nil {
		return "", err
	}
	a.playlistMu.Lock()
	defer a.playlistMu.Unlock()
	if err := a.requireReady(); err != nil {
		return "", err
	}
	validated, err := urlcheck.Validate(req.URL)
	if err != nil {
		return "", err
	}
	if validated.Kind != urlcheck.KindPlaylist || validated.PlaylistID == "" || validated.PlaylistID != req.PlaylistID {
		return "", errors.New("playlist identity does not match the analyzed preview")
	}
	policy, err := validatePlaylistPolicy(req.Quality, req.AudioBitrate)
	if err != nil {
		return "", err
	}
	if err := req.Options.Validate(); err != nil {
		return "", fmt.Errorf("invalid output options: %w", err)
	}
	if req.Options.RequiresFFmpeg() && !a.ffmpegStatus().Available {
		return "", errors.New("subtitles and embedded details need FFmpeg; install FFmpeg or turn those options off")
	}
	if req.Quality == jobs.QualityAudioOnly && req.AudioBitrate != 0 && !a.ffmpegStatus().Available {
		return "", errors.New("MP3 conversion needs FFmpeg; choose original audio or configure FFmpeg")
	}
	preview, entries, err := a.jobs.ResolvePlaylistSelection(validated.PlaylistID, req.SelectedItems)
	if err != nil {
		return "", err
	}
	if preview.ID != validated.PlaylistID || preview.URL != validated.PlaylistURL {
		return "", errors.New("playlist preview identity is no longer valid")
	}

	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	children, err := analyzePlaylistChildren(ctx, a.jobs, entries, req.Quality, req.AudioBitrate)
	if err != nil {
		logAppErrorf(a.ctx, "desktop: analyze playlist children: %v", err)
		return "", errors.New(friendlyAnalyzeError(err))
	}
	for _, child := range children {
		if child.plan.RequiresFFmpeg && !a.ffmpegStatus().Available {
			return "", errors.New("this playlist output needs FFmpeg; install FFmpeg or choose original audio")
		}
	}

	outputDir := filepath.Join(a.store.Settings().DownloadFolder, playlistSubfolder(preview.Title, preview.ID))
	outputDir, err = canonicalOutputRequestPath(outputDir)
	if err != nil {
		return "", err
	}
	root, err := reservationfs.EnsureOpenRoot(outputDir)
	if err != nil {
		return "", fmt.Errorf("could not create playlist output folder: %w", err)
	}
	defer root.Close()

	admissionChildren := make([]admission.CollectionChildRequest, len(children))
	for index, child := range children {
		admissionChildren[index] = admission.CollectionChildRequest{
			Request: admission.Request{Queue: jobs.Request{
				URL: child.entry.URL, VideoID: child.entry.VideoID, Title: child.summary.Title,
				Channel: child.summary.Channel, Quality: req.Quality, PlanID: child.plan.ID,
				OutputDir: outputDir, Duration: child.summary.Duration, Thumbnail: child.summary.Thumbnail,
				Options: req.Options,
			}, Metadata: value.NewInfo(value.NewObject(
				value.Field{Key: "title", Value: value.String(child.summary.Title)},
				value.Field{Key: "id", Value: value.String(child.entry.VideoID)},
				value.Field{Key: "channel", Value: value.String(child.summary.Channel)},
			))},
			ResolvedPlan: child.plan,
		}
	}
	result, err := a.coordinator.AdmitCollection(a.ctx, root, admission.CollectionRequest{
		Collection: admission.Collection{
			Kind: jobmodel.CollectionKindPlaylist, PlaylistID: preview.ID, SourceURL: preview.URL, Title: preview.Title,
			Channel: preview.Channel, Thumbnail: preview.Thumbnail, Policy: policy,
		},
		Children: admissionChildren,
	})
	if err != nil {
		return "", err
	}
	return result.Collection.ID, nil
}

func analyzePlaylistChildren(ctx context.Context, analyzer playlistChildAnalyzer, entries []jobs.PlaylistEntrySummary, quality jobs.Quality, bitrate int) ([]analyzedPlaylistChild, error) {
	results := make([]analyzedPlaylistChild, len(entries))
	work := make(chan int)
	workerCount := playlistAnalysisConcurrency
	if len(entries) < workerCount {
		workerCount = len(entries)
	}
	analysisCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wait sync.WaitGroup
	var firstErr error
	var errorOnce sync.Once
	fail := func(err error) {
		errorOnce.Do(func() { firstErr = err; cancel() })
	}
	for worker := 0; worker < workerCount; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range work {
				entry := entries[index]
				childCtx, childCancel := context.WithTimeout(analysisCtx, 75*time.Second)
				summary, privatePlans, err := analyzer.AnalyzeForAdmission(childCtx, entry.URL)
				childCancel()
				if err != nil {
					fail(fmt.Errorf("analyze playlist item %d: %w", entry.Index, err))
					return
				}
				if summary.VideoID != entry.VideoID || summary.URL != entry.URL {
					fail(fmt.Errorf("analyze playlist item %d: video identity mismatch", entry.Index))
					return
				}
				if strings.TrimSpace(summary.Title) == "" {
					summary.Title = entry.Title
					if strings.TrimSpace(summary.Title) == "" {
						summary.Title = "Untitled video"
					}
				}
				if summary.Channel == "" {
					summary.Channel = "YouTube"
				}
				if summary.Thumbnail == "" {
					summary.Thumbnail = entry.Thumbnail
				}
				plan, err := choosePlaylistPlan(privatePlans, quality, bitrate)
				if err != nil {
					fail(fmt.Errorf("analyze playlist item %d: %w", entry.Index, err))
					return
				}
				results[index] = analyzedPlaylistChild{entry: entry, summary: summary, plan: plan}
			}
		}()
	}
	func() {
		defer close(work)
		for index := range entries {
			select {
			case work <- index:
			case <-analysisCtx.Done():
				return
			}
		}
	}()
	wait.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func validatePlaylistPolicy(quality jobs.Quality, bitrate int) (string, error) {
	switch quality {
	case jobs.QualityBest, jobs.Quality4K, jobs.Quality1440p, jobs.Quality1080p, jobs.Quality720p:
		if bitrate != 0 {
			return "", errors.New("audio bitrate is only valid for audio playlists")
		}
		return "video:" + string(quality), nil
	case jobs.QualityAudioOnly:
		switch bitrate {
		case 0:
			return "audio:original", nil
		case 128, 192, 256:
			return fmt.Sprintf("audio:mp3-%d", bitrate), nil
		default:
			return "", errors.New("audio bitrate must be original, 128, 192, or 256 kbps")
		}
	default:
		return "", errors.New("unsupported playlist quality")
	}
}

func choosePlaylistPlan(plans []outputplan.Plan, quality jobs.Quality, bitrate int) (outputplan.Plan, error) {
	if quality == jobs.QualityAudioOnly {
		for _, plan := range plans {
			if plan.Kind != outputplan.KindAudio || !plan.Available {
				continue
			}
			if bitrate == 0 && !plan.RequiresFFmpeg && plan.AudioBitrateKbps == 0 {
				return plan, nil
			}
			if bitrate != 0 && plan.RequiresFFmpeg && plan.AudioBitrateKbps == bitrate {
				return plan, nil
			}
		}
		return outputplan.Plan{}, errors.New("requested audio output is unavailable")
	}
	caps := map[jobs.Quality]int64{jobs.Quality4K: 2160, jobs.Quality1440p: 1440, jobs.Quality1080p: 1080, jobs.Quality720p: 720}
	capHeight := caps[quality]
	candidates := make([]outputplan.Plan, 0, len(plans))
	for _, plan := range plans {
		if plan.Kind != outputplan.KindVideo || !plan.Available || plan.Height <= 0 || (capHeight > 0 && plan.Height > capHeight) {
			continue
		}
		candidates = append(candidates, plan)
	}
	if len(candidates) == 0 {
		return outputplan.Plan{}, errors.New("requested video output is unavailable")
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Height > candidates[j].Height })
	return candidates[0], nil
}

func playlistSubfolder(title, playlistID string) string {
	idRunes := []rune(strings.TrimSpace(playlistID))
	if len(idRunes) > 64 {
		idRunes = idRunes[:64]
	}
	name := videoSubfolder(title, string(idRunes))
	const maxFolderBytes = 200
	for len(name) > maxFolderBytes {
		_, size := utf8.DecodeLastRuneInString(name)
		if size <= 0 {
			break
		}
		name = name[:len(name)-size]
	}
	name = strings.Trim(name, " .")
	if name == "" {
		return "Playlist"
	}
	return name
}
