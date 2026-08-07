package outputplan

import "testing"

func TestBuildCuratesCompleteOutputs(t *testing.T) {
	info := map[string]any{
		"formats": []any{
			format("137", "mp4", "avc1.640028", "none", 1920, 1080, 4500, 0, 0),
			format("248", "webm", "vp9", "none", 1920, 1080, 2500, 0, 0),
			format("22", "mp4", "avc1.64001F", "mp4a.40.2", 1280, 720, 1800, 0, 18_000_000),
			format("140", "m4a", "none", "mp4a.40.2", 0, 0, 129, 129, 2_000_000),
			format("251", "webm", "none", "opus", 0, 0, 160, 160, 2_500_000),
		},
	}
	plans := Build(info, 120)
	if len(plans) != 7 {
		t.Fatalf("len(Build()) = %d; want 7: %#v", len(plans), plans)
	}

	video1080 := plans[0]
	if video1080.ID != "video-1080-mp4" || video1080.Selector != "137+140" ||
		video1080.VideoCodec != "H.264" || video1080.AudioCodec != "AAC" ||
		!video1080.RequiresFFmpeg || !video1080.Recommended {
		t.Fatalf("1080p plan = %#v", video1080)
	}
	video720 := plans[1]
	if video720.Selector != "22" || video720.RequiresFFmpeg || video720.Container != "MP4" {
		t.Fatalf("720p plan = %#v", video720)
	}
	if plans[2].ID != "audio-m4a-original" || plans[3].ID != "audio-webm-original" {
		t.Fatalf("original audio plans = %#v, %#v", plans[2], plans[3])
	}
	for index, bitrate := range []int{128, 192, 256} {
		plan := plans[index+4]
		if plan.ID != "audio-mp3-"+itoa(bitrate) || plan.AudioBitrateKbps != bitrate ||
			plan.Selector != "140" || !plan.RequiresFFmpeg {
			t.Fatalf("mp3 plan %d = %#v", bitrate, plan)
		}
	}
}

func TestBuildFallsBackToWebM(t *testing.T) {
	info := map[string]any{"formats": []any{
		format("303", "webm", "vp9", "none", 1920, 1080, 2400, 0, 0),
		format("251", "webm", "none", "opus", 0, 0, 160, 160, 0),
	}}
	plans := Build(info, 60)
	if len(plans) < 2 {
		t.Fatalf("plans = %#v", plans)
	}
	if plans[0].Container != "WEBM" || plans[0].Selector != "303+251" || plans[0].AudioCodec != "Opus" {
		t.Fatalf("video fallback = %#v", plans[0])
	}
}

func TestBuildRejectsUnsafeAndUnboundedFormats(t *testing.T) {
	unsafe := map[string]any{"formats": []any{format("video+audio", "mp4", "avc1", "aac", 1280, 720, 1000, 0, 0)}}
	if plans := Build(unsafe, 10); len(plans) != 0 {
		t.Fatalf("unsafe format produced plans: %#v", plans)
	}
	many := make([]any, 4097)
	if plans := Build(map[string]any{"formats": many}, 10); len(plans) != 0 {
		t.Fatalf("oversize format set produced plans: %#v", plans)
	}
}

func TestPlanPrivateSelectorsAreNotJSONFields(t *testing.T) {
	plan := Plan{ID: "video-720-mp4", Selector: "22", SourceFormatIDs: []string{"22"}}
	// Compile-time contract: private fields deliberately carry json:"-".
	if plan.Selector == "" || len(plan.SourceFormatIDs) != 1 {
		t.Fatal("private selector setup failed")
	}
}

func format(id, ext, video, audio string, width, height int64, tbr, abr float64, size int64) map[string]any {
	return map[string]any{
		"format_id": id, "ext": ext, "vcodec": video, "acodec": audio,
		"width": float64(width), "height": float64(height), "tbr": tbr, "abr": abr,
		"filesize": float64(size),
	}
}

func itoa(value int) string {
	switch value {
	case 128:
		return "128"
	case 192:
		return "192"
	case 256:
		return "256"
	default:
		return "unknown"
	}
}
