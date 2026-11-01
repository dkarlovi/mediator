package commands

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/dkarlovi/mediator/config"
	"github.com/dkarlovi/mediator/media"
	"github.com/dkarlovi/mediator/transcode"
	"github.com/symfony-cli/console"
	"github.com/symfony-cli/terminal"
)

// issue is something about the ffmpeg install that affects the config.
// Fatal ones mean files the config sends to a container ffmpeg can't write
// will always fail.
type issue struct {
	fatal bool
	text  string
}

// section is one line of the compatibility table: a config section and how
// each of its preferred entries maps onto the installed ffmpeg.
type section struct {
	name    string
	entries []string
}

// compatibility checks a config against an ffmpeg install.
func compatibility(cfg *config.Config, tc *media.Toolchain) ([]section, []issue) {
	var sections []section
	var issues []issue

	if untested, why := tc.Untested(); untested {
		issues = append(issues, issue{text: why})
	}
	if tc.ProbeVersion != "" && tc.ProbeVersion != tc.Version {
		issues = append(issues, issue{text: fmt.Sprintf("ffprobe %s and ffmpeg %s are different versions; they should come from the same install", tc.ProbeVersion, tc.Version)})
	}

	cont := section{name: "container"}
	for _, name := range cfg.Container.Preferred {
		c, _ := media.LookupContainer(name)
		if tc.HasMuxer(c.Muxer) {
			cont.entries = append(cont.entries, fmt.Sprintf("%s <info>✓</>", name))
		} else {
			cont.entries = append(cont.entries, fmt.Sprintf("%s <fg=red>✗ no %s muxer</>", name, c.Muxer))
			issues = append(issues, issue{fatal: true, text: fmt.Sprintf("container: this ffmpeg cannot write %s files (no %q muxer)", name, c.Muxer)})
		}
	}
	if len(cont.entries) > 0 {
		sections = append(sections, cont)
	}

	for _, s := range []struct {
		name string
		cfg  config.StreamConfig
	}{{"video", cfg.Video}, {"audio", cfg.Audio}, {"subtitles", cfg.Subtitles}} {
		if len(s.cfg.Preferred) == 0 {
			continue
		}
		sec := section{name: s.name}
		var usable int
		for _, codec := range s.cfg.Preferred {
			want, known := media.Encoder(codec)
			enc, ok := tc.Encoder(codec)
			switch {
			case ok:
				usable++
				sec.entries = append(sec.entries, fmt.Sprintf("%s <info>✓</> %s", codec, enc))
			case !known:
				// Bitmap subtitles: accepted as-is, never produced.
				sec.entries = append(sec.entries, fmt.Sprintf("%s (copy only)", codec))
			default:
				sec.entries = append(sec.entries, fmt.Sprintf("%s <fg=red>✗ no %s</>", codec, want))
				issues = append(issues, issue{text: fmt.Sprintf("%s: this ffmpeg has no %s encoder, so nothing will be converted to %s", s.name, want, codec)})
			}
		}
		if usable == 0 && s.name != "subtitles" {
			issues = append(issues, issue{text: fmt.Sprintf("%s: this ffmpeg can produce none of %v; %s in other codecs will be kept as-is, and files whose container can't hold it will fail", s.name, s.cfg.Preferred, s.name)})
		}
		sections = append(sections, sec)
	}
	return sections, issues
}

// detectToolchain finds ffmpeg for a command, failing with an install hint.
func detectToolchain(c *console.Context, cfg *config.Config) (*media.Toolchain, error) {
	tc, err := media.Detect(context.Background(), cfg.FFmpeg, cfg.FFprobe)
	if err != nil {
		return nil, console.Exit(err.Error(), 1)
	}
	if terminal.IsVerbose() {
		fmt.Fprintf(c.App.Writer, "Using ffmpeg <comment>%s</> (%s)\n", tc.Version, tc.FFmpeg)
	}
	return tc, nil
}

// printIssues prints problems found with the ffmpeg install, before a run.
func printIssues(w io.Writer, issues []issue) {
	for _, is := range issues {
		color := "yellow"
		if is.fatal {
			color = "red"
		}
		fmt.Fprintf(w, "<fg=%s>! %s</>\n", color, is.text)
	}
}

var doctorCmd = &console.Command{
	Name:        "doctor",
	Aliases:     []*console.Alias{{Name: "check"}},
	Usage:       "Check that ffmpeg is installed and can do what the config asks",
	Description: "Shows which ffmpeg and config mediator uses, and for every preferred container and codec whether the installed ffmpeg can produce it.",
	Action: func(c *console.Context) error {
		w := c.App.Writer
		cfg, path, err := config.LoadPrefer(c.String("config"))
		if err != nil {
			return console.Exit(fmt.Sprintf("Failed to load config: %v", err), 1)
		}
		tc, err := media.Detect(context.Background(), cfg.FFmpeg, cfg.FFprobe)
		if err != nil {
			fmt.Fprintf(w, "%-10s %s\n", "config", path)
			return console.Exit(err.Error(), 1)
		}
		fmt.Fprintf(w, "%-10s %s  <comment>%s</>\n", "ffmpeg", tc.Version, tc.FFmpeg)
		fmt.Fprintf(w, "%-10s %s  <comment>%s</>\n", "ffprobe", tc.ProbeVersion, tc.FFprobe)
		fmt.Fprintf(w, "%-10s <comment>%s</>\n", "config", path)
		if cfg.WorkDir != "" {
			note := ""
			if free, err := transcode.FreeSpace(cfg.WorkDir); err == nil {
				note = fmt.Sprintf(" (%s free)", humanSize(int64(free)))
			}
			fmt.Fprintf(w, "%-10s <comment>%s</>%s\n", "workdir", cfg.WorkDir, note)
		}
		fmt.Fprintln(w)

		sections, issues := compatibility(cfg, tc)
		for _, s := range sections {
			fmt.Fprintf(w, "%-10s %s\n", s.name, strings.Join(s.entries, "   "))
		}
		fmt.Fprintln(w)

		if len(issues) == 0 {
			fmt.Fprintln(w, "<info>ffmpeg can do everything this config asks for.</>")
			return nil
		}
		printIssues(w, issues)
		for _, is := range issues {
			if is.fatal {
				return console.Exit("", 1)
			}
		}
		return nil
	},
}
