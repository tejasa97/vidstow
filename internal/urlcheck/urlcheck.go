// Package urlcheck validates and classifies YouTube URLs at the desktop boundary.
package urlcheck

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

type Reason string

const (
	ReasonEmpty          Reason = "empty"
	ReasonNotYouTube     Reason = "not_youtube"
	ReasonChannel        Reason = "channel"
	ReasonPlaylist       Reason = "playlist"
	ReasonSearch         Reason = "search"
	ReasonLive           Reason = "live"
	ReasonInvalidScheme  Reason = "invalid_scheme"
	ReasonMalformed      Reason = "malformed"
	ReasonMissingVideoID Reason = "missing_video_id"
)

const (
	KindSingleVideo   = "single_video"
	KindPlaylist      = "playlist"
	KindVideoPlaylist = "video_playlist"
)

type Result struct {
	URL         string `json:"url"`
	VideoID     string `json:"videoId,omitempty"`
	PlaylistID  string `json:"playlistId,omitempty"`
	Kind        string `json:"kind"`
	VideoURL    string `json:"videoUrl,omitempty"`
	PlaylistURL string `json:"playlistUrl,omitempty"`
}

var (
	videoIDPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	playlistIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{2,128}$`)
	ErrRejected       = errors.New("rejected")
)

type Rejection struct {
	Reason  Reason
	Message string
}

func (r *Rejection) Error() string               { return r.Message }
func (r *Rejection) Is(target error) bool        { return target == ErrRejected }
func reject(reason Reason, message string) error { return &Rejection{reason, message} }

func Validate(raw string) (Result, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Result{}, reject(ReasonEmpty, "Paste a YouTube video or playlist link.")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return Result{}, reject(ReasonMalformed, "That link is not a valid web address.")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Result{}, reject(ReasonInvalidScheme, "Paste a link that starts with http:// or https://.")
	}
	host := strings.ToLower(parsed.Hostname())
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	if host != "youtube.com" && host != "youtu.be" && host != "youtube-nocookie.com" {
		return Result{}, reject(ReasonNotYouTube, "Only YouTube video and playlist links are supported.")
	}

	switch {
	case parsed.Path == "/results" || strings.HasPrefix(parsed.Path, "/search"):
		return Result{}, reject(ReasonSearch, "YouTube search pages are not supported. Paste a video or playlist link.")
	case strings.HasPrefix(parsed.Path, "/channel/") || strings.HasPrefix(parsed.Path, "/c/") || strings.HasPrefix(parsed.Path, "/user/") || strings.HasPrefix(parsed.Path, "/@"):
		return Result{}, reject(ReasonChannel, "YouTube channel pages are not supported. Paste a video or playlist link.")
	case parsed.Path == "/live" || strings.HasPrefix(parsed.Path, "/live/"):
		return Result{}, reject(ReasonLive, "YouTube live streams are not supported in this version.")
	}

	playlistID := parsed.Query().Get("list")
	if playlistID != "" && !playlistIDPattern.MatchString(playlistID) {
		return Result{}, reject(ReasonPlaylist, "That YouTube playlist link is not valid.")
	}
	videoID := extractVideoID(parsed, host)
	if videoID != "" && !videoIDPattern.MatchString(videoID) {
		return Result{}, reject(ReasonMissingVideoID, "We could not find a video in that link. Copy the link directly from YouTube.")
	}

	videoURL, playlistURL := "", ""
	if videoID != "" {
		videoURL = canonicalVideo(videoID)
	}
	if playlistID != "" {
		playlistURL = canonicalPlaylist(playlistID)
	}
	switch {
	case videoID != "" && playlistID != "":
		return Result{URL: trimmed, VideoID: videoID, PlaylistID: playlistID, Kind: KindVideoPlaylist, VideoURL: videoURL, PlaylistURL: playlistURL}, nil
	case playlistID != "" && (parsed.Path == "/playlist" || parsed.Path == "/watch"):
		return Result{URL: playlistURL, PlaylistID: playlistID, Kind: KindPlaylist, PlaylistURL: playlistURL}, nil
	case videoID != "":
		return Result{URL: videoURL, VideoID: videoID, Kind: KindSingleVideo, VideoURL: videoURL}, nil
	default:
		return Result{}, reject(ReasonMissingVideoID, "We could not find a video or playlist in that link. Copy the link directly from YouTube.")
	}
}

func IsRejected(err error) bool { return errors.Is(err, ErrRejected) }
func ReasonOf(err error) Reason {
	if err == nil {
		return ""
	}
	var rejection *Rejection
	if errors.As(err, &rejection) && isReason(string(rejection.Reason)) {
		return rejection.Reason
	}
	return ReasonMalformed
}
func isReason(s string) bool {
	switch Reason(s) {
	case ReasonEmpty, ReasonNotYouTube, ReasonChannel, ReasonPlaylist, ReasonSearch, ReasonLive, ReasonInvalidScheme, ReasonMalformed, ReasonMissingVideoID:
		return true
	}
	return false
}
func extractVideoID(parsed *url.URL, host string) string {
	if host == "youtu.be" {
		return strings.SplitN(strings.TrimPrefix(parsed.Path, "/"), "/", 2)[0]
	}
	if parsed.Path == "/watch" {
		return parsed.Query().Get("v")
	}
	if strings.HasPrefix(parsed.Path, "/embed/") || strings.HasPrefix(parsed.Path, "/v/") {
		parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
		if len(parts) >= 2 {
			return parts[1]
		}
	}
	if strings.HasPrefix(parsed.Path, "/shorts/") {
		parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
		if len(parts) >= 2 {
			return parts[1]
		}
	}
	return ""
}
func canonicalVideo(id string) string {
	return "https://www.youtube.com/watch?v=" + url.QueryEscape(id)
}
func canonicalPlaylist(id string) string {
	return "https://www.youtube.com/playlist?list=" + url.QueryEscape(id)
}
