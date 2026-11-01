package plan

import (
	"strings"
	"testing"

	"github.com/dkarlovi/mediator/config"
	"github.com/dkarlovi/mediator/media"
)

func cfg(t *testing.T, yaml string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const plexConfig = `
container: {preferred: [mkv, mp4]}
video: {preferred: [h264, hevc]}
audio:
  preferred: [aac, mp3]
  convert: {bitrate: 192k, channels: 2}
subtitles: {preferred: [srt, ass]}
`

func video(i int, codec string) media.Stream {
	return media.Stream{Index: i, CodecType: "video", CodecName: codec, Width: 1920, Height: 1080}
}
func audio(i int, codec string, ch int) media.Stream {
	return media.Stream{Index: i, CodecType: "audio", CodecName: codec, Channels: ch}
}
func sub(i int, codec string) media.Stream {
	return media.Stream{Index: i, CodecType: "subtitle", CodecName: codec}
}

func actions(p *Plan) string {
	var parts []string
	for _, s := range p.Streams {
		a := string(s.Action)
		if s.Action == Convert {
			a += ":" + s.Codec
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

func TestBuild(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		streams   []media.Stream
		wantDst   string
		want      string
		needsWork bool
	}{
		{
			name:    "already compliant",
			path:    "/x/a.mkv",
			streams: []media.Stream{video(0, "hevc"), audio(1, "aac", 6), sub(2, "subrip")},
			wantDst: "mkv", want: "copy copy copy",
		},
		{
			name:    "mp3 is acceptable, not converted",
			path:    "/x/a.mkv",
			streams: []media.Stream{video(0, "h264"), audio(1, "mp3", 2)},
			wantDst: "mkv", want: "copy copy",
		},
		{
			name:    "truehd and eac3 become aac, aac stays",
			path:    "/x/a.mkv",
			streams: []media.Stream{video(0, "h264"), audio(1, "truehd", 8), audio(2, "eac3", 6), audio(3, "aac", 2)},
			wantDst: "mkv", want: "copy convert:aac convert:aac copy", needsWork: true,
		},
		{
			name:    "avi is remuxed into the first preferred container",
			path:    "/x/a.avi",
			streams: []media.Stream{video(0, "h264"), audio(1, "mp3", 2)},
			wantDst: "mkv", want: "copy copy", needsWork: true,
		},
		{
			name:    "bitmap subtitles cannot be converted, kept in mkv",
			path:    "/x/a.mkv",
			streams: []media.Stream{video(0, "h264"), sub(1, "hdmv_pgs_subtitle")},
			wantDst: "mkv", want: "copy copy",
		},
		{
			name:    "attachments are kept in mkv",
			path:    "/x/a.mkv",
			streams: []media.Stream{video(0, "h264"), {Index: 1, CodecType: "attachment", CodecName: "ttf"}},
			wantDst: "mkv", want: "copy copy",
		},
	}
	c := cfg(t, plexConfig)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Build(&media.Info{Path: tt.path, Streams: tt.streams}, c)
			if err != nil {
				t.Fatal(err)
			}
			if p.Dst.Name != tt.wantDst {
				t.Errorf("dst = %s, want %s", p.Dst.Name, tt.wantDst)
			}
			if got := actions(p); got != tt.want {
				t.Errorf("actions = %q, want %q", got, tt.want)
			}
			if p.NeedsWork() != tt.needsWork {
				t.Errorf("NeedsWork = %v, want %v", p.NeedsWork(), tt.needsWork)
			}
		})
	}
}

func TestMP4TargetFitsStreamsToContainer(t *testing.T) {
	c := cfg(t, `
container: {preferred: [mp4]}
audio: {preferred: [aac]}
subtitles: {preferred: [srt]}
`)
	p, err := Build(&media.Info{Path: "/x/a.mkv", Streams: []media.Stream{
		video(0, "hevc"),
		audio(1, "dts", 6),
		sub(2, "subrip"),            // srt can't go in mp4, but text converts to mov_text
		sub(3, "hdmv_pgs_subtitle"), // bitmap: nowhere to go
		{Index: 4, CodecType: "attachment", CodecName: "ttf"},
	}}, c)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := actions(p), "copy convert:aac convert:mov_text drop drop"; got != want {
		t.Errorf("actions = %q, want %q", got, want)
	}
	if p.Dst.Name != "mp4" {
		t.Errorf("dst = %s", p.Dst.Name)
	}
}

