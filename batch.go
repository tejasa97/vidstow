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

	"github.com/google/uuid"
	"github.com/tejasa97/vidstow/internal/admission"
	"github.com/tejasa97/vidstow/internal/jobmodel"
	"github.com/tejasa97/vidstow/internal/jobs"
	"github.com/tejasa97/vidstow/internal/outputplan"
	"github.com/tejasa97/vidstow/internal/reservationfs"
	"github.com/tejasa97/vidstow/internal/urlcheck"
	"github.com/tejasa97/ytdlp-go/engine/value"
)

const (
	batchAnalysisConcurrency = 4
	batchAnalysisTTL         = 30 * time.Minute
	maxCachedBatchAnalyses   = 64
)

type BatchLineStatus string

const (
	BatchStatusReady          BatchLineStatus = "ready"
	BatchStatusDuplicate      BatchLineStatus = "duplicate"
	BatchStatusInvalid        BatchLineStatus = "invalid"
	BatchStatusAnalysisFailed BatchLineStatus = "analysis_failed"
)

type BatchAnalysisCounts struct {
	Pasted         int `json:"pasted"`
	Ready          int `json:"ready"`
	Duplicate      int `json:"duplicate"`
	Invalid        int `json:"invalid"`
	AnalysisFailed int `json:"analysisFailed"`
}

type BatchAnalysisItemView struct {
	LineNumber      int             `json:"lineNumber"`
	Input           string          `json:"input"`
	Status          BatchLineStatus `json:"status"`
	MessageKey      string          `json:"messageKey"`
	Message         string          `json:"message"`
	DuplicateOfLine int             `json:"duplicateOfLine,omitempty"`
	Title           string          `json:"title,omitempty"`
	Channel         string          `json:"channel,omitempty"`
	Duration        string          `json:"duration,omitempty"`
	Thumbnail       string          `json:"thumbnail,omitempty"`
}

type BatchAnalysisView struct {
	Token     string                  `json:"token,omitempty"`
	ExpiresAt time.Time               `json:"expiresAt,omitempty"`
	Counts    BatchAnalysisCounts     `json:"counts"`
	Items     []BatchAnalysisItemView `json:"items"`
}

type BatchStartRequest struct {
	Token        string       `json:"token"`
	Quality      jobs.Quality `json:"quality"`
	AudioBitrate int          `json:"audioBitrate,omitempty"`
}

type BatchStartResult struct {
	CollectionID string `json:"collectionId"`
	Admitted     int    `json:"admitted"`
}

type analyzedBatchItem struct {
	lineNumber   int
	canonicalURL string
	videoID      string
	summary      jobs.InfoSummary
	plans        []outputplan.Plan
}

type cachedBatchAnalysis struct {
	token     string
	expiresAt time.Time
	items     []analyzedBatchItem
	inUse     bool
}

type batchChildAnalyzer interface {
	AnalyzeForAdmission(context.Context, string) (jobs.InfoSummary, []outputplan.Plan, error)
}

type batchAnalysisWork struct {
	parsedIndex int
	line        urlcheck.BatchLine
}

type batchAnalysisResult struct {
	view     BatchAnalysisItemView
	analyzed *analyzedBatchItem
}

