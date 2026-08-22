package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tejasa97/vidstow/internal/jobs"
	"github.com/tejasa97/vidstow/internal/outputplan"
	"github.com/tejasa97/vidstow/internal/urlcheck"
)

type fixtureBatchAnalyzer struct{}

func (fixtureBatchAnalyzer) AnalyzeForAdmission(_ context.Context, raw string) (jobs.InfoSummary, []outputplan.Plan, error) {
	validated, err := urlcheck.Validate(raw)
	if err != nil {
		return jobs.InfoSummary{}, nil, err
	}
	if validated.VideoID == "lmnopqrstuv" {
		return jobs.InfoSummary{}, nil, errors.New("private extraction detail")
	}
	return jobs.InfoSummary{
		URL: raw, VideoID: validated.VideoID, Title: "Video " + validated.VideoID, Channel: "Fixture", Duration: "1:00",
	}, []outputplan.Plan{{ID: "video", Kind: outputplan.KindVideo, Label: "1080p", Container: "mp4", Available: true}}, nil
}

func TestAnalyzeBatchLinesPreservesMixedResults(t *testing.T) {
	parsed, err := urlcheck.ParseBatch("https://youtu.be/abcdefghijk\nhttps://youtu.be/lmnopqrstuv\nhttps://www.youtube.com/watch?v=abcdefghijk\nnot-a-url")
	if err != nil {
		t.Fatalf("ParseBatch() error = %v", err)
	}
	view, analyzed := analyzeBatchLines(context.Background(), fixtureBatchAnalyzer{}, parsed)
	if view.Counts.Pasted != 4 || view.Counts.Ready != 1 || view.Counts.Duplicate != 1 || view.Counts.Invalid != 1 || view.Counts.AnalysisFailed != 1 {
		t.Fatalf("counts = %+v", view.Counts)
	}
	want := []BatchLineStatus{BatchStatusReady, BatchStatusAnalysisFailed, BatchStatusDuplicate, BatchStatusInvalid}
	for index, status := range want {
		if view.Items[index].Status != status {
			t.Fatalf("item %d status = %q, want %q", index, view.Items[index].Status, status)
		}
	}
	if view.Items[1].Message == "private extraction detail" {
		t.Fatal("raw analysis failure crossed the view boundary")
	}
	if len(analyzed) != 1 || analyzed[0].videoID != "abcdefghijk" || analyzed[0].lineNumber != 1 {
		t.Fatalf("analyzed = %+v", analyzed)
	}
}

type concurrencyBatchAnalyzer struct {
	active atomic.Int32
	max    atomic.Int32
}

func (a *concurrencyBatchAnalyzer) AnalyzeForAdmission(ctx context.Context, raw string) (jobs.InfoSummary, []outputplan.Plan, error) {
	current := a.active.Add(1)
	defer a.active.Add(-1)
	for {
		maximum := a.max.Load()
		if current <= maximum || a.max.CompareAndSwap(maximum, current) {
			break
		}
	}
	select {
	case <-ctx.Done():
		return jobs.InfoSummary{}, nil, ctx.Err()
	case <-time.After(20 * time.Millisecond):
	}
	validated, err := urlcheck.Validate(raw)
	if err != nil {
		return jobs.InfoSummary{}, nil, err
	}
	return jobs.InfoSummary{URL: raw, VideoID: validated.VideoID, Title: "Video"}, []outputplan.Plan{{ID: "video", Kind: outputplan.KindVideo, Container: "mp4", Available: true}}, nil
}

