// Package outputplan turns extractor format metadata into a small set of
// complete, user-facing files. It deliberately hides raw YouTube format IDs
// from the UI; callers retain the selector only inside the Go process.
package outputplan

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Kind identifies the final artifact the user receives.
type Kind string

const (
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
)

// Plan is one complete output choice. Selector and SourceFormatIDs are kept
// out of JSON so a renderer cannot invent or mutate engine selectors.
type Plan struct {
	ID                string `json:"id"`
	Kind              Kind   `json:"kind"`
	Label             string `json:"label"`
	Resolution        string `json:"resolution,omitempty"`
	Container         string `json:"container"`
	VideoCodec        string `json:"videoCodec,omitempty"`
	AudioCodec        string `json:"audioCodec,omitempty"`
	Width             int64  `json:"width,omitempty"`
	Height            int64  `json:"height,omitempty"`
	ApproxBytes       int64  `json:"approxBytes,omitempty"`
	SizeIsApproximate bool   `json:"sizeIsApproximate,omitempty"`
	RequiresFFmpeg    bool   `json:"requiresFfmpeg,omitempty"`
	AudioBitrateKbps  int    `json:"audioBitrateKbps,omitempty"`
	Recommended       bool   `json:"recommended,omitempty"`

	Selector        string   `json:"-"`
	SourceFormatIDs []string `json:"-"`
}

type mediaFormat struct {
	id, ext, vcodec, acodec string
	width, height           int64
	tbr, abr                float64
	filesize, approx        int64
}

var safeFormatID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Build returns one recommended video output per available resolution,
// original M4A/Opus audio choices, and MP3 conversions at 128/192/256 kbps.
// Raw private media URLs are never retained in a Plan.
func Build(info map[string]any, durationSeconds int64) []Plan {
	formats := decodeFormats(info["formats"])
	if len(formats) == 0 {
		return nil
	}

	audios := make([]mediaFormat, 0)
	videosByHeight := make(map[int64][]mediaFormat)
	for _, format := range formats {
		if hasAudio(format) && !hasVideo(format) {
			audios = append(audios, format)
		}
		if hasVideo(format) && format.height > 0 {
			videosByHeight[format.height] = append(videosByHeight[format.height], format)
		}
	}

	heights := make([]int64, 0, len(videosByHeight))
	for height := range videosByHeight {
		heights = append(heights, height)
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] > heights[j] })

	plans := make([]Plan, 0, len(heights)+5)
	for _, height := range heights {
		if plan, ok := bestVideoPlan(videosByHeight[height], audios, durationSeconds); ok {
			plans = append(plans, plan)
		}
	}
	if len(plans) > 0 {
		plans[0].Recommended = true
	}

	m4a, hasM4A := bestAudio(audios, "m4a")
	opus, hasOpus := bestAudio(audios, "opus")
	if hasM4A {
		plans = append(plans, originalAudioPlan(m4a, "M4A (Original)", durationSeconds))
	}
	if hasOpus {
		plans = append(plans, originalAudioPlan(opus, "Opus (Original)", durationSeconds))
	}

	source, hasSource := m4a, hasM4A
	if !hasSource {
		source, hasSource = opus, hasOpus
	}
	if hasSource {
		for _, bitrate := range []int{128, 192, 256} {
			plans = append(plans, mp3Plan(source, bitrate, durationSeconds))
		}
	}
	return plans
}

func decodeFormats(raw any) []mediaFormat {
	items, ok := raw.([]any)
	if !ok || len(items) > 4096 {
		return nil
	}
	formats := make([]mediaFormat, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := text(object, "format_id")
		if !safeFormatID.MatchString(id) {
			continue
		}
		format := mediaFormat{
			id:       id,
			ext:      strings.ToLower(text(object, "ext")),
			vcodec:   strings.ToLower(text(object, "vcodec")),
			acodec:   strings.ToLower(text(object, "acodec")),
			width:    integer(object, "width"),
			height:   integer(object, "height"),
			tbr:      number(object, "tbr"),
			abr:      number(object, "abr"),
			filesize: integer(object, "filesize"),
			approx:   firstInteger(object, "filesize_approx", "fs_approx"),
		}
		if !hasVideo(format) && !hasAudio(format) {
			continue
		}
		formats = append(formats, format)
	}
	return formats
}

