package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/dkarlovi/mediator/config"
	"github.com/dkarlovi/mediator/media"
	"github.com/dkarlovi/mediator/plan"
	"github.com/dkarlovi/mediator/scan"
	"github.com/dkarlovi/mediator/transcode"
	"github.com/symfony-cli/console"
	"github.com/symfony-cli/terminal"
)

// Commands returns every mediator command.
func Commands() []*console.Command {
	return []*console.Command{runCmd, probeCmd}
}

func loadConfig(c *console.Context) (*config.Config, error) {
	cfg, path, err := config.LoadPrefer(c.String("config"))
	if err != nil {
		return nil, console.Exit(fmt.Sprintf("Failed to load config: %v", err), 1)
	}
	if terminal.IsVerbose() {
		fmt.Fprintf(c.App.Writer, "Using config <comment>%s</>\n", path)
	}
	return cfg, nil
}

var runCmd = &console.Command{
	Name:  "run",
	Usage: "Check media files against the config and convert the ones that differ",
	Description: `Probes every media file under the given paths and shows what would change.
Nothing is written unless <info>--ack</> is given.

Paths can be files or directories, local or on a network share:
  <info>mediator run "\\NAS\Shows\Example Show\Season 3"</>
  <info>mediator run /mnt/media/Shows --ack</>`,
	Args: []*console.Arg{
		{Name: "paths", Optional: true, Slice: true, Description: "Files or directories to process (default: paths from the config)"},
	},
	Flags: []console.Flag{
		&console.BoolFlag{Name: "ack", Usage: "Actually convert files (default is a dry run)"},
		&console.BoolFlag{Name: "no-recurse", Usage: "Do not descend into subdirectories"},
		&console.BoolFlag{Name: "show-ok", Usage: "Also list files that already match the config"},
	},
	Action: func(c *console.Context) error {
		cfg, err := loadConfig(c)
		if err != nil {
			return err
		}
		paths := c.Args().Tail()
		if len(paths) == 0 {
			paths = cfg.Paths
		}
		if len(paths) == 0 {
			return console.Exit("No paths given, and the config has no paths", 1)
		}

		files, err := scan.Files(paths, !c.Bool("no-recurse"))
		if err != nil {
			return console.Exit(fmt.Sprintf("Scanning failed: %v", err), 1)
		}
		ack := c.Bool("ack")
		fmt.Fprintf(c.App.Writer, "Found <info>%d</> media files\n\n", len(files))

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		interactive := terminal.IsTerminal(os.Stdout)
		var ok, converted, todo, done, failed int
		var saved int64
		dir := ""
		header := func(f string) {
			if d := filepath.Dir(f); d != dir {
				dir = d
				fmt.Fprintf(c.App.Writer, "<comment>%s</>\n", d)
			}
		}
		for n, f := range files {
			if ctx.Err() != nil {
				break
			}
			info, err := media.Probe(ctx, cfg.FFprobe, f)
			if err != nil {
				reportError(c, f, err)
				failed++
				continue
			}
			p, err := plan.Build(info, cfg)
			if err != nil {
				reportError(c, f, err)
				failed++
				continue
			}
			if !p.NeedsWork() {
				ok++
				if c.Bool("show-ok") || terminal.IsVerbose() {
					header(f)
					fmt.Fprintf(c.App.Writer, "  <info>%s</>: OK\n", filepath.Base(f))
				}
				continue
			}
			// With original: keep, an existing "-mediator" copy means an
			// earlier run already did this file.
			if cfg.Original == config.OriginalKeep && p.Output != p.Source {
				if _, err := os.Stat(p.Output); err == nil {
					converted++
					if c.Bool("show-ok") || terminal.IsVerbose() {
						header(f)
						fmt.Fprintf(c.App.Writer, "  <info>%s</>: already converted to %s\n", filepath.Base(f), filepath.Base(p.Output))
					}
					continue
				}
			}
			header(f)
			printPlan(c.App.Writer, p, "  ")
			if err := transcode.Preflight(p); err != nil {
				reportError(c, f, err)
				failed++
				continue
			}
			todo++
			if !ack {
				continue
			}
			var progress transcode.Progress
			if interactive {
				progress = progressPrinter(c.App.Writer, n+1, len(files))
			}

			res, err := transcode.Execute(ctx, p, transcode.Options{
				FFmpeg:   cfg.FFmpeg,
				FFprobe:  cfg.FFprobe,
				Original: cfg.Original,
				Progress: progress,
			})
			if interactive {
				fmt.Fprint(c.App.Writer, "\r\033[K")
			}
			if err != nil {
				if errors.Is(err, context.Canceled) {
					fmt.Fprintf(c.App.ErrWriter, "<fg=yellow>Interrupted; %s left untouched</>\n", f)
					break
				}
				reportError(c, f, err)
				failed++
				continue
			}
			done++
			saved += res.OldSize - res.NewSize
			msg := fmt.Sprintf("    <info>Done</> -> %s (%s -> %s) in %s", filepath.Base(res.Output), humanSize(res.OldSize), humanSize(res.NewSize), clock(res.Took))
			if res.Deleted {
				msg += ", original deleted"
			}
			fmt.Fprintln(c.App.Writer, msg)
		}

		fmt.Fprintln(c.App.Writer, "\n"+strings.Repeat("-", 40))
		if ack {
			fmt.Fprintf(c.App.Writer, "Converted: <info>%d</>  Already OK: %d  Converted earlier: %d  Errors: %d", done, ok, converted, failed)
			if done > 0 {
				fmt.Fprintf(c.App.Writer, "  Size change: %s", signedSize(-saved))
			}
			fmt.Fprintln(c.App.Writer)
		} else {
			fmt.Fprintf(c.App.Writer, "Would convert: <info>%d</>  Already OK: %d  Converted earlier: %d  Errors: %d\n", todo, ok, converted, failed)
			if todo > 0 {
				fmt.Fprintln(c.App.Writer, "Dry run, nothing was changed. Use <info>--ack</> to convert.")
			}
		}
		if failed > 0 {
			return console.Exit("", 1)
		}
		return nil
	},
}