// AnalyzeBatchURLs parses, canonicalizes, and independently analyzes every
// unique valid line. The returned token is the only admission authority; raw
// URLs, canonical identities, and private format selectors from the renderer
// are never trusted by StartBatchDownload.
func (a *App) AnalyzeBatchURLs(raw string) (BatchAnalysisView, error) {
	if err := a.requireReady(); err != nil {
		return BatchAnalysisView{}, err
	}
	parsed, err := urlcheck.ParseBatch(raw)
	if err != nil {
		return BatchAnalysisView{}, err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	view, analyzed := analyzeBatchLines(ctx, a.jobs, parsed)
	if len(analyzed) == 0 {
		return view, nil
	}
	view.Token = uuid.NewString()
	view.ExpiresAt = time.Now().UTC().Add(batchAnalysisTTL)
	plan := &cachedBatchAnalysis{token: view.Token, expiresAt: view.ExpiresAt, items: analyzed}
	if err := a.cacheBatchAnalysis(plan, time.Now().UTC()); err != nil {
		return BatchAnalysisView{}, err
	}
	return view, nil
}

func analyzeBatchLines(ctx context.Context, analyzer batchChildAnalyzer, parsed urlcheck.BatchParseResult) (BatchAnalysisView, []analyzedBatchItem) {
	view := BatchAnalysisView{
		Counts: BatchAnalysisCounts{Pasted: parsed.Pasted, Duplicate: parsed.Duplicates, Invalid: parsed.Invalid},
		Items:  make([]BatchAnalysisItemView, len(parsed.Lines)),
	}
	workItems := make([]batchAnalysisWork, 0, parsed.Ready)
	for index, line := range parsed.Lines {
		item := BatchAnalysisItemView{
			LineNumber: line.LineNumber, Input: line.Input, MessageKey: line.MessageKey,
			Message: line.Message, DuplicateOfLine: line.DuplicateOfLine,
		}
		switch line.Status {
		case urlcheck.BatchLineDuplicate:
			item.Status = BatchStatusDuplicate
		case urlcheck.BatchLineInvalid:
			item.Status = BatchStatusInvalid
		case urlcheck.BatchLineReady:
			workItems = append(workItems, batchAnalysisWork{parsedIndex: index, line: line})
		}
		view.Items[index] = item
	}
	if len(workItems) == 0 {
		return view, nil
	}

	results := make([]batchAnalysisResult, len(workItems))
	for index, candidate := range workItems {
		results[index].view = BatchAnalysisItemView{
			LineNumber: candidate.line.LineNumber, Input: candidate.line.Input,
			Status: BatchStatusAnalysisFailed, MessageKey: "batch.analysis_failed",
			Message: "Video analysis was canceled.",
		}
	}
	work := make(chan int)
	workerCount := batchAnalysisConcurrency
	if len(workItems) < workerCount {
		workerCount = len(workItems)
	}
	var wait sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range work {
				candidate := workItems[index]
				childCtx, childCancel := context.WithTimeout(ctx, 75*time.Second)
				summary, plans, err := analyzer.AnalyzeForAdmission(childCtx, candidate.line.CanonicalURL)
				childCancel()
				if err == nil && (summary.VideoID != candidate.line.VideoID || summary.URL != candidate.line.CanonicalURL) {
					err = errors.New("analyzed video identity did not match the requested URL")
				}
				if err == nil && len(plans) == 0 {
					err = errors.New("no supported output is available for this video")
				}
				if err != nil {
					results[index].view = BatchAnalysisItemView{
						LineNumber: candidate.line.LineNumber, Input: candidate.line.Input,
						Status: BatchStatusAnalysisFailed, MessageKey: "batch.analysis_failed",
						Message: friendlyAnalyzeError(err),
					}
					continue
				}
				if strings.TrimSpace(summary.Title) == "" {
					summary.Title = "Untitled video"
				}
				if strings.TrimSpace(summary.Channel) == "" {
					summary.Channel = "YouTube"
				}
				summary.Thumbnail = strings.TrimSpace(summary.Thumbnail)
				if summary.Thumbnail == "" {
					summary.Thumbnail = "https://i.ytimg.com/vi/" + candidate.line.VideoID + "/hqdefault.jpg"
				}
				results[index].view = BatchAnalysisItemView{
					LineNumber: candidate.line.LineNumber, Input: candidate.line.Input,
					Status: BatchStatusReady, MessageKey: "batch.ready", Message: "Ready",
					Title: summary.Title, Channel: summary.Channel, Duration: summary.Duration, Thumbnail: summary.Thumbnail,
				}
				results[index].analyzed = &analyzedBatchItem{
					lineNumber: candidate.line.LineNumber, canonicalURL: candidate.line.CanonicalURL,
					videoID: candidate.line.VideoID, summary: summary, plans: append([]outputplan.Plan(nil), plans...),
				}
			}
		}()
	}
	func() {
		defer close(work)
		for index := range workItems {
			select {
			case work <- index:
			case <-ctx.Done():
				return
			}
		}
	}()
	wait.Wait()

	analyzed := make([]analyzedBatchItem, 0, len(results))
	for index, result := range results {
		view.Items[workItems[index].parsedIndex] = result.view
		if result.analyzed != nil {
			view.Counts.Ready++
			analyzed = append(analyzed, *result.analyzed)
		} else {
			view.Counts.AnalysisFailed++
		}
	}
	sort.SliceStable(analyzed, func(i, j int) bool { return analyzed[i].lineNumber < analyzed[j].lineNumber })
	return view, analyzed
}

