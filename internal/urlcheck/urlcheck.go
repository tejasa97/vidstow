// Package urlcheck validates and classifies YouTube URLs at the desktop boundary.
package urlcheck

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
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
	KindChannel       = "channel"

	ChannelTabVideos = "videos"
	ChannelTabShorts = "shorts"

	maxChannelAliasBytes = 100
)

type Result struct {
	URL         string `json:"url"`
	VideoID     string `json:"videoId,omitempty"`
	PlaylistID  string `json:"playlistId,omitempty"`
	Kind        string `json:"kind"`
	VideoURL    string `json:"videoUrl,omitempty"`
	PlaylistURL string `json:"playlistUrl,omitempty"`
	ChannelTab  string `json:"channelTab,omitempty"`
}

var (
	videoIDPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	playlistIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{2,128}$`)
	channelIDPattern  = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
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
		return Result{}, reject(ReasonEmpty, "Paste a YouTube video, playlist, or channel link.")
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
		return Result{}, reject(ReasonNotYouTube, "Only YouTube video, playlist, and channel links are supported.")
	}

	switch {
	case parsed.Path == "/results" || strings.HasPrefix(parsed.Path, "/search"):
		return Result{}, reject(ReasonSearch, "YouTube search pages are not supported. Paste a video, playlist, or channel link.")
	case isChannelPath(parsed.Path):
		return classifyChannel(parsed)
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
		return Result{}, reject(ReasonMissingVideoID, "We could not find a video, playlist, or channel in that link. Copy the link directly from YouTube.")
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

func isChannelPath(path string) bool {
	return strings.HasPrefix(path, "/channel/") || strings.HasPrefix(path, "/c/") || strings.HasPrefix(path, "/user/") || strings.HasPrefix(path, "/@")
}

func classifyChannel(parsed *url.URL) (Result, error) {
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return Result{}, reject(ReasonChannel, "That YouTube channel link is not valid.")
	}
	identityPath, tab, err := channelRoute(parts)
	if err != nil {
		return Result{}, err
	}
	canonical := (&url.URL{Scheme: "https", Host: "www.youtube.com", Path: identityPath + "/" + tab}).String()
	return Result{URL: canonical, Kind: KindChannel, PlaylistURL: canonical, ChannelTab: tab}, nil
}

func channelRoute(parts []string) (identityPath, tab string, err error) {
	switch parts[0] {
	case "channel":
		if len(parts) < 2 || !channelIDPattern.MatchString(parts[1]) {
			return "", "", reject(ReasonChannel, "That YouTube channel link is not valid.")
		}
		identityPath = "/channel/" + parts[1]
		tab, err = admittedChannelTab(parts[2:])
		return identityPath, tab, err
	case "c", "user":
		if len(parts) < 2 || !validChannelAlias(parts[1]) {
			return "", "", reject(ReasonChannel, "That YouTube channel link is not valid.")
		}
		identityPath = "/" + parts[0] + "/" + parts[1]
		tab, err = admittedChannelTab(parts[2:])
		return identityPath, tab, err
	default:
		if !strings.HasPrefix(parts[0], "@") || !validChannelHandle(parts[0]) {
			return "", "", reject(ReasonChannel, "That YouTube channel link is not valid.")
		}
		identityPath = "/" + parts[0]
		tab, err = admittedChannelTab(parts[1:])
		return identityPath, tab, err
	}
}

func admittedChannelTab(rest []string) (string, error) {
	if len(rest) == 0 || rest[0] == "" {
		return ChannelTabVideos, nil
	}
	if len(rest) != 1 {
		return "", reject(ReasonChannel, "That YouTube channel tab is not supported. Paste the channel, Videos, or Shorts page.")
	}
	switch rest[0] {
	case ChannelTabVideos, ChannelTabShorts:
		return rest[0], nil
	case "streams", "live":
		return "", reject(ReasonLive, "YouTube live streams are not supported in this version.")
	case "search":
		return "", reject(ReasonSearch, "YouTube search pages are not supported. Paste a video, playlist, or channel link.")
	default:
		return "", reject(ReasonChannel, "That YouTube channel tab is not supported. Paste the channel, Videos, or Shorts page.")
	}
}

func validChannelHandle(handle string) bool {
	if !utf8.ValidString(handle) || !strings.HasPrefix(handle, "@") {
		return false
	}
	value := strings.TrimPrefix(handle, "@")
	count := utf8.RuneCountInString(value)
	if count < 3 || count > 30 {
		return false
	}
	for _, character := range value {
		if character == '_' || character == '.' || character == '-' || unicode.IsLetter(character) || unicode.IsNumber(character) {
			continue
		}
		return false
	}
	return true
}

func validChannelAlias(alias string) bool {
	if alias == "" || alias == "." || alias == ".." || len(alias) > maxChannelAliasBytes || !utf8.ValidString(alias) || strings.ContainsAny(alias, `/\`) {
		return false
	}
	for _, character := range alias {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// CollectionIdentity returns the durable playlist or channel identity encoded
// in a canonical collection URL. Channel handles and aliases may later resolve
// to a UCID during analysis.
func CollectionIdentity(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if list := parsed.Query().Get("list"); playlistIDPattern.MatchString(list) {
		return list
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	switch parts[0] {
	case "channel":
		if len(parts) >= 2 && channelIDPattern.MatchString(parts[1]) {
			return parts[1]
		}
	case "c", "user":
		if len(parts) >= 2 && validChannelAlias(parts[1]) {
			return parts[0] + ":" + parts[1]
		}
	default:
		if validChannelHandle(parts[0]) {
			return "handle:" + parts[0]
		}
	}
	return ""
}

// IdentityMatches reports whether analyzed metadata belongs to the collection URL.
func IdentityMatches(rawURL, summaryID string) bool {
	expected := CollectionIdentity(rawURL)
	if expected == "" || strings.TrimSpace(summaryID) == "" {
		return false
	}
	if expected == summaryID {
		return true
	}
	return isUnresolvedChannelIdentity(expected) && channelIDPattern.MatchString(summaryID)
}

func isUnresolvedChannelIdentity(id string) bool {
	return strings.HasPrefix(id, "handle:") || strings.HasPrefix(id, "c:") || strings.HasPrefix(id, "user:")
}

func IsCollection(kind string) bool {
	return kind == KindPlaylist || kind == KindChannel
}