func reportError(c *console.Context, path string, err error) {
	fmt.Fprintf(c.App.ErrWriter, "<fg=red>Error: %s: %v</>\n", path, err)
}

func printPlan(w io.Writer, p *plan.Plan, indent string) {
	fmt.Fprintf(w, "%s<info>%s</>\n", indent, filepath.Base(p.Source))
	in := indent + "  "
	if p.Src.Name != p.Dst.Name {
		fmt.Fprintf(w, "%s%-11s %-28s <fg=yellow>-> %s</> (%s)\n", in, "container", p.Src.Name, p.Dst.Name, filepath.Base(p.Output))
	} else {
		fmt.Fprintf(w, "%s%-11s %-28s keep\n", in, "container", p.Src.Name)
	}
	for _, s := range p.Streams {
		label := fmt.Sprintf("%s #%d", streamKind(s.Stream), s.Stream.Index)
		desc := describe(s.Stream)
		switch s.Action {
		case plan.Copy:
			fmt.Fprintf(w, "%s%-11s %-28s keep\n", in, label, desc)
		case plan.Convert:
			target := s.Codec
			if s.Channels > 0 {
				target += " " + channels(s.Channels)
			}
			for _, o := range s.Options {
				if o.Name == "b" {
					target += " @ " + o.Value
				}
			}
			fmt.Fprintf(w, "%s%-11s %-28s <fg=yellow>-> %s</>\n", in, label, desc, target)
		case plan.Drop:
			fmt.Fprintf(w, "%s%-11s %-28s <fg=red>drop</> (%s)\n", in, label, desc, s.Reason)
		}
	}
	for _, warn := range p.Warnings {
		fmt.Fprintf(w, "%s<fg=yellow>! %s</>\n", in, warn)
	}
}

func streamKind(s media.Stream) string {
	switch s.CodecType {
	case "subtitle":
		return "subs"
	case "attachment":
		return "attach"
	}
	return s.CodecType
}

func describe(s media.Stream) string {
	parts := []string{s.CodecName}
	switch s.CodecType {
	case "video":
		if s.Width > 0 {
			parts = append(parts, fmt.Sprintf("%dx%d", s.Width, s.Height))
		}
		if s.IsHDR() {
			parts = append(parts, "HDR")
		}
		if s.IsCoverArt() {
			parts = append(parts, "cover")
		}
	case "audio":
		if s.Channels > 0 {
			parts = append(parts, channels(s.Channels))
		}
	case "attachment":
		if fn := s.Tags["filename"]; fn != "" {
			parts = append(parts, fn)
		}
	}
	if l := s.Language(); l != "" {
		parts = append(parts, l)
	}
	if s.Disposition["forced"] == 1 {
		parts = append(parts, "forced")
	}
	d := strings.Join(parts, " ")
	if len(d) > 28 {
		d = d[:27] + "…"
	}
	return d
}

func channels(n int) string {
	switch n {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	}
	return fmt.Sprintf("%dch", n)
}

// progressPrinter redraws a single status line in place:
//
//	[███████████░░░░░░░░░░░░░]  46%  3.1x  12.4 MB/s  elapsed 0:41  ETA 0:48  (2/9)
func progressPrinter(w io.Writer, n, total int) transcode.Progress {
	return func(st transcode.Status) {
		fmt.Fprintf(w, "\r    %s\033[K", statusLine(st, n, total))
	}
}

func statusLine(st transcode.Status, n, total int) string {
	const width = 24
	filled := int(st.Fraction * width)
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	line := fmt.Sprintf("[%s] %3d%%", bar, int(st.Fraction*100))
	if st.Speed > 0 {
		line += fmt.Sprintf("  %.1fx", st.Speed)
	}
	if st.ReadRate > 0 {
		line += fmt.Sprintf("  %.1f MB/s", st.ReadRate/1e6)
	}
	line += "  elapsed " + clock(st.Elapsed)
	if st.ETA > 0 {
		line += "  ETA " + clock(st.ETA)
	}
	if total > 1 {
		line += fmt.Sprintf("  (%d/%d)", n, total)
	}
	return line
}

// clock formats a duration as m:ss, or h:mm:ss from an hour up.
func clock(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func signedSize(n int64) string {
	if n < 0 {
		return "-" + humanSize(-n)
	}
	return "+" + humanSize(n)
}
