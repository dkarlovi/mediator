// Package plan decides, for one media file, what has to happen to it so it
// matches the configuration. It is pure: it only looks at ffprobe output and
// the config, and never touches the file.
package plan

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/dkarlovi/mediator/config"
	"github.com/dkarlovi/mediator/media"
)

// Action is what happens to a single stream.
type Action string

const (
	Copy    Action = "copy"
	Convert Action = "convert"
	Drop    Action = "drop"
)

// Option is one encoder option, written without a stream specifier; the
// ffmpeg command builder adds the output stream index.
type Option struct {
	Name  string
	Value string
}

// StreamPlan is the decision for one input stream.
type StreamPlan struct {
	Stream media.Stream
	Action Action
	// Codec is the output codec when converting.
	Codec string
	// Encoder is the ffmpeg encoder when converting.
	Encoder string
	// Options are the encoder options when converting.
	Options []Option
	// Channels is the output channel count of converted audio.
	Channels int
	// Reason explains a convert or drop decision.
	Reason string
	// MakeDefault marks this stream as the default track of its kind, set
	// when the source's default audio track is dropped.
	MakeDefault bool
	// Reattach writes cover art into a Matroska output as an attachment.
	// Copied as a stream, ffmpeg would turn it into a one-frame video track.
	Reattach bool
}

// Plan is everything that will happen to one file.
type Plan struct {
	Info     *media.Info
	Source   string
	Src      media.Container
	Dst      media.Container
	Streams  []StreamPlan
	Warnings []string
	// tc is the ffmpeg the plan is for; nil assumes every encoder exists.
	tc *media.Toolchain
}

// NeedsWork reports whether the file has to be rewritten at all.
func (p *Plan) NeedsWork() bool {
	if p.Src.Name != p.Dst.Name {
		return true
	}
	for _, s := range p.Streams {
		if s.Action != Copy || s.Repair() {
			return true
		}
	}
	return false
}

// Repair reports whether a stream is cover art that an earlier remux turned
// into a video track, and this plan restores as an attachment.
func (s StreamPlan) Repair() bool { return s.Reattach && s.Stream.CoverLost() }

// Kept returns the streams that end up in the output, in output order.
//
// Cover art that is re-attached comes last, because ffmpeg adds attachments
// after all mapped streams.
func (p *Plan) Kept() []StreamPlan {
	var kept, attached []StreamPlan
	for _, s := range p.Streams {
		switch {
		case s.Action == Drop:
		case s.Reattach:
			attached = append(attached, s)
		default:
			kept = append(kept, s)
		}
	}
	return append(kept, attached...)
}

// maxChannels caps encoders that cannot produce more channels than this.
var maxChannels = map[string]int{"mp3": 2, "ac3": 6, "eac3": 6, "aac": 8, "opus": 8}

// Build decides what has to happen to a probed file, assuming an ffmpeg with
// every encoder mediator knows. Use BuildFor to plan for the installed one.
func Build(info *media.Info, cfg *config.Config) (*Plan, error) {
	return BuildFor(info, cfg, nil)
}

// BuildFor decides what has to happen to a probed file with the given ffmpeg:
// a preferred codec it has no encoder for is skipped in favour of the next.
func BuildFor(info *media.Info, cfg *config.Config, tc *media.Toolchain) (*Plan, error) {
	src, ok := media.ContainerOf(info.Path)
	if !ok {
		return nil, fmt.Errorf("unsupported container %q", filepath.Ext(info.Path))
	}
	dst := src
	if prefs := cfg.Container.Preferred; len(prefs) > 0 && !slices.Contains(prefs, src.Name) {
		dst, _ = media.LookupContainer(prefs[0])
	}

	if !tc.HasMuxer(dst.Muxer) {
		return nil, fmt.Errorf("this ffmpeg cannot write %s files (no %q muxer)", dst.Name, dst.Muxer)
	}

	p := &Plan{
		Info:   info,
		Source: info.Path,
		Src:    src,
		Dst:    dst,
		tc:     tc,
	}

	// A language filter must never leave a file silent: if none of the audio
	// tracks would survive it, keep them all.
	keepAudio := cfg.Audio.Languages
	if len(keepAudio) > 0 && !slices.ContainsFunc(info.Streams, func(s media.Stream) bool {
		return s.CodecType == "audio" && wantedLanguage(s, keepAudio)
	}) {
		if slices.ContainsFunc(info.Streams, func(s media.Stream) bool { return s.CodecType == "audio" }) {
			p.warnf("no audio track is in %v; keeping all audio tracks", keepAudio)
		}
		keepAudio = nil
	}

	for _, s := range info.Streams {
		var sp StreamPlan
		var err error
		switch {
		case s.CodecType == "audio" && !wantedLanguage(s, keepAudio):
			sp = StreamPlan{Stream: s, Action: Drop, Reason: fmt.Sprintf("language %s not in %v", s.Language(), keepAudio)}
		case s.CodecType == "subtitle" && !wantedLanguage(s, cfg.Subtitles.Languages):
			sp = StreamPlan{Stream: s, Action: Drop, Reason: fmt.Sprintf("language %s not in %v", s.Language(), cfg.Subtitles.Languages)}
		default:
			sp, err = p.planStream(s, cfg)
		}
		if err != nil {
			return nil, fmt.Errorf("stream #%d (%s %s): %w", s.Index, s.CodecType, s.CodecName, err)
		}
		p.Streams = append(p.Streams, sp)
	}
	p.keepDefaultAudio()

	if len(p.Kept()) == 0 {
		return nil, fmt.Errorf("no streams would be left in the output")
	}
	return p, nil
}

