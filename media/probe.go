package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Stream is one stream of a media file, as reported by ffprobe.
type Stream struct {
	Index         int               `json:"index"`
	CodecType     string            `json:"codec_type"`
	CodecName     string            `json:"codec_name"`
	Profile       string            `json:"profile"`
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	PixFmt        string            `json:"pix_fmt"`
	ColorTransfer string            `json:"color_transfer"`
	Channels      int               `json:"channels"`
	ChannelLayout string            `json:"channel_layout"`
	BitRate       string            `json:"bit_rate"`
	RawDuration   string            `json:"duration"`
	Tags          map[string]string `json:"tags"`
	Disposition   map[string]int    `json:"disposition"`
}

// Language returns the stream's language tag, or "" when it has none.
func (s Stream) Language() string {
	if l := s.Tags["language"]; l != "" && l != "und" {
		return l
	}
	return ""
}

// Duration returns the stream's own duration in seconds, or 0 when unknown.
// MP4 stores it per stream; Matroska as a DURATION tag ("01:41:52.600000000"),
// which mkvmerge and ffmpeg write for every track.
func (s Stream) Duration() float64 {
	if d, err := strconv.ParseFloat(s.RawDuration, 64); err == nil && d > 0 {
		return d
	}
	for k, v := range s.Tags {
		if strings.EqualFold(k, "DURATION") || strings.HasPrefix(strings.ToUpper(k), "DURATION-") {
			if d := parseClock(v); d > 0 {
				return d
			}
		}
	}
	return 0
}

// parseClock parses "HH:MM:SS.fraction" into seconds, or returns 0.
func parseClock(v string) float64 {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) != 3 {
		return 0
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0
	}
	return float64(h*3600+m*60) + sec
}

// Title returns the stream's title tag.
func (s Stream) Title() string { return s.Tags["title"] }

// IsCoverArt reports whether a "video" stream is really an embedded picture
// (poster, thumbnail). Those are copied as-is and never transcoded.
func (s Stream) IsCoverArt() bool { return s.Disposition["attached_pic"] == 1 }

// IsHDR reports whether a video stream uses an HDR transfer function. Converting
// one to another codec with default settings throws the HDR away.
func (s Stream) IsHDR() bool {
	return s.ColorTransfer == "smpte2084" || s.ColorTransfer == "arib-std-b67"
}

// Format is the container-level information ffprobe reports.
type Format struct {
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
	Size       string `json:"size"`
	BitRate    string `json:"bit_rate"`
}

// Info is everything mediator knows about a media file.
type Info struct {
	Path    string   `json:"-"`
	Streams []Stream `json:"streams"`
	Format  Format   `json:"format"`
}

// Duration returns the file's duration in seconds, or 0 when unknown.
func (i *Info) Duration() float64 {
	d, _ := strconv.ParseFloat(i.Format.Duration, 64)
	return d
}

// Probe runs ffprobe on a file.
func Probe(ctx context.Context, ffprobe, path string) (*Info, error) {
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ffprobe: %s", msg)
	}
	var info Info
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("ffprobe: parsing output: %w", err)
	}
	info.Path = path
	return &info, nil
}
