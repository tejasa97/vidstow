package jobmodel

import "testing"

func TestOutputOptionsValidateAcceptsContractShapes(t *testing.T) {
	valid := []OutputOptions{
		{},
		{SubtitleMode: SubtitleModeSidecar, SubtitleFormat: "srt", SubtitleLanguages: []string{"en"}},
		{SubtitleMode: SubtitleModeEmbed, SubtitleAutoCaptions: true, SubtitleLanguages: []string{"en", "pt-BR"}},
		{SubtitleMode: SubtitleModeSidecar, SubtitleFormat: "vtt"},
		{EmbedMetadata: true, EmbedChapters: true, EmbedThumbnail: true},
	}
	for _, options := range valid {
		if err := options.Validate(); err != nil {
			t.Fatalf("Validate(%#v) = %v; want nil", options, err)
		}
	}
}

func TestOutputOptionsValidateRejectsOutOfContractShapes(t *testing.T) {
	invalid := []OutputOptions{
		{SubtitleMode: "banana"},
		{SubtitleMode: SubtitleModeSidecar, SubtitleFormat: "ass"},
		{SubtitleMode: SubtitleModeEmbed, SubtitleLanguages: []string{"en", ""}},
		{SubtitleMode: SubtitleModeEmbed, SubtitleLanguages: []string{"en.*"}},
		{SubtitleMode: SubtitleModeEmbed, SubtitleLanguages: []string{"all,-en"}},
		{SubtitleLanguages: make([]string, maxSubtitleLanguages+1)},
	}
	for _, options := range invalid {
		if err := options.Validate(); err == nil {
			t.Fatalf("Validate(%#v) = nil; want error", options)
		}
	}
}

func TestOutputOptionsRequiresFFmpeg(t *testing.T) {
	cases := []struct {
		options OutputOptions
		want    bool
	}{
		{OutputOptions{}, false},
		{OutputOptions{SubtitleMode: SubtitleModeSidecar}, false},
		{OutputOptions{SubtitleMode: SubtitleModeSidecar, SubtitleFormat: "srt"}, true},
		{OutputOptions{SubtitleMode: SubtitleModeEmbed}, true},
		{OutputOptions{EmbedMetadata: true}, true},
		{OutputOptions{EmbedThumbnail: true}, true},
		{OutputOptions{EmbedChapters: true}, true},
	}
	for _, tc := range cases {
		if got := tc.options.RequiresFFmpeg(); got != tc.want {
			t.Fatalf("RequiresFFmpeg(%#v) = %v; want %v", tc.options, got, tc.want)
		}
	}
}

func TestOutputOptionsEqualAndClone(t *testing.T) {
	base := OutputOptions{SubtitleMode: SubtitleModeEmbed, SubtitleLanguages: []string{"en", "de"}}
	if !base.Equal(base.Clone()) {
		t.Fatal("clone must compare equal")
	}
	if base.Clone().IsZero() {
		t.Fatal("non-zero options must not report zero")
	}
	drifted := base
	drifted.SubtitleLanguages = []string{"de", "en"}
	if base.Equal(drifted) {
		t.Fatal("language order must participate in equality")
	}
	if !(OutputOptions{}).IsZero() {
		t.Fatal("zero value must report zero")
	}
}

func TestOutputOptionsNote(t *testing.T) {
	cases := []struct {
		options OutputOptions
		want    string
	}{
		{OutputOptions{}, ""},
		{OutputOptions{SubtitleMode: SubtitleModeSidecar, SubtitleLanguages: []string{"en"}}, "subtitles (en)"},
		{OutputOptions{SubtitleMode: SubtitleModeEmbed}, "embedded subtitles"},
		{OutputOptions{EmbedMetadata: true, EmbedThumbnail: true, EmbedChapters: true}, "embedded metadata, thumbnail, chapters"},
		{OutputOptions{SubtitleMode: SubtitleModeSidecar, SubtitleLanguages: []string{"en"}, EmbedMetadata: true}, "subtitles (en) · embedded metadata"},
	}
	for _, tc := range cases {
		if got := tc.options.Note(); got != tc.want {
			t.Fatalf("Note(%#v) = %q; want %q", tc.options, got, tc.want)
		}
	}
}