// wantedLanguage reports whether a stream passes a language filter. An empty
// filter passes everything, and so does a stream without a language tag:
// there is no telling what it is.
func wantedLanguage(s media.Stream, languages []string) bool {
	lang := media.Language(s.Language())
	return len(languages) == 0 || lang == "" || slices.Contains(languages, lang)
}

// keepDefaultAudio makes the first kept audio track the default when the
// source's default audio track was dropped, so players don't have to guess.
func (p *Plan) keepDefaultAudio() {
	first := -1
	for i, s := range p.Streams {
		if s.Stream.CodecType != "audio" {
			continue
		}
		if s.Action != Drop {
			if s.Stream.Disposition["default"] == 1 {
				return
			}
			if first < 0 {
				first = i
			}
		}
	}
	droppedDefault := slices.ContainsFunc(p.Streams, func(s StreamPlan) bool {
		return s.Action == Drop && s.Stream.CodecType == "audio" && s.Stream.Disposition["default"] == 1
	})
	if first >= 0 && droppedDefault {
		p.Streams[first].MakeDefault = true
	}
}

func (p *Plan) warnf(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

func (p *Plan) planStream(s media.Stream, cfg *config.Config) (StreamPlan, error) {
	dst := p.Dst
	switch s.CodecType {
	case "video":
		if s.IsCoverArt() {
			if _, _, ok := media.CoverFile(s.CodecName); ok && dst.Muxer == "matroska" {
				sp := StreamPlan{Stream: s, Action: Copy, Reattach: true}
				if s.CoverLost() {
					sp.Reason = "cover art stored as a video track; restoring it as an attachment"
				}
				return sp, nil
			}
			if dst.Allows("video", s.CodecName) {
				return StreamPlan{Stream: s, Action: Copy}, nil
			}
			return StreamPlan{Stream: s, Action: Drop, Reason: fmt.Sprintf("%s cannot hold %s cover art", dst.Name, s.CodecName)}, nil
		}
		return p.choose(s, cfg.Video)
	case "audio":
		return p.choose(s, cfg.Audio)
	case "subtitle":
		return p.choose(s, cfg.Subtitles)
	case "attachment":
		if dst.Attachments {
			return StreamPlan{Stream: s, Action: Copy}, nil
		}
		return StreamPlan{Stream: s, Action: Drop, Reason: fmt.Sprintf("%s cannot hold attachments", dst.Name)}, nil
	case "data":
		if dst.Data && p.Src.Muxer == dst.Muxer {
			return StreamPlan{Stream: s, Action: Copy}, nil
		}
		return StreamPlan{Stream: s, Action: Drop, Reason: "data stream cannot be carried over to " + dst.Name}, nil
	}
	return StreamPlan{Stream: s, Action: Drop, Reason: fmt.Sprintf("unknown stream type %q", s.CodecType)}, nil
}

// choose picks copy or convert for a video, audio or subtitle stream.
func (p *Plan) choose(s media.Stream, cfg config.StreamConfig) (StreamPlan, error) {
	dst := p.Dst
	codec := s.CodecName
	kind := s.CodecType
	prefs := cfg.Preferred
	fits := dst.Allows(kind, codec)

	if fits && (len(prefs) == 0 || slices.Contains(prefs, codec)) {
		return StreamPlan{Stream: s, Action: Copy}, nil
	}

	reason := fmt.Sprintf("%s is not in preferred %v", codec, prefs)
	if !fits {
		reason = fmt.Sprintf("%s cannot hold %s", dst.Name, codec)
	}

	convertible := func(target string) bool {
		if _, ok := p.tc.Encoder(target); !ok || !dst.Allows(kind, target) {
			return false
		}
		// Bitmap subtitles can only be copied; text ones only become text.
		return kind != "subtitle" || (media.IsTextSubtitle(codec) && media.IsTextSubtitle(target))
	}

	for _, target := range prefs {
		if convertible(target) {
			return p.convert(s, target, cfg.Convert, reason), nil
		}
	}

	// A text subtitle the container can't hold is still worth converting to
	// whatever text format it can (srt in mkv -> mov_text in mp4).
	if kind == "subtitle" && !fits && media.IsTextSubtitle(codec) {
		for _, target := range dst.Subtitles {
			if convertible(target) {
				return p.convert(s, target, cfg.Convert, reason), nil
			}
		}
	}

	if fits {
		p.warnf("stream #%d: %s is not preferred, but cannot be converted to any of %v; keeping it", s.Index, codec, prefs)
		return StreamPlan{Stream: s, Action: Copy}, nil
	}
	if kind == "subtitle" {
		return StreamPlan{Stream: s, Action: Drop, Reason: reason + " and it cannot be converted"}, nil
	}
	return StreamPlan{}, fmt.Errorf("%s, and none of %v can be produced for it", reason, prefs)
}

func (p *Plan) convert(s media.Stream, target string, cc config.ConvertConfig, reason string) StreamPlan {
	enc, _ := p.tc.Encoder(target)
	sp := StreamPlan{Stream: s, Action: Convert, Codec: target, Encoder: enc, Reason: reason}

	switch s.CodecType {
	case "audio":
		ch := s.Channels
		if cc.Channels > 0 && ch > cc.Channels {
			ch = cc.Channels
		}
		if m, ok := maxChannels[target]; ok && ch > m {
			ch = m
		}
		if ch > 0 && ch != s.Channels {
			sp.Channels = ch
			sp.Options = append(sp.Options, Option{"ac", strconv.Itoa(ch)})
		} else {
			sp.Channels = s.Channels
		}
		if target != "flac" && target != "alac" {
			br := cc.Bitrate
			if br == "" && sp.Channels > 0 {
				br = strconv.Itoa(64*sp.Channels) + "k"
			}
			if br != "" {
				sp.Options = append(sp.Options, Option{"b", br})
			}
		}
	case "video":
		sp.Options = videoOptions(enc, cc)
		if enc == "libx264" && strings.Contains(s.PixFmt, "10") {
			// 10-bit H.264 barely plays anywhere; it defeats the point.
			sp.Options = append(sp.Options, Option{"pix_fmt", "yuv420p"})
		}
		if s.IsHDR() {
			p.warnf("stream #%d is HDR (%s); converting it to %s will lose the HDR", s.Index, s.ColorTransfer, target)
		}
		p.warnf("stream #%d: converting video (%s -> %s) is slow and lossy", s.Index, s.CodecName, target)
	}
	return sp
}

func videoOptions(enc string, cc config.ConvertConfig) []Option {
	type defaults struct {
		crf    int
		preset string
	}
	d := map[string]defaults{
		"libx264":    {20, "medium"},
		"libx265":    {22, "medium"},
		"libsvtav1":  {30, "6"},
		"libaom-av1": {30, ""},
		"libvpx-vp9": {31, ""},
	}[enc]
	crf, preset := d.crf, d.preset
	if cc.CRF > 0 {
		crf = cc.CRF
	}
	if cc.Preset != "" {
		preset = cc.Preset
	}
	var opts []Option
	if preset != "" {
		opts = append(opts, Option{"preset", preset})
	}
	if cc.Bitrate != "" {
		return append(opts, Option{"b", cc.Bitrate})
	}
	if crf > 0 {
		opts = append(opts, Option{"crf", strconv.Itoa(crf)})
	}
	if enc == "libvpx-vp9" || enc == "libaom-av1" {
		// Both treat -crf as a cap on a target bitrate unless that is 0.
		opts = append(opts, Option{"b", "0"})
	}
	return opts
}
