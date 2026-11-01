package commands

import (
	"strings"
	"testing"

	"github.com/dkarlovi/mediator/config"
	"github.com/dkarlovi/mediator/media"
)

func TestCompatibility(t *testing.T) {
	cfg, err := config.Parse([]byte(`
container: {preferred: [mkv, mp4]}
video: {preferred: [h264, hevc]}
audio: {preferred: [aac, mp3]}
subtitles: {preferred: [srt, pgs]}
`))
	if err != nil {
		t.Fatal(err)
	}

	full := media.NewToolchain("9.0.2", []string{"libx264", "libx265", "aac", "libmp3lame", "srt"}, []string{"matroska", "mp4"})
	if _, issues := compatibility(cfg, full); len(issues) != 0 {
		t.Errorf("full build: %v", issues)
	}

	stripped := media.NewToolchain("6.1.2", []string{"aac", "srt"}, []string{"matroska"})
	sections, issues := compatibility(cfg, stripped)
	var fatal, warn []string
	for _, is := range issues {
		if is.fatal {
			fatal = append(fatal, is.text)
		} else {
			warn = append(warn, is.text)
		}
	}
	if len(fatal) != 1 || !strings.Contains(fatal[0], "cannot write mp4") {
		t.Errorf("fatal = %q", fatal)
	}
	for _, want := range []string{"older than mediator is tested", "no libx264", "no libx265", "no libmp3lame", "video: this ffmpeg can produce none"} {
		if !strings.Contains(strings.Join(warn, "\n"), want) {
			t.Errorf("warnings missing %q: %q", want, warn)
		}
	}
	// Bitmap subtitles are accepted, never produced: not an issue.
	if subs := sections[len(sections)-1]; !strings.Contains(strings.Join(subs.entries, " "), "hdmv_pgs_subtitle (copy only)") {
		t.Errorf("subtitles: %v", subs.entries)
	}
}
