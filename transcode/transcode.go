// Package transcode turns a plan into an ffmpeg run, verifies the result and
// swaps it in for the original.
package transcode

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dkarlovi/mediator/config"
	"github.com/dkarlovi/mediator/media"
	"github.com/dkarlovi/mediator/plan"
)

// TempMarker is part of every in-progress output file name, so a scan never
// picks up a half-written file left behind by an interrupted run.
const TempMarker = ".mediator-tmp."

// TempPath is where the output is written before it replaces the source. It
// lives next to the source so the final swap is a rename on the same volume,
// never a second copy across the network.
func TempPath(p *plan.Plan) string {
	dir, base := filepath.Split(p.Output)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(dir, name+TempMarker+p.Dst.Ext)
}

// Args builds the ffmpeg arguments that execute a plan into out.
func Args(p *plan.Plan, out string) []string {
	args := []string{
		"-hide_banner", "-nostdin", "-y",
		"-v", "error",
		"-progress", "pipe:1", "-nostats",
		"-i", p.Source,
		"-map_metadata", "0",
		"-map_chapters", "0",
	}
	for i, s := range p.Kept() {
		o := strconv.Itoa(i)
		args = append(args, "-map", "0:"+strconv.Itoa(s.Stream.Index))
		switch s.Action {
		case plan.Copy:
			args = append(args, "-c:"+o, "copy")
		case plan.Convert:
			args = append(args, "-c:"+o, s.Encoder)
			for _, opt := range s.Options {
				args = append(args, "-"+opt.Name+":"+o, opt.Value)
			}
		}
		hevc := s.Stream.CodecName == "hevc" && s.Action == plan.Copy || s.Codec == "hevc"
		if p.Dst.HvcTag && s.Stream.CodecType == "video" && hevc {
			args = append(args, "-tag:"+o, "hvc1")
		}
	}
	// Sparse subtitle streams otherwise make the muxer give up on long files.
	args = append(args, "-max_muxing_queue_size", "9999")
	if p.Dst.FastStart {
		args = append(args, "-movflags", "+faststart")
	}
	return append(args, "-f", p.Dst.Muxer, out)
}

// Status is a snapshot of a running conversion.
type Status struct {
	// Fraction is how much of the file is done, 0..1.
	Fraction float64
	// Speed is ffmpeg's processing speed relative to playback (2.0 = twice
	// real time), or 0 when ffmpeg hasn't reported one yet.
	Speed float64
	// Elapsed is the wall-clock time since ffmpeg started.
	Elapsed time.Duration
	// ETA is the estimated time left, or 0 when it can't be estimated yet.
	ETA time.Duration
	// ReadRate is source bytes processed per second, estimated from the
	// fraction done and the source size: the share's effective throughput.
	ReadRate float64
	// Written is the output size so far, in bytes.
	Written int64
	// Done is set on the final update.
	Done bool
}

// Progress receives a status update roughly twice a second.
type Progress func(Status)

// Options control a single execution.
type Options struct {
	FFmpeg   string
	FFprobe  string
	Original config.OriginalPolicy
	Progress Progress
}

// Result describes what Execute did.
type Result struct {
	Output string
	// Deleted is set when the source was removed (original: delete).
	Deleted bool
	OldSize int64
	NewSize int64
	// Took is how long ffmpeg ran.
	Took     time.Duration
	Replaced bool
}

// Preflight reports problems Execute would hit before any work is done, so a
// dry run can show them too.
func Preflight(p *plan.Plan) error {
	if p.Output != p.Source {
		if _, err := os.Stat(p.Output); err == nil {
			return fmt.Errorf("output %s already exists", p.Output)
		}
	}
	if _, err := os.Stat(TempPath(p)); err == nil {
		return fmt.Errorf("leftover temporary file %s exists (from an interrupted run?); remove it first", TempPath(p))
	}
	return nil
}