func TestAudioChannelsAndBitrate(t *testing.T) {
	c := cfg(t, `audio: {preferred: [aac], convert: {channels: 2}}`)
	p, err := Build(&media.Info{Path: "/x/a.mkv", Streams: []media.Stream{audio(0, "truehd", 8), audio(1, "ac3", 1)}}, c)
	if err != nil {
		t.Fatal(err)
	}
	down, mono := p.Streams[0], p.Streams[1]
	if down.Channels != 2 || !hasOpt(down, "ac", "2") || !hasOpt(down, "b", "128k") {
		t.Errorf("downmix: channels=%d opts=%v", down.Channels, down.Options)
	}
	if mono.Channels != 1 || hasOpt(mono, "ac", "") || !hasOpt(mono, "b", "64k") {
		t.Errorf("mono must not be upmixed: channels=%d opts=%v", mono.Channels, mono.Options)
	}
}

func TestKeepSurroundWhenChannelsUnset(t *testing.T) {
	c := cfg(t, `audio: {preferred: [aac]}`)
	p, err := Build(&media.Info{Path: "/x/a.mkv", Streams: []media.Stream{audio(0, "eac3", 6)}}, c)
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Streams[0]; s.Channels != 6 || hasOpt(s, "ac", "") || !hasOpt(s, "b", "384k") {
		t.Errorf("channels=%d opts=%v", s.Channels, s.Options)
	}
}

func TestEmptyPreferenceLeavesStreamsAlone(t *testing.T) {
	c := cfg(t, `audio: {preferred: [aac]}`)
	p, err := Build(&media.Info{Path: "/x/a.mkv", Streams: []media.Stream{video(0, "mpeg2video"), sub(1, "dvd_subtitle")}}, c)
	if err != nil {
		t.Fatal(err)
	}
	if p.NeedsWork() {
		t.Errorf("actions = %q", actions(p))
	}
}

func TestImpossibleVideoIsAnError(t *testing.T) {
	c := cfg(t, `
container: {preferred: [webm]}
video: {preferred: [vp9]}
`)
	// vp9 is producible, so this works...
	if _, err := Build(&media.Info{Path: "/x/a.mkv", Streams: []media.Stream{video(0, "h264")}}, c); err != nil {
		t.Fatal(err)
	}
	// ...but with no video preference, h264 simply can't go in webm.
	c = cfg(t, `container: {preferred: [webm]}`)
	if _, err := Build(&media.Info{Path: "/x/a.mkv", Streams: []media.Stream{video(0, "h264")}}, c); err == nil {
		t.Fatal("expected an error")
	}
}

func hasOpt(s StreamPlan, name, value string) bool {
	for _, o := range s.Options {
		if o.Name == name && (value == "" || o.Value == value) {
			return true
		}
	}
	return false
}