// StartBatchDownload resolves a backend-owned analysis token and atomically
// admits all ready items as one durable collection.
func (a *App) StartBatchDownload(req BatchStartRequest) (BatchStartResult, error) {
	if err := a.requireReady(); err != nil {
		return BatchStartResult{}, err
	}
	policy, err := validatePlaylistPolicy(req.Quality, req.AudioBitrate)
	if err != nil {
		return BatchStartResult{}, err
	}
	if req.Quality == jobs.QualityAudioOnly && req.AudioBitrate != 0 && !a.ffmpegStatus().Available {
		return BatchStartResult{}, errors.New("MP3 conversion needs FFmpeg; choose original audio or configure FFmpeg")
	}
	analysis, err := a.claimBatchAnalysis(req.Token)
	if err != nil {
		return BatchStartResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			a.releaseBatchAnalysis(req.Token)
		}
	}()
	if len(analysis.items) < urlcheck.MinBatchLines {
		return BatchStartResult{}, fmt.Errorf("at least %d ready videos are required to start a batch", urlcheck.MinBatchLines)
	}

	settings := a.store.Settings()
	if strings.TrimSpace(settings.DownloadFolder) == "" {
		return BatchStartResult{}, errors.New("choose a default download folder before starting this batch")
	}
	children := make([]admission.CollectionChildRequest, len(analysis.items))
	openedRoots := make(map[string]*reservationfs.Root)
	defer func() {
		for _, root := range openedRoots {
			_ = root.Close()
		}
	}()
	var defaultRoot *reservationfs.Root
	for index, item := range analysis.items {
		plan, chooseErr := choosePlaylistPlan(item.plans, req.Quality, req.AudioBitrate)
		if chooseErr != nil {
			return BatchStartResult{}, fmt.Errorf("line %d: %w", item.lineNumber, chooseErr)
		}
		if plan.RequiresFFmpeg && !a.ffmpegStatus().Available {
			return BatchStartResult{}, fmt.Errorf("line %d needs FFmpeg; install FFmpeg or choose original audio", item.lineNumber)
		}
		outputDir := settings.DownloadFolder
		if settings.PerVideoSubfolder {
			outputDir = filepath.Join(outputDir, videoSubfolder(item.summary.Title, item.videoID))
		}
		outputDir, err = canonicalOutputRequestPath(outputDir)
		if err != nil {
			return BatchStartResult{}, err
		}
		root := openedRoots[outputDir]
		if root == nil {
			root, err = reservationfs.EnsureOpenRoot(outputDir)
			if err != nil {
				return BatchStartResult{}, fmt.Errorf("could not create output folder for line %d: %w", item.lineNumber, err)
			}
			openedRoots[outputDir] = root
		}
		if defaultRoot == nil {
			defaultRoot = root
		}
		children[index] = admission.CollectionChildRequest{
			Request: admission.Request{Queue: jobs.Request{
				URL: item.canonicalURL, VideoID: item.videoID, Title: item.summary.Title,
				Channel: item.summary.Channel, Quality: req.Quality, PlanID: plan.ID,
				OutputDir: outputDir, Duration: item.summary.Duration, Thumbnail: item.summary.Thumbnail,
			}, Metadata: value.NewInfo(value.NewObject(
				value.Field{Key: "title", Value: value.String(item.summary.Title)},
				value.Field{Key: "id", Value: value.String(item.videoID)},
				value.Field{Key: "channel", Value: value.String(item.summary.Channel)},
			))},
			ResolvedPlan: plan,
			Root:         root,
		}
	}
	result, admitErr := a.coordinator.AdmitCollection(a.ctx, defaultRoot, admission.CollectionRequest{
		Collection: admission.Collection{
			Kind: jobmodel.CollectionKindBatch, Title: fmt.Sprintf("Batch download · %d videos", len(children)), Policy: policy,
		},
		Children: children,
	})
	if result.Collection.ID != "" {
		committed = true
		a.consumeBatchAnalysis(req.Token)
	}
	startResult := BatchStartResult{CollectionID: result.Collection.ID, Admitted: len(result.Children)}
	if admitErr != nil {
		return startResult, admitErr
	}
	return startResult, nil
}