// Execute runs ffmpeg for the plan, verifies the output and replaces the
// source with it. On any failure the source is left untouched and the
// temporary output is removed.
func Execute(ctx context.Context, p *plan.Plan, opts Options) (res Result, err error) {
	if err := Preflight(p); err != nil {
		return res, err
	}
	tmp := TempPath(p)
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	srcSize, _ := strconv.ParseInt(p.Info.Format.Size, 10, 64)
	started := time.Now()
	if err := run(ctx, opts.FFmpeg, Args(p, tmp), p.Info.Duration(), srcSize, opts.Progress); err != nil {
		return res, err
	}
	res.Took = time.Since(started)
	if err := verify(ctx, opts.FFprobe, p, tmp); err != nil {
		return res, fmt.Errorf("verifying output: %w", err)
	}

	if fi, err := os.Stat(p.Source); err == nil {
		res.OldSize = fi.Size()
	}
	if fi, err := os.Stat(tmp); err == nil {
		res.NewSize = fi.Size()
	}

	if err := os.Rename(tmp, p.Output); err != nil {
		return res, fmt.Errorf("installing output: %w", err)
	}
	res.Output = p.Output
	// Different name and the original is not wanted: remove it only now that
	// the output is in place, so there is never a moment with neither file.
	if p.Output != p.Source && opts.Original == config.OriginalDelete {
		if err := os.Remove(p.Source); err != nil {
			return res, fmt.Errorf("output installed as %s, but removing the original failed: %w", p.Output, err)
		}
		res.Deleted = true
	}
	return res, nil
}

func run(ctx context.Context, ffmpeg string, args []string, duration float64, srcSize int64, progress Progress) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting ffmpeg: %w", err)
	}
	start := time.Now()
	readProgress(stdout, duration, srcSize, func() time.Duration { return time.Since(start) }, progress)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("ffmpeg: %s", lastLines(msg, 5))
	}
	return nil
}

// readProgress consumes ffmpeg's -progress output: blocks of key=value lines,
// each closed by a "progress=continue" (or "progress=end") line.
func readProgress(r io.Reader, duration float64, srcSize int64, elapsed func() time.Duration, progress Progress) {
	var st Status
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), "=")
		if !ok || progress == nil {
			continue
		}
		switch key {
		case "out_time_us":
			// "N/A" until the first packet is written.
			if us, err := strconv.ParseFloat(val, 64); err == nil && us >= 0 && duration > 0 {
				st.Fraction = math.Min(us/1e6/duration, 1)
			}
		case "speed":
			st.Speed, _ = strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(val), "x"), 64)
		case "total_size":
			st.Written, _ = strconv.ParseInt(val, 10, 64)
		case "progress":
			st.Done = val == "end"
			if st.Done {
				st.Fraction = 1
			}
			st.Elapsed = elapsed()
			st.ETA = eta(st, duration)
			if secs := st.Elapsed.Seconds(); secs > 0 && srcSize > 0 {
				st.ReadRate = st.Fraction * float64(srcSize) / secs
			}
			progress(st)
		}
	}
}

// eta estimates the time left. ffmpeg's speed is an average over the whole
// run so far, so it is steadier than extrapolating from the fraction alone;
// the fraction is the fallback when no speed is reported.
func eta(st Status, duration float64) time.Duration {
	if st.Done || st.Fraction <= 0 {
		return 0
	}
	if st.Speed > 0 && duration > 0 {
		return time.Duration((1 - st.Fraction) * duration / st.Speed * float64(time.Second))
	}
	if st.Fraction >= 0.01 {
		return time.Duration(float64(st.Elapsed) * (1 - st.Fraction) / st.Fraction)
	}
	return 0
}

// verify checks that the output is a readable file with every planned stream
// and about the same duration as the source.
func verify(ctx context.Context, ffprobe string, p *plan.Plan, out string) error {
	info, err := media.Probe(ctx, ffprobe, out)
	if err != nil {
		return err
	}
	want := len(p.Kept())
	if got := len(info.Streams); got != want {
		return fmt.Errorf("expected %d streams, found %d", want, got)
	}
	for i, s := range p.Kept() {
		wantCodec := s.Stream.CodecName
		if s.Action == plan.Convert {
			wantCodec = s.Codec
		}
		if got := info.Streams[i].CodecName; got != wantCodec {
			return fmt.Errorf("stream #%d: expected %s, found %s", i, wantCodec, got)
		}
	}
	if src, dst := p.Info.Duration(), info.Duration(); src > 0 {
		if diff := math.Abs(src - dst); diff > math.Max(2, src*0.01) {
			return fmt.Errorf("duration %.1fs differs from source %.1fs", dst, src)
		}
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
