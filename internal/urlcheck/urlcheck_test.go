package urlcheck

import (
	"strings"
	"testing"
)

func TestValidateAcceptsVideosAndPlaylists(t *testing.T) {
	cases := []struct{ name, input, kind, videoID, playlistID string }{
		{"youtube watch", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", KindSingleVideo, "dQw4w9WgXcQ", ""},
		{"youtu.be", "https://youtu.be/dQw4w9WgXcQ", KindSingleVideo, "dQw4w9WgXcQ", ""},
		{"embed", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ", KindSingleVideo, "dQw4w9WgXcQ", ""},
		{"shorts", "https://www.youtube.com/shorts/dQw4w9WgXcQ", KindSingleVideo, "dQw4w9WgXcQ", ""},
		{"shorts mobile", "https://m.youtube.com/shorts/dQw4w9WgXcQ", KindSingleVideo, "dQw4w9WgXcQ", ""},
		{"shorts share", "https://www.youtube.com/shorts/dQw4w9WgXcQ?si=tracking", KindSingleVideo, "dQw4w9WgXcQ", ""},
		{"shorts slash", "https://www.youtube.com/shorts/dQw4w9WgXcQ/", KindSingleVideo, "dQw4w9WgXcQ", ""},
		{"playlist", "https://www.youtube.com/playlist?list=PL1234567890", KindPlaylist, "", "PL1234567890"},
		{"mixed", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PL1234567890", KindVideoPlaylist, "dQw4w9WgXcQ", "PL1234567890"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Validate(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != tc.kind || got.VideoID != tc.videoID || got.PlaylistID != tc.playlistID {
				t.Fatalf("got %#v", got)
			}
			if tc.kind == KindVideoPlaylist && (got.VideoURL == "" || got.PlaylistURL == "") {
				t.Fatalf("mixed result lacks choices: %#v", got)
			}
			if tc.videoID != "" && got.VideoURL != "https://www.youtube.com/watch?v="+tc.videoID {
				t.Fatalf("canonical video URL = %q", got.VideoURL)
			}
		})
	}
}

func TestValidateRejectsUnsupportedRoutes(t *testing.T) {
	cases := []struct {
		name, input string
		reason      Reason
	}{
		{"empty", "", ReasonEmpty}, {"not youtube", "https://example.com/watch?v=abcd", ReasonNotYouTube},
		{"search", "https://www.youtube.com/results?search_query=hello", ReasonSearch}, {"channel", "https://www.youtube.com/@veritasium", ReasonChannel},
		{"live", "https://www.youtube.com/live/abcdefghijk", ReasonLive},
		{"shorts missing id", "https://www.youtube.com/shorts/", ReasonMissingVideoID},
		{"missing", "https://www.youtube.com/", ReasonMissingVideoID}, {"ftp", "ftp://youtube.com/watch?v=abc", ReasonInvalidScheme}, {"malformed", "ht!tp://nope", ReasonMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Validate(tc.input)
			if err == nil || !IsRejected(err) || ReasonOf(err) != tc.reason {
				t.Fatalf("error=%v reason=%q", err, ReasonOf(err))
			}
			if strings.Contains(strings.ToLower(err.Error()), "rejected") || strings.Contains(err.Error(), "not_youtube") {
				t.Fatalf("internal details: %q", err)
			}
		})
	}
}