func TestAnalyzeBatchLinesBoundsConcurrencyAndRetainsCanceledLines(t *testing.T) {
	ids := []string{"abcdefghij0", "abcdefghij1", "abcdefghij2", "abcdefghij3", "abcdefghij4", "abcdefghij5", "abcdefghij6", "abcdefghij7"}
	lines := make([]string, len(ids))
	for index, id := range ids {
		lines[index] = "https://youtu.be/" + id
	}
	parsed, err := urlcheck.ParseBatch(strings.Join(lines, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	analyzer := &concurrencyBatchAnalyzer{}
	view, analyzed := analyzeBatchLines(context.Background(), analyzer, parsed)
	if view.Counts.Ready != len(ids) || len(analyzed) != len(ids) {
		t.Fatalf("analysis = counts %#v, ready %d", view.Counts, len(analyzed))
	}
	if got := analyzer.max.Load(); got < 2 || got > batchAnalysisConcurrency {
		t.Fatalf("maximum concurrent analyses = %d", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled, ready := analyzeBatchLines(ctx, analyzer, parsed)
	if len(ready) != 0 || canceled.Counts.AnalysisFailed != len(ids) {
		t.Fatalf("canceled analysis = counts %#v, ready %d", canceled.Counts, len(ready))
	}
	for index, item := range canceled.Items {
		if item.LineNumber != index+1 || item.Input != lines[index] || item.Status != BatchStatusAnalysisFailed || item.Message == "" {
			t.Fatalf("canceled item %d = %#v", index, item)
		}
	}
}

func TestBatchAnalysisTokenClaimIsExclusiveAndExpires(t *testing.T) {
	now := time.Now().UTC()
	app := &App{batchPlans: map[string]*cachedBatchAnalysis{
		"ready":   {token: "ready", expiresAt: now.Add(time.Minute), items: []analyzedBatchItem{{lineNumber: 1}}},
		"expired": {token: "expired", expiresAt: now.Add(-time.Minute)},
	}}
	claimed, err := app.claimBatchAnalysis("ready")
	if err != nil || len(claimed.items) != 1 {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if _, err := app.claimBatchAnalysis("ready"); err == nil {
		t.Fatal("concurrent replay was accepted")
	}
	app.releaseBatchAnalysis("ready")
	if _, err := app.claimBatchAnalysis("ready"); err != nil {
		t.Fatalf("released token could not be claimed: %v", err)
	}
	app.releaseBatchAnalysis("ready")
	app.consumeBatchAnalysis("ready")
	if _, err := app.claimBatchAnalysis("ready"); err == nil {
		t.Fatal("consumed token was accepted")
	}
	if _, err := app.claimBatchAnalysis("expired"); err == nil {
		t.Fatal("expired token was accepted")
	}
}

func TestBatchAnalysisCacheRemainsBoundedWhenPlansAreClaimed(t *testing.T) {
	now := time.Now().UTC()
	app := &App{batchPlans: make(map[string]*cachedBatchAnalysis, maxCachedBatchAnalyses)}
	for index := 0; index < maxCachedBatchAnalyses; index++ {
		token := fmt.Sprintf("token-%d", index)
		app.batchPlans[token] = &cachedBatchAnalysis{token: token, expiresAt: now.Add(time.Minute), inUse: true}
	}
	newPlan := &cachedBatchAnalysis{token: "new", expiresAt: now.Add(time.Minute)}
	if err := app.cacheBatchAnalysis(newPlan, now); err == nil {
		t.Fatal("cache exceeded its bound while every existing plan was claimed")
	}
	if len(app.batchPlans) != maxCachedBatchAnalyses || app.batchPlans["new"] != nil {
		t.Fatalf("cache size after rejected insert = %d", len(app.batchPlans))
	}
	app.batchPlans["token-0"].inUse = false
	if err := app.cacheBatchAnalysis(newPlan, now); err != nil {
		t.Fatal(err)
	}
	if len(app.batchPlans) != maxCachedBatchAnalyses || app.batchPlans["token-0"] != nil || app.batchPlans["new"] == nil {
		t.Fatalf("bounded cache did not evict the available plan: %#v", app.batchPlans)
	}
}

func TestPruneBatchAnalysesKeepsClaimedAndFuturePlans(t *testing.T) {
	now := time.Now().UTC()
	app := &App{batchPlans: map[string]*cachedBatchAnalysis{
		"expired":         {token: "expired", expiresAt: now.Add(-time.Second)},
		"claimed-expired": {token: "claimed-expired", expiresAt: now.Add(-time.Second), inUse: true},
		"future":          {token: "future", expiresAt: now.Add(time.Second)},
	}}
	app.pruneBatchAnalysesLocked(now)
	if app.batchPlans["expired"] != nil || app.batchPlans["claimed-expired"] == nil || app.batchPlans["future"] == nil {
		t.Fatalf("pruned plans = %#v", app.batchPlans)
	}
}
