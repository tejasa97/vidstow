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

func TestValidateAcceptsPublicChannelPages(t *testing.T) {
	ucid := "UCabcdefghijklmnopqrstuv"
	cases := []struct {
		name, input, wantURL, tab string
	}{
		{"handle", "https://www.youtube.com/@veritasium", "https://www.youtube.com/@veritasium/videos", ChannelTabVideos},
		{"handle videos", "https://youtube.com/@veritasium/videos?view=0", "https://www.youtube.com/@veritasium/videos", ChannelTabVideos},
		{"handle shorts", "https://m.youtube.com/@veritasium/shorts/", "https://www.youtube.com/@veritasium/shorts", ChannelTabShorts},
		{"channel id", "http://youtube.com/channel/" + ucid, "https://www.youtube.com/channel/" + ucid + "/videos", ChannelTabVideos},
		{"channel shorts", "https://www.youtube.com/channel/" + ucid + "/shorts", "https://www.youtube.com/channel/" + ucid + "/shorts", ChannelTabShorts},
		{"legacy c", "https://www.youtube.com/c/Veritasium", "https://www.youtube.com/c/Veritasium/videos", ChannelTabVideos},
		{"legacy user shorts", "https://www.youtube.com/user/1veritasium/shorts", "https://www.youtube.com/user/1veritasium/shorts", ChannelTabShorts},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Validate(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != KindChannel || got.URL != tc.wantURL || got.PlaylistURL != tc.wantURL || got.ChannelTab != tc.tab {
				t.Fatalf("got %#v, want url=%q tab=%q", got, tc.wantURL, tc.tab)
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
		{"search", "https://www.youtube.com/results?search_query=hello", ReasonSearch},
		{"channel search", "https://www.youtube.com/@veritasium/search?query=hello", ReasonSearch},
		{"channel streams", "https://www.youtube.com/@veritasium/streams", ReasonLive},
		{"channel live", "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/live", ReasonLive},
		{"channel playlists", "https://www.youtube.com/@veritasium/playlists", ReasonChannel},
		{"channel community", "https://www.youtube.com/@veritasium/community", ReasonChannel},
		{"invalid handle", "https://www.youtube.com/@ab", ReasonChannel},
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

func TestIdentityMatchesChannelHandlesAndUCIDs(t *testing.T) {
	handleURL := "https://www.youtube.com/@veritasium/videos"
	if got := CollectionIdentity(handleURL); got != "handle:@veritasium" {
		t.Fatalf("handle identity = %q", got)
	}
	if !IdentityMatches(handleURL, "handle:@veritasium") {
		t.Fatal("handle identity should match itself")
	}
	if !IdentityMatches(handleURL, "UCabcdefghijklmnopqrstuv") {
		t.Fatal("handle identity should accept a resolved UCID")
	}
	if IdentityMatches(handleURL, "PLunexpected") {
		t.Fatal("handle identity must not match a playlist id")
	}
	channelURL := "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/shorts"
	if !IdentityMatches(channelURL, "UCabcdefghijklmnopqrstuv") {
		t.Fatal("UCID channel should match extracted id")
	}
	if IdentityMatches(channelURL, "UCzzzzzzzzzzzzzzzzzzzzzz") {
		t.Fatal("UCID channel must not match a different channel")
	}
}