func bestVideoPlan(videos, audios []mediaFormat, duration int64) (Plan, bool) {
	type candidate struct {
		video mediaFormat
		audio mediaFormat
		score int64
	}
	var best candidate
	found := false
	for _, video := range videos {
		current := candidate{video: video, score: videoScore(video)}
		if !hasAudio(video) {
			if audio, ok := compatibleAudio(video, audios); ok {
				current.audio = audio
				current.score += audioScore(audio)
			} else {
				continue
			}
		} else {
			current.score += 50_000
		}
		if !found || current.score > best.score {
			best, found = current, true
		}
	}
	if !found {
		return Plan{}, false
	}

	container := videoContainer(best.video)
	selector := best.video.id
	ids := []string{best.video.id}
	audioCodec := best.video.acodec
	requiresFFmpeg := false
	approxBytes, approximate := formatSize(best.video, duration)
	if best.audio.id != "" {
		selector += "+" + best.audio.id
		ids = append(ids, best.audio.id)
		audioCodec = best.audio.acodec
		requiresFFmpeg = true
		audioBytes, audioApprox := formatSize(best.audio, duration)
		approxBytes += audioBytes
		approximate = approximate || audioApprox
	}

	return Plan{
		ID:                fmt.Sprintf("video-%d-%s", best.video.height, container),
		Kind:              KindVideo,
		Label:             resolutionLabel(best.video.height),
		Resolution:        resolutionLabel(best.video.height),
		Container:         strings.ToUpper(container),
		VideoCodec:        displayVideoCodec(best.video.vcodec),
		AudioCodec:        displayAudioCodec(audioCodec),
		Width:             best.video.width,
		Height:            best.video.height,
		ApproxBytes:       approxBytes,
		SizeIsApproximate: approximate,
		RequiresFFmpeg:    requiresFFmpeg,
		Selector:          selector,
		SourceFormatIDs:   ids,
	}, true
}

func compatibleAudio(video mediaFormat, audios []mediaFormat) (mediaFormat, bool) {
	want := "m4a"
	if videoContainer(video) == "webm" {
		want = "opus"
	}
	if audio, ok := bestAudio(audios, want); ok {
		return audio, true
	}
	if want != "m4a" {
		if audio, ok := bestAudio(audios, "m4a"); ok {
			return audio, true
		}
	}
	if want != "opus" {
		if audio, ok := bestAudio(audios, "opus"); ok {
			return audio, true
		}
	}
	return mediaFormat{}, false
}

func bestAudio(audios []mediaFormat, family string) (mediaFormat, bool) {
	var best mediaFormat
	found := false
	for _, audio := range audios {
		matches := false
		switch family {
		case "m4a":
			matches = audio.ext == "m4a" || audio.ext == "mp4" || strings.Contains(audio.acodec, "aac") || strings.HasPrefix(audio.acodec, "mp4a")
		case "opus":
			matches = audio.ext == "webm" || strings.Contains(audio.acodec, "opus")
		}
		if !matches {
			continue
		}
		if !found || audioScore(audio) > audioScore(best) {
			best, found = audio, true
		}
	}
	return best, found
}

func originalAudioPlan(audio mediaFormat, label string, duration int64) Plan {
	container := audio.ext
	if strings.Contains(audio.acodec, "aac") || strings.HasPrefix(audio.acodec, "mp4a") {
		container = "m4a"
	}
	if strings.Contains(audio.acodec, "opus") {
		container = "webm"
	}
	bytes, approximate := formatSize(audio, duration)
	return Plan{
		ID:                "audio-" + container + "-original",
		Kind:              KindAudio,
		Label:             label,
		Container:         strings.ToUpper(container),
		AudioCodec:        displayAudioCodec(audio.acodec),
		ApproxBytes:       bytes,
		SizeIsApproximate: approximate,
		Selector:          audio.id,
		SourceFormatIDs:   []string{audio.id},
	}
}

