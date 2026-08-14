package main

import (
	"strings"
	"testing"

	"github.com/tejasa97/vidstow/internal/jobs"
	"github.com/tejasa97/vidstow/internal/outputplan"
)

func TestValidatePlaylistPolicy(t *testing.T) {
	for _, test := range []struct {
		quality jobs.Quality
		bitrate int
		want    string
	}{
		{jobs.QualityBest, 0, "video:best"},
		{jobs.Quality1080p, 0, "video:1080p"},
		{jobs.QualityAudioOnly, 0, "audio:original"},
		{jobs.QualityAudioOnly, 192, "audio:mp3-192"},
	} {
		got, err := validatePlaylistPolicy(test.quality, test.bitrate)
		if err != nil || got != test.want {
			t.Fatalf("policy(%q, %d) = %q, %v", test.quality, test.bitrate, got, err)
		}
	}
	for _, test := range []struct {
		quality jobs.Quality
		bitrate int
	}{{jobs.Quality1080p, 128}, {jobs.QualityAudioOnly, 320}, {jobs.Quality("invented"), 0}} {
		if _, err := validatePlaylistPolicy(test.quality, test.bitrate); err == nil {
			t.Fatalf("policy(%q, %d) error = nil", test.quality, test.bitrate)
		}
	}
}

func TestChoosePlaylistPlanUsesPerChildAvailabilityAndCaps(t *testing.T) {
	plans := []outputplan.Plan{
		{ID: "4k", Kind: outputplan.KindVideo, Height: 2160, Available: true},
		{ID: "1080", Kind: outputplan.KindVideo, Height: 1080, Available: true},
		{ID: "720", Kind: outputplan.KindVideo, Height: 720, Available: true},
		{ID: "original", Kind: outputplan.KindAudio, Available: true},
		{ID: "mp3", Kind: outputplan.KindAudio, Available: true, RequiresFFmpeg: true, AudioBitrateKbps: 192},
	}
	for _, test := range []struct {
		quality jobs.Quality
		bitrate int
		want    string
	}{
		{jobs.QualityBest, 0, "4k"}, {jobs.Quality1080p, 0, "1080"},
		{jobs.Quality720p, 0, "720"}, {jobs.QualityAudioOnly, 0, "original"},
		{jobs.QualityAudioOnly, 192, "mp3"},
	} {
		plan, err := choosePlaylistPlan(plans, test.quality, test.bitrate)
		if err != nil || plan.ID != test.want {
			t.Fatalf("choose(%q, %d) = %#v, %v", test.quality, test.bitrate, plan, err)
		}
	}
	if _, err := choosePlaylistPlan(plans, jobs.Quality1440p, 0); err != nil {
		t.Fatalf("1440p policy should fall back to the best child plan under the cap: %v", err)
	}
	if _, err := choosePlaylistPlan(plans, jobs.QualityAudioOnly, 256); err == nil {
		t.Fatal("unavailable MP3 policy error = nil")
	}
}

func TestPlaylistSubfolderBoundsUntrustedIdentity(t *testing.T) {
	folder := playlistSubfolder(strings.Repeat("title", 50), strings.Repeat("x", 500))
	if len([]rune(folder)) > 140 || strings.ContainsAny(folder, `/\\`) {
		t.Fatalf("unsafe playlist folder = %q", folder)
	}
}
