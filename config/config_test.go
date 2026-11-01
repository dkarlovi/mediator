package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseNormalizes(t *testing.T) {
	c, err := Parse([]byte(`
container: {preferred: [MKV, .mp4]}
video: {preferred: [H265, x264]}
subtitles: {preferred: [srt, pgs]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mkv", "mp4"}; !reflect.DeepEqual(c.Container.Preferred, want) {
		t.Errorf("container = %v", c.Container.Preferred)
	}
	if want := []string{"hevc", "h264"}; !reflect.DeepEqual(c.Video.Preferred, want) {
		t.Errorf("video = %v", c.Video.Preferred)
	}
	if want := []string{"subrip", "hdmv_pgs_subtitle"}; !reflect.DeepEqual(c.Subtitles.Preferred, want) {
		t.Errorf("subtitles = %v", c.Subtitles.Preferred)
	}
	if c.Original != OriginalKeep || c.FFmpeg != "ffmpeg" || c.FFprobe != "ffprobe" {
		t.Errorf("defaults not applied: %+v", c)
	}
}

func TestParseErrors(t *testing.T) {
	for yaml, want := range map[string]string{
		`container: {preferred: [flv]}`:          "unknown container",
		`audio: {preferred: [truehd]}`:           "cannot encode",
		`audio: {convert: {bitrate: loud}}`:      "not a bitrate",
		`original: shred`:                        "unknown policy",
		`audio: {prefered: [aac]}`:               "field prefered not found",
		``:                                       "", // empty config is valid, does nothing
		`audio: {convert: {channels: -1}}`:       "must be positive",
		`video: {preferred: [mpeg2video, h264]}`: "cannot encode",
	} {
		_, err := Parse([]byte(yaml))
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", yaml, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: error = %v, want %q", yaml, err, want)
		}
	}
}