func TestListedBitmapSubtitlesAreKeptQuietly(t *testing.T) {
	streams := []media.Stream{video(0, "h264"), sub(1, "hdmv_pgs_subtitle"), sub(2, "mov_text")}

	// Not listed: kept, but with a warning.
	p, err := Build(&media.Info{Path: "/x/a.mkv", Streams: streams}, cfg(t, `subtitles: {preferred: [srt, ass]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := actions(p); got != "copy copy convert:subrip" || len(p.Warnings) != 1 {
		t.Errorf("unlisted: actions = %q, warnings = %v", got, p.Warnings)
	}

	// Listed: kept silently, while other text subtitles still become srt.
	p, err = Build(&media.Info{Path: "/x/a.mkv", Streams: streams}, cfg(t, `subtitles: {preferred: [srt, ass, pgs]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := actions(p); got != "copy copy convert:subrip" || len(p.Warnings) != 0 {
		t.Errorf("listed: actions = %q, warnings = %v", got, p.Warnings)
	}
}

func lang(s media.Stream, l string) media.Stream {
	s.Tags = map[string]string{"language": l}
	return s
}

func TestLanguages(t *testing.T) {
	c := cfg(t, `
audio: {preferred: [aac], languages: [en]}
subtitles: {preferred: [srt], languages: [eng, de]}
`)
	def := lang(audio(1, "truehd", 8), "spa")
	def.Disposition = map[string]int{"default": 1}
	p, err := Build(&media.Info{Path: "/x/a.mkv", Streams: []media.Stream{
		video(0, "h264"),
		def,                             // default track, wrong language: dropped
		lang(audio(2, "ac3", 6), "eng"), // "en" in config matches "eng"
		lang(audio(3, "ac3", 6), "fre"),
		audio(4, "aac", 2),            // untagged: kept
		lang(sub(5, "subrip"), "ger"), // "de" matches the bibliographic "ger"
		lang(sub(6, "subrip"), "deu"), // ...and the terminologic "deu"
		lang(sub(7, "subrip"), "ita"),
		lang(sub(8, "subrip"), "und"), // undetermined: kept
	}}, c)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := actions(p), "copy drop convert:aac drop copy copy copy drop copy"; got != want {
		t.Errorf("actions = %q, want %q", got, want)
	}
	if !p.Streams[2].MakeDefault {
		t.Errorf("first kept audio should become the default after the default was dropped")
	}
	for i, s := range p.Streams {
		if i != 2 && s.MakeDefault {
			t.Errorf("stream %d unexpectedly made default", i)
		}
	}
	if !strings.Contains(p.Streams[1].Reason, "language spa") {
		t.Errorf("reason = %q", p.Streams[1].Reason)
	}
}

func TestLanguagesNeverLeaveAFileSilent(t *testing.T) {
	c := cfg(t, `audio: {languages: [eng]}`)
	p, err := Build(&media.Info{Path: "/x/a.mkv", Streams: []media.Stream{
		video(0, "h264"), lang(audio(1, "aac", 2), "jpn"), lang(audio(2, "aac", 6), "jpn"),
	}}, c)
	if err != nil {
		t.Fatal(err)
	}
	if got := actions(p); got != "copy copy copy" || p.NeedsWork() {
		t.Errorf("actions = %q", got)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "keeping all audio") {
		t.Errorf("warnings = %v", p.Warnings)
	}
}

func TestKeptDefaultIsLeftAlone(t *testing.T) {
	c := cfg(t, `audio: {languages: [eng]}`)
	eng := lang(audio(2, "aac", 2), "eng")
	eng.Disposition = map[string]int{"default": 1}
	p, err := Build(&media.Info{Path: "/x/a.mkv", Streams: []media.Stream{
		video(0, "h264"), lang(audio(1, "aac", 2), "spa"), eng,
	}}, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Streams {
		if s.MakeDefault {
			t.Errorf("default was kept, nothing should be re-flagged")
		}
	}
}

func TestBuildForUsesInstalledEncoders(t *testing.T) {
	c := cfg(t, `video: {preferred: [h264, hevc]}`)
	info := &media.Info{Path: "/x/a.mkv", Streams: []media.Stream{video(0, "mpeg4")}}

	// No libx264: the next preferred codec is used.
	tc := media.NewToolchain("9.0", []string{"libx265"}, []string{"matroska"})
	p, err := BuildFor(info, c, tc)
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Streams[0]; s.Codec != "hevc" || s.Encoder != "libx265" {
		t.Errorf("got %s via %s, want hevc via libx265", s.Codec, s.Encoder)
	}

	// Neither: the stream is kept as-is with a warning, since mkv holds it.
	p, err = BuildFor(info, c, media.NewToolchain("9.0", nil, []string{"matroska"}))
	if err != nil {
		t.Fatal(err)
	}
	if p.Streams[0].Action != Copy || len(p.Warnings) != 1 {
		t.Errorf("no encoder: action %s, warnings %v", p.Streams[0].Action, p.Warnings)
	}
	// ...but where the container can't hold it, the file fails.
	mp4 := cfg(t, "container: {preferred: [mp4]}\nvideo: {preferred: [h264]}")
	odd := &media.Info{Path: "/x/a.mkv", Streams: []media.Stream{video(0, "mpeg2video")}}
	if _, err := BuildFor(odd, mp4, media.NewToolchain("9.0", nil, []string{"mp4"})); err == nil {
		t.Errorf("expected an error: mp4 can't hold mpeg2video and nothing can convert it")
	}

	// A file that needs no conversion is fine without any encoder.
	ok := &media.Info{Path: "/x/a.mkv", Streams: []media.Stream{video(0, "h264")}}
	if _, err := BuildFor(ok, c, media.NewToolchain("9.0", nil, []string{"matroska"})); err != nil {
		t.Errorf("compliant file: %v", err)
	}
}

func TestBuildForNeedsTheMuxer(t *testing.T) {
	c := cfg(t, `container: {preferred: [mp4]}`)
	info := &media.Info{Path: "/x/a.mkv", Streams: []media.Stream{video(0, "h264")}}
	if _, err := BuildFor(info, c, media.NewToolchain("9.0", nil, []string{"matroska"})); err == nil || !strings.Contains(err.Error(), "cannot write mp4") {
		t.Errorf("err = %v", err)
	}
}
