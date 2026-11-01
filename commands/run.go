package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
	return []*console.Command{runCmd, probeCmd, doctorCmd}
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
		&console.StringFlag{Name: "workdir", Usage: "Local directory to convert in before uploading the result (overrides <info>workdir</> from the config)"},
	},
	Action: func(c *console.Context) error {
		cfg, err := loadConfig(c)
		if err != nil {
			return err
		}
		tc, err := detectToolchain(c, cfg)
		if err != nil {
			return err
		}
		_, issues := compatibility(cfg, tc)
		printIssues(c.App.ErrWriter, issues)

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
		workDir := cfg.WorkDir
		if wd := c.String("workdir"); wd != "" {
			if workDir, err = filepath.Abs(wd); err != nil {
				return console.Exit(fmt.Sprintf("Invalid --workdir: %v", err), 1)
			}
		}
		fmt.Fprintf(c.App.Writer, "Found <info>%d</> media files\n", len(files))
		if workDir != "" {
			note := ""
			if free, err := transcode.FreeSpace(workDir); err == nil {
				note = fmt.Sprintf(" (%s free)", humanSize(int64(free)))
			}
			fmt.Fprintf(c.App.Writer, "Converting in <comment>%s</>%s, then uploading\n", workDir, note)
		}
		fmt.Fprintln(c.App.Writer)

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		interactive := terminal.IsTerminal(os.Stdout)
		var ok, converted, adopted, todo, done, failed int
		// With original: delete, a "-mediator" copy left by an earlier keep
		// run is handled together with its source, not as a file of its own.
		stems := map[string]bool{}
		for _, f := range files {
			stems[strings.TrimSuffix(f, filepath.Ext(f))] = true
		}
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
			if stem := strings.TrimSuffix(f, filepath.Ext(f)); cfg.Original == config.OriginalDelete &&
				strings.HasSuffix(stem, suffix) && stems[strings.TrimSuffix(stem, suffix)] {
				continue
			}
			info, err := media.Probe(ctx, tc.FFprobe, f)
			if err != nil {
				reportError(c, f, err)
				failed++
				continue
			}
			p, err := plan.BuildFor(info, cfg, tc)
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
			out := outputPath(p, cfg.Original)
			if cfg.Original == config.OriginalKeep && out != p.Source {
				if _, err := os.Stat(out); err == nil {
					converted++
					if c.Bool("show-ok") || terminal.IsVerbose() {
						header(f)
						fmt.Fprintf(c.App.Writer, "  <info>%s</>: already converted to %s\n", filepath.Base(f), filepath.Base(out))
					}
					continue
				}
			}
			// With original: delete, a "-mediator" copy from an earlier keep run
			// is installed instead of converting again, if it still matches
			// the config. One that doesn't is replaced by a fresh conversion.
			stale, staleWhy := "", error(nil)
			if sib := outputPath(p, config.OriginalKeep); cfg.Original == config.OriginalDelete && sib != p.Source {
				if _, err := os.Stat(sib); err == nil {
					header(f)
					verr := transcode.Verify(ctx, tc.FFprobe, p, sib)
					if verr == nil {
						adopted++
						if !ack {
							fmt.Fprintf(c.App.Writer, "  <info>%s</>: already converted as %s; would install it as %s\n", filepath.Base(f), filepath.Base(sib), filepath.Base(out))
							continue
						}
						res, err := transcode.Adopt(ctx, p, sib, out, transcode.Options{FFprobe: tc.FFprobe, RemoveSource: true})
						if err != nil {
							adopted--
							reportError(c, f, err)
							failed++
							continue
						}
						msg := fmt.Sprintf("  <info>%s</>: installed earlier conversion %s as %s", filepath.Base(f), filepath.Base(sib), filepath.Base(res.Output))
						switch {
						case res.Output == p.Source:
							msg += ", replacing the original"
						case res.Deleted:
							msg += ", original deleted"
						}
						fmt.Fprintln(c.App.Writer, msg)
						continue
					}
					stale, staleWhy = sib, verr
				}
			}
			header(f)
			printPlan(c.App.Writer, p, out, "  ")
			if stale != "" {
				fmt.Fprintf(c.App.Writer, "    <fg=yellow>! %s doesn't match the current config (%v); converting again and removing it afterwards</>\n", filepath.Base(stale), staleWhy)
			}
			if err := transcode.Preflight(p, out); err != nil {
				reportError(c, f, err)
				failed++
				continue
			}
			todo++
			if workDir != "" {
				warnWorkDirSpace(c.App.Writer, workDir, info)
			}
			if !ack {
				continue
			}

			res, err := transcode.Execute(ctx, p, out, transcode.Options{
				FFmpeg:       tc.FFmpeg,
				FFprobe:      tc.FFprobe,
				RemoveSource: cfg.Original == config.OriginalDelete,
				WorkDir:      workDir,
				Progress:     progressPrinter(c.App.Writer, interactive, n+1, len(files)),
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
			if stale != "" {
				if err := os.Remove(stale); err != nil {
					reportError(c, stale, fmt.Errorf("removing outdated conversion: %w", err))
				} else {
					msg += ", outdated " + filepath.Base(stale) + " removed"
				}
			}
			fmt.Fprintln(c.App.Writer, msg)
		}

		fmt.Fprintln(c.App.Writer, "\n"+strings.Repeat("-", 40))
		if ack {
			fmt.Fprintf(c.App.Writer, "Converted: <info>%d</>  Already OK: %d  Converted earlier: %d", done, ok, converted)
			if adopted > 0 {
				fmt.Fprintf(c.App.Writer, "  Earlier conversions installed: %d", adopted)
			}
			fmt.Fprintf(c.App.Writer, "  Errors: %d", failed)
			if done > 0 {
				fmt.Fprintf(c.App.Writer, "  Size change: %s", signedSize(-saved))
			}
			fmt.Fprintln(c.App.Writer)
		} else {
			fmt.Fprintf(c.App.Writer, "Would convert: <info>%d</>  Already OK: %d  Converted earlier: %d", todo, ok, converted)
			if adopted > 0 {
				fmt.Fprintf(c.App.Writer, "  Would install earlier conversions: %d", adopted)
			}
			fmt.Fprintf(c.App.Writer, "  Errors: %d\n", failed)
			if todo > 0 || adopted > 0 {
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

func printPlan(w io.Writer, p *plan.Plan, out, indent string) {
	fmt.Fprintf(w, "%s<info>%s</>\n", indent, filepath.Base(p.Source))
	in := indent + "  "
	if p.Src.Name != p.Dst.Name {
		fmt.Fprintf(w, "%s%-11s %-28s <fg=yellow>-> %s</> (%s)\n", in, "container", p.Src.Name, p.Dst.Name, filepath.Base(out))
	} else {
		fmt.Fprintf(w, "%s%-11s %-28s keep\n", in, "container", p.Src.Name)
	}
	for _, s := range p.Streams {
		label := fmt.Sprintf("%s #%d", streamKind(s.Stream), s.Stream.Index)
		desc := describe(s.Stream)
		note := ""
		if s.MakeDefault {
			note = " <fg=yellow>(becomes default)</>"
		}
		switch s.Action {
		case plan.Copy:
			fmt.Fprintf(w, "%s%-11s %-28s keep%s\n", in, label, desc, note)
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
			fmt.Fprintf(w, "%s%-11s %-28s <fg=yellow>-> %s</>%s\n", in, label, desc, target, note)
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

// progressPrinter shows each phase of a conversion. In a terminal the
// current phase is redrawn in place; every phase that finishes leaves a
// summary line, so the steps stay visible:
//
//	convert [████████████████████] 100%  30.2x  read 95.1 MB/s  took 4:12
//	upload  [████████░░░░░░░░░░░░]  41%  write 18.2 MB/s  elapsed 14:02  ETA 20:11  (2/9)
func progressPrinter(w io.Writer, interactive bool, n, total int) transcode.Progress {
	return func(st transcode.Status) {
		switch {
		case st.Done && interactive:
			fmt.Fprintf(w, "\r    %s\033[K\n", statusLine(st, n, total))
		case st.Done:
			fmt.Fprintf(w, "    %s\n", statusLine(st, n, total))
		case interactive:
			fmt.Fprintf(w, "\r    %s\033[K", statusLine(st, n, total))
		}
	}
}

func statusLine(st transcode.Status, n, total int) string {
	const width = 20
	filled := int(st.Fraction * width)
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	line := fmt.Sprintf("%-7s [%s] %3d%%", st.Phase, bar, int(st.Fraction*100))
	if st.Speed > 0 {
		line += fmt.Sprintf("  %.1fx", st.Speed)
	}
	if st.Rate > 0 {
		dir := "read"
		if st.Phase == transcode.PhaseUpload {
			dir = "write"
		}
		line += fmt.Sprintf("  %s %.1f MB/s", dir, st.Rate/1e6)
	}
	if st.Done {
		return line + "  took " + clock(st.Elapsed)
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

// warnWorkDirSpace warns when the work directory may be too small. The output
// size isn't known up front; the source size is a safe upper bound for the
// usual case of converting audio and keeping the video.
func warnWorkDirSpace(w io.Writer, dir string, info *media.Info) {
	need, _ := strconv.ParseInt(info.Format.Size, 10, 64)
	free, err := transcode.FreeSpace(dir)
	if err != nil || need <= 0 || int64(free) >= need {
		return
	}
	fmt.Fprintf(w, "    <fg=yellow>! work directory has %s free; the output may need up to %s</>\n", humanSize(int64(free)), humanSize(need))
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