func mp3Plan(source mediaFormat, bitrate int, duration int64) Plan {
	bytes := int64(0)
	if duration > 0 {
		bytes = duration * int64(bitrate) * 1000 / 8
	}
	return Plan{
		ID:                fmt.Sprintf("audio-mp3-%d", bitrate),
		Kind:              KindAudio,
		Label:             fmt.Sprintf("MP3 %d kbps", bitrate),
		Container:         "MP3",
		AudioCodec:        "MP3",
		ApproxBytes:       bytes,
		SizeIsApproximate: bytes > 0,
		RequiresFFmpeg:    true,
		AudioBitrateKbps:  bitrate,
		Selector:          source.id,
		SourceFormatIDs:   []string{source.id},
	}
}

func videoScore(format mediaFormat) int64 {
	score := int64(0)
	if format.ext == "mp4" || format.ext == "m4v" {
		score += 1_000_000
	}
	codec := displayVideoCodec(format.vcodec)
	switch codec {
	case "H.264":
		score += 500_000
	case "VP9":
		score += 300_000
	case "AV1":
		score += 200_000
	}
	score += int64(format.tbr * 10)
	return score
}

func audioScore(format mediaFormat) int64 {
	score := int64(format.abr*100 + format.tbr*10)
	if format.ext == "m4a" || format.ext == "mp4" {
		score += 10_000
	}
	return score
}

func videoContainer(format mediaFormat) string {
	if format.ext == "mp4" || format.ext == "m4v" || displayVideoCodec(format.vcodec) == "H.264" {
		return "mp4"
	}
	return "webm"
}

func formatSize(format mediaFormat, duration int64) (int64, bool) {
	if format.filesize > 0 {
		return format.filesize, false
	}
	if format.approx > 0 {
		return format.approx, true
	}
	bitrate := format.tbr
	if bitrate <= 0 {
		bitrate = format.abr
	}
	if duration > 0 && bitrate > 0 {
		return int64(float64(duration) * bitrate * 1000 / 8), true
	}
	return 0, false
}

func resolutionLabel(height int64) string {
	switch height {
	case 2160:
		return "4K"
	case 1440:
		return "1440p"
	case 1080:
		return "1080p"
	case 720:
		return "720p"
	case 480:
		return "480p"
	case 360:
		return "360p"
	default:
		return fmt.Sprintf("%dp", height)
	}
}

func displayVideoCodec(codec string) string {
	switch {
	case strings.HasPrefix(codec, "avc1"), strings.Contains(codec, "h264"):
		return "H.264"
	case strings.HasPrefix(codec, "vp09"), strings.Contains(codec, "vp9"):
		return "VP9"
	case strings.HasPrefix(codec, "av01"), strings.Contains(codec, "av1"):
		return "AV1"
	case codec == "", codec == "none":
		return ""
	default:
		return strings.ToUpper(codec)
	}
}

func displayAudioCodec(codec string) string {
	switch {
	case strings.HasPrefix(codec, "mp4a"), strings.Contains(codec, "aac"):
		return "AAC"
	case strings.Contains(codec, "opus"):
		return "Opus"
	case strings.Contains(codec, "mp3"):
		return "MP3"
	case codec == "", codec == "none":
		return ""
	default:
		return strings.ToUpper(codec)
	}
}

func hasVideo(format mediaFormat) bool { return format.vcodec != "" && format.vcodec != "none" }
func hasAudio(format mediaFormat) bool { return format.acodec != "" && format.acodec != "none" }

func text(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return strings.TrimSpace(value)
}

func integer(object map[string]any, key string) int64 {
	switch value := object[key].(type) {
	case float64:
		if value > 0 {
			return int64(value)
		}
	case int64:
		if value > 0 {
			return value
		}
	case int:
		if value > 0 {
			return int64(value)
		}
	}
	return 0
}

func firstInteger(object map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value := integer(object, key); value > 0 {
			return value
		}
	}
	return 0
}

func number(object map[string]any, key string) float64 {
	switch value := object[key].(type) {
	case float64:
		if value > 0 {
			return value
		}
	case int64:
		if value > 0 {
			return float64(value)
		}
	case int:
		if value > 0 {
			return float64(value)
		}
	}
	return 0
}
