package media

import (
	"path/filepath"
	"slices"
	"strings"
)

// aliases maps the names people tend to write in a config file onto the codec
// names ffprobe reports, so "srt" and "subrip" (or "h265" and "hevc") are the
// same thing everywhere else in mediator.
var aliases = map[string]string{
	"srt":    "subrip",
	"h265":   "hevc",
	"x265":   "hevc",
	"avc":    "h264",
	"x264":   "h264",
	"pgs":    "hdmv_pgs_subtitle",
	"vobsub": "dvd_subtitle",
	"vtt":    "webvtt",
	"ddp":    "eac3",
	"dd":     "ac3",
}

// Canonical returns the ffprobe codec name for a codec name or alias.
func Canonical(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if c, ok := aliases[n]; ok {
		return c
	}
	return n
}

// encoders maps a target codec onto the ffmpeg encoder that produces it.
var encoders = map[string]string{
	// audio
	"aac":    "aac",
	"mp3":    "libmp3lame",
	"ac3":    "ac3",
	"eac3":   "eac3",
	"opus":   "libopus",
	"flac":   "flac",
	"vorbis": "libvorbis",
	"alac":   "alac",
	// video
	"h264": "libx264",
	"hevc": "libx265",
	"av1":  "libsvtav1",
	"vp9":  "libvpx-vp9",
	// subtitles
	"subrip":   "srt",
	"ass":      "ass",
	"mov_text": "mov_text",
	"webvtt":   "webvtt",
}

// Encoder returns the ffmpeg encoder for a target codec.
func Encoder(codec string) (string, bool) {
	e, ok := encoders[codec]
	return e, ok
}

// textSubtitles are the subtitle codecs that are plain text and can therefore
// be converted between each other. Bitmap subtitles (PGS, VobSub, DVB) can only
// ever be copied: turning them into text needs OCR.
var textSubtitles = []string{"subrip", "ass", "ssa", "mov_text", "webvtt", "text"}

// IsTextSubtitle reports whether a subtitle codec is text based.
func IsTextSubtitle(codec string) bool {
	return slices.Contains(textSubtitles, codec)
}

// Container describes what an output container can hold. A nil codec list
// means "anything ffmpeg will mux".
type Container struct {
	Name        string
	Ext         string
	Muxer       string
	Video       []string
	Audio       []string
	Subtitles   []string
	Attachments bool
	Data        bool
	// FastStart moves the MP4 index to the front so playback (and Plex
	// direct play) can start before the whole file has been read.
	FastStart bool
	// HvcTag tags HEVC as hvc1 instead of hev1, which Apple players and many
	// Plex clients require for HEVC in MP4/MOV.
	HvcTag bool
}

var mp4Video = []string{"h264", "hevc", "av1", "vp9", "mpeg4", "mjpeg", "png"}
var mp4Audio = []string{"aac", "mp3", "ac3", "eac3", "alac", "flac", "opus"}

var containers = map[string]Container{
	"mkv": {Name: "mkv", Ext: "mkv", Muxer: "matroska", Attachments: true,
		Subtitles: []string{"subrip", "ass", "ssa", "webvtt", "hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle"}},
	"mp4":  {Name: "mp4", Ext: "mp4", Muxer: "mp4", Video: mp4Video, Audio: mp4Audio, Subtitles: []string{"mov_text"}, FastStart: true, HvcTag: true},
	"m4v":  {Name: "m4v", Ext: "m4v", Muxer: "mp4", Video: mp4Video, Audio: mp4Audio, Subtitles: []string{"mov_text"}, FastStart: true, HvcTag: true},
	"mov":  {Name: "mov", Ext: "mov", Muxer: "mov", Video: mp4Video, Audio: mp4Audio, Subtitles: []string{"mov_text"}, Data: true, FastStart: true, HvcTag: true},
	"webm": {Name: "webm", Ext: "webm", Muxer: "webm", Video: []string{"vp8", "vp9", "av1"}, Audio: []string{"opus", "vorbis"}, Subtitles: []string{"webvtt"}},
	"avi":  {Name: "avi", Ext: "avi", Muxer: "avi", Subtitles: []string{}},
	"ts":   {Name: "ts", Ext: "ts", Muxer: "mpegts", Subtitles: []string{"dvb_subtitle"}, Data: true},
}

// LookupContainer returns the container for a name such as "mkv".
func LookupContainer(name string) (Container, bool) {
	c, ok := containers[strings.ToLower(strings.TrimPrefix(name, "."))]
	return c, ok
}

// ContainerOf returns the container a file is in, judged by its extension.
func ContainerOf(path string) (Container, bool) {
	return LookupContainer(filepath.Ext(path))
}

// IsMediaFile reports whether a path has an extension mediator handles.
func IsMediaFile(path string) bool {
	_, ok := ContainerOf(path)
	return ok
}

// Allows reports whether the container can hold a stream of the given type and
// codec.
func (c Container) Allows(codecType, codec string) bool {
	var list []string
	switch codecType {
	case "video":
		list = c.Video
	case "audio":
		list = c.Audio
	case "subtitle":
		list = c.Subtitles
	case "attachment":
		return c.Attachments
	case "data":
		return c.Data
	default:
		return false
	}
	return list == nil || slices.Contains(list, codec)
}
