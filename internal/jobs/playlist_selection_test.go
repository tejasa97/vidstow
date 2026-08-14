package jobs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/engine"
)

func TestAnalyzeForAdmissionReturnsPrivatePlansWithoutDependingOnCache(t *testing.T) {
	manager := New(nil, nil)
	t.Cleanup(func() { _ = manager.Close() })
	manager.runAnalyze = func(context.Context, engine.Request) (engine.Result, error) {
		return engine.Result{InfoJSON: []byte(`{"id":"fixture0001","title":"Fixture","formats":[{"format_id":"v1","ext":"mp4","vcodec":"avc1","acodec":"none","height":1080,"width":1920},{"format_id":"a1","ext":"m4a","vcodec":"none","acodec":"mp4a","abr":128}]}`)}, nil
	}
	summary, plans, err := manager.AnalyzeForAdmission(context.Background(), "https://www.youtube.com/watch?v=fixture0001")
	if err != nil || summary.VideoID != "fixture0001" || len(plans) == 0 || plans[0].Selector == "" {
		t.Fatalf("summary=%#v plans=%#v error=%v", summary, plans, err)
	}
	if _, cached := manager.planCache["fixture0001"]; cached {
		t.Fatal("private collection analysis unexpectedly depended on the single-video plan cache")
	}
}

func TestResolvePlaylistSelectionUsesOnlyTrustedAvailableEntries(t *testing.T) {
	manager := New(nil, nil)
	t.Cleanup(func() { _ = manager.Close() })
	manager.playlistCache["PLfixture"] = cachedPlaylist{
		expiresAt: time.Now().Add(time.Minute),
		summary: PlaylistSummary{ID: "PLfixture", Title: "Playlist", Entries: []PlaylistEntrySummary{
			{Index: 1, VideoID: "fixture0001", URL: "https://www.youtube.com/watch?v=fixture0001", Title: "One", Available: true},
			{Index: 2, VideoID: "fixture0002", Title: "Unavailable", Available: false},
			{Index: 3, VideoID: "fixture0003", URL: "https://www.youtube.com/watch?v=fixture0003", Title: "Three", Available: true},
		}},
	}

	summary, entries, err := manager.ResolvePlaylistSelection("PLfixture", []int{3, 1})
	if err != nil {
		t.Fatal(err)
	}
	if summary.ID != "PLfixture" || len(entries) != 2 || entries[0].Index != 1 || entries[1].Index != 3 {
		t.Fatalf("selection = %#v, summary = %#v", entries, summary)
	}
	entries[0].Title = "mutated"
	if manager.playlistCache["PLfixture"].summary.Entries[0].Title != "One" {
		t.Fatal("returned selection aliases trusted cache")
	}

	for name, selected := range map[string][]int{
		"duplicate": {1, 1}, "unavailable": {2}, "unknown": {4}, "invalid": {0},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := manager.ResolvePlaylistSelection("PLfixture", selected); err == nil {
				t.Fatal("error = nil")
			}
		})
	}
}

func TestResolvePlaylistSelectionRejectsAmbiguousPreviewIndexes(t *testing.T) {
	manager := New(nil, nil)
	t.Cleanup(func() { _ = manager.Close() })
	manager.playlistCache["duplicate"] = cachedPlaylist{
		expiresAt: time.Now().Add(time.Minute),
		summary: PlaylistSummary{ID: "duplicate", Entries: []PlaylistEntrySummary{
			{Index: 1, VideoID: "fixture0001", URL: "https://www.youtube.com/watch?v=fixture0001", Available: true},
			{Index: 1, VideoID: "fixture0002", URL: "https://www.youtube.com/watch?v=fixture0002", Available: true},
		}},
	}
	if _, _, err := manager.ResolvePlaylistSelection("duplicate", []int{1}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate preview error = %v", err)
	}
}

func TestResolvePlaylistSelectionExpiresAndBoundsRequests(t *testing.T) {
	manager := New(nil, nil)
	t.Cleanup(func() { _ = manager.Close() })
	manager.playlistCache["expired"] = cachedPlaylist{expiresAt: time.Now().Add(-time.Second), summary: PlaylistSummary{ID: "expired"}}
	if _, _, err := manager.ResolvePlaylistSelection("expired", []int{1}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired error = %v", err)
	}
	if _, exists := manager.playlistCache["expired"]; exists {
		t.Fatal("expired preview was not pruned")
	}
	if _, _, err := manager.ResolvePlaylistSelection("PLfixture", make([]int, MaxPlaylistEntries+1)); err == nil {
		t.Fatal("oversized selection error = nil")
	}
}