func (a *App) cacheBatchAnalysis(plan *cachedBatchAnalysis, now time.Time) error {
	if plan == nil || strings.TrimSpace(plan.token) == "" || !now.Before(plan.expiresAt) {
		return errors.New("batch analysis plan is invalid")
	}
	a.batchMu.Lock()
	defer a.batchMu.Unlock()
	a.pruneBatchAnalysesLocked(now)
	if a.batchPlans == nil {
		a.batchPlans = make(map[string]*cachedBatchAnalysis)
	}
	if len(a.batchPlans) >= maxCachedBatchAnalyses {
		oldestToken := ""
		oldestExpiry := time.Time{}
		for token, candidate := range a.batchPlans {
			if candidate.inUse {
				continue
			}
			if oldestToken == "" || candidate.expiresAt.Before(oldestExpiry) {
				oldestToken, oldestExpiry = token, candidate.expiresAt
			}
		}
		if oldestToken == "" {
			return errors.New("too many batch reviews are currently being started; try again in a moment")
		}
		delete(a.batchPlans, oldestToken)
	}
	a.batchPlans[plan.token] = plan
	return nil
}

func (a *App) pruneBatchAnalysesLocked(now time.Time) {
	for token, candidate := range a.batchPlans {
		if !candidate.inUse && !now.Before(candidate.expiresAt) {
			delete(a.batchPlans, token)
		}
	}
}

func (a *App) claimBatchAnalysis(token string) (cachedBatchAnalysis, error) {
	if strings.TrimSpace(token) == "" {
		return cachedBatchAnalysis{}, errors.New("batch review is missing; analyze the URLs again")
	}
	a.batchMu.Lock()
	defer a.batchMu.Unlock()
	now := time.Now().UTC()
	a.pruneBatchAnalysesLocked(now)
	candidate := a.batchPlans[token]
	if candidate == nil || !now.Before(candidate.expiresAt) {
		delete(a.batchPlans, token)
		return cachedBatchAnalysis{}, errors.New("batch review expired; analyze the URLs again")
	}
	if candidate.inUse {
		return cachedBatchAnalysis{}, errors.New("this batch is already being started")
	}
	candidate.inUse = true
	clone := *candidate
	clone.items = make([]analyzedBatchItem, len(candidate.items))
	for index, item := range candidate.items {
		clone.items[index] = item
		clone.items[index].plans = append([]outputplan.Plan(nil), item.plans...)
	}
	return clone, nil
}

func (a *App) releaseBatchAnalysis(token string) {
	a.batchMu.Lock()
	defer a.batchMu.Unlock()
	if candidate := a.batchPlans[token]; candidate != nil {
		candidate.inUse = false
	}
}

func (a *App) consumeBatchAnalysis(token string) {
	a.batchMu.Lock()
	delete(a.batchPlans, token)
	a.batchMu.Unlock()
}
