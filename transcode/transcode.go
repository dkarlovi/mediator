// Package transcode turns a plan into an ffmpeg run, verifies the result and
// installs it at the output path the caller chose.
//
// It deliberately knows nothing about how outputs are named or whether
// originals are kept; that is the caller's policy. mediator's CLI decides it
// from mediator.yaml, and another tool (fileferry, say) can decide it from its
// own target templates.
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

	"github.com/dkarlovi/mediator/media"
	"github.com/dkarlovi/mediator/plan"
)

// TempMarker is part of every in-progress output file name, so a scan never
// picks up a half-written file left behind by an interrupted run.
const TempMarker = ".mediator-tmp."

// TempPath is where the output for out is written while ffmpeg runs. It lives
// next to out so installing it is a rename on the same volume, never a second
// copy across the network.
func TempPath(out string) string {
	dir, base := filepath.Split(out)
	ext := filepath.Ext(base)
	return filepath.Join(dir, strings.TrimSuffix(base, ext)+TempMarker+strings.TrimPrefix(ext, "."))
}

// Covers returns where each re-attached cover of the plan is extracted to
// inside dir, keyed by input stream index.
func Covers(p *plan.Plan, dir string) map[int]string {
	covers := map[int]string{}
	for _, s := range p.Kept() {
		if s.Reattach {
			ext, _, _ := media.CoverFile(s.Stream.CodecName)
			covers[s.Stream.Index] = filepath.Join(dir, fmt.Sprintf("cover-%d%s", s.Stream.Index, ext))
		}
	}
	return covers
}

// ExtractArgs builds the ffmpeg arguments that copy one cover image out of the
// source, byte for byte, so it can be re-attached.
func ExtractArgs(p *plan.Plan, index int, path string) []string {
	return []string{"-hide_banner", "-nostdin", "-y", "-v", "error",
		"-i", p.Source, "-map", "0:" + strconv.Itoa(index), "-c", "copy", "-frames:v", "1", "-f", "image2", path}
}

// Args builds the ffmpeg arguments that execute a plan into out. covers are
// the extracted cover images (see Covers), attached instead of mapped.
func Args(p *plan.Plan, out string, covers map[int]string) []string {
	args := []string{
		"-hide_banner", "-nostdin", "-y",
		"-v", "error",
		"-progress", "pipe:1", "-nostats",
	}
	if !p.Src.Timestamps {
		// The source has no presentation timestamps (AVI): without generated
		// ones, copying B-frame video into MKV/MP4 fails with "Can't write
		// packet with unknown timestamp".
		args = append(args, "-fflags", "+genpts")
	}
	args = append(args,
		"-i", p.Source,
		"-map_metadata", "0",
		"-map_chapters", "0",
	)
	for i, s := range p.Kept() {
		o := strconv.Itoa(i)
		if s.Reattach {
			_, mimetype, _ := media.CoverFile(s.Stream.CodecName)
			name := s.Stream.Tag("filename")
			if name == "" {
				name = filepath.Base(covers[s.Stream.Index])
			}
			args = append(args, "-attach", covers[s.Stream.Index],
				"-metadata:s:"+o, "mimetype="+mimetype, "-metadata:s:"+o, "filename="+name)
			continue
		}
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
		if s.MakeDefault {
			args = append(args, "-disposition:"+o, "+default")
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

// Phase is a step of Execute that reports progress.
type Phase string

const (
	// PhaseConvert is ffmpeg reading the source and writing the output
	// (into the work directory, when one is set).
	PhaseConvert Phase = "convert"
	// PhaseUpload is copying the output from the work directory to its
	// destination.
	PhaseUpload Phase = "upload"
)

// Status is a snapshot of a running phase.
type Status struct {
	Phase Phase
	// Fraction is how much of the phase is done, 0..1.
	Fraction float64
	// Speed is ffmpeg's processing speed relative to playback (2.0 = twice
	// real time), or 0 when ffmpeg hasn't reported one yet. Convert only.
	Speed float64
	// Elapsed is the wall-clock time since the phase started.
	Elapsed time.Duration
	// ETA is the estimated time left, or 0 when it can't be estimated yet.
	ETA time.Duration
	// Rate is bytes per second: the source being read during convert
	// (estimated from the fraction done and the source size), the output
	// being written during upload.
	Rate float64
	// Written is the output size so far, in bytes.
	Written int64
	// Done is set on a phase's final update.
	Done bool
}

// Progress receives a status update roughly twice a second, and a final one
// with Done set when each phase finishes.
type Progress func(Status)

// Options control a single execution.
type Options struct {
	FFmpeg  string
	FFprobe string
	// RemoveSource deletes the plan's source once the output is installed at a
	// different path. When the output path is the source itself, the source is
	// always replaced.
	RemoveSource bool
	// WorkDir, when set, is a local directory ffmpeg writes into. The finished
	// output is then copied to its destination in one sequential pass, so a
	// network share never has to serve the read and the write at the same
	// time.
	WorkDir  string
	Progress Progress
}

// Result describes what Execute did.
type Result struct {
	Output string
	// Deleted is set when the source was removed (Options.RemoveSource).
	Deleted bool
	OldSize int64
	NewSize int64
	// Took is how long the whole execution ran, all phases included.
	Took time.Duration
}

// Preflight reports problems Execute would hit before any work is done, so a
// dry run can show them too. out may be the plan's source (replace in place);
// any other existing file is never overwritten.
func Preflight(p *plan.Plan, out string) error {
	if filepath.Ext(out) != "."+p.Dst.Ext {
		return fmt.Errorf("output %s does not have the planned .%s extension", out, p.Dst.Ext)
	}
	if out != p.Source {
		if _, err := os.Stat(out); err == nil {
			return fmt.Errorf("output %s already exists", out)
		}
	}
	if tmp := TempPath(out); exists(tmp) {
		return fmt.Errorf("leftover temporary file %s exists (from an interrupted run?); remove it first", tmp)
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Execute runs ffmpeg for the plan, verifies the output and installs it at
// out, creating out's directory if needed. On any failure the source is left
// untouched and every temporary file is removed.
func Execute(ctx context.Context, p *plan.Plan, out string, opts Options) (res Result, err error) {
	started := time.Now()
	if err := Preflight(p, out); err != nil {
		return res, err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return res, fmt.Errorf("creating output directory: %w", err)
	}
	progress := opts.Progress
	if progress == nil {
		progress = func(Status) {}
	}

	// tmp is the output's temporary name next to out; work is where ffmpeg
	// writes, which is tmp itself unless a work directory is used.
	tmp := TempPath(out)
	work := tmp
	if opts.WorkDir != "" {
		if work, err = workFile(opts.WorkDir, out); err != nil {
			return res, err
		}
	}
	defer func() {
		if work != tmp {
			_ = os.Remove(work)
		}
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	var covers map[int]string
	if c := Covers(p, ""); len(c) > 0 {
		dir, err := os.MkdirTemp(opts.WorkDir, "mediator-covers-")
		if err != nil {
			return res, fmt.Errorf("creating a folder for cover art: %w", err)
		}
		defer os.RemoveAll(dir)
		covers = Covers(p, dir)
		for index, path := range covers {
			if out, err := exec.CommandContext(ctx, opts.FFmpeg, ExtractArgs(p, index, path)...).CombinedOutput(); err != nil {
				return res, fmt.Errorf("extracting cover art (stream #%d): %v: %s", index, err, lastLines(strings.TrimSpace(string(out)), 3))
			}
			if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
				return res, fmt.Errorf("extracting cover art (stream #%d): no image written", index)
			}
		}
	}

	srcSize, _ := strconv.ParseInt(p.Info.Format.Size, 10, 64)
	if err := run(ctx, opts.FFmpeg, Args(p, work, covers), p.Info.Duration(), srcSize, progress); err != nil {
		return res, err
	}
	if err := verify(ctx, opts.FFprobe, p, work); err != nil {
		return res, fmt.Errorf("verifying output: %w", err)
	}
	if fi, err := os.Stat(p.Source); err == nil {
		res.OldSize = fi.Size()
	}
	if fi, err := os.Stat(work); err == nil {
		res.NewSize = fi.Size()
	}

	if work != tmp {
		if err := upload(ctx, work, tmp, progress); err != nil {
			return res, fmt.Errorf("uploading output: %w", err)
		}
		// The copy is checked by size and re-probed, which reads only the
		// container headers; a full checksum would mean downloading the whole
		// file again.
		if fi, err := os.Stat(tmp); err != nil || fi.Size() != res.NewSize {
			return res, fmt.Errorf("uploaded output is incomplete")
		}
		if err := verify(ctx, opts.FFprobe, p, tmp); err != nil {
			return res, fmt.Errorf("verifying uploaded output: %w", err)
		}
	}

	if err := os.Rename(tmp, out); err != nil {
		return res, fmt.Errorf("installing output: %w", err)
	}
	res.Output = out
	res.Took = time.Since(started)
	// Remove the source only now that the output is in place, so there is
	// never a moment with neither file.
	if out != p.Source && opts.RemoveSource {
		if err := os.Remove(p.Source); err != nil {
			return res, fmt.Errorf("output installed as %s, but removing the source failed: %w", out, err)
		}
		res.Deleted = true
	}
	return res, nil
}

// renameFile is os.Rename, replaceable in tests to force the copy path a
// work directory on another volume takes.
var renameFile = os.Rename

// Verify checks that an existing file is what executing the plan would
// produce: every planned stream, in the planned codec, with the source's
// duration.
func Verify(ctx context.Context, ffprobe string, p *plan.Plan, path string) error {
	return verify(ctx, ffprobe, p, path)
}

// Adopt installs a file converted earlier (by a run that kept the original
// beside it) at out, instead of converting the source again. The file must
// pass Verify against the current plan. Like Execute, it never overwrites a
// file other than the source, and removes the source only once the output is
// in place.
func Adopt(ctx context.Context, p *plan.Plan, converted, out string, opts Options) (res Result, err error) {
	started := time.Now()
	if err := Verify(ctx, opts.FFprobe, p, converted); err != nil {
		return res, fmt.Errorf("%s does not match the current config: %w", filepath.Base(converted), err)
	}
	if out != p.Source && exists(out) {
		return res, fmt.Errorf("output %s already exists", out)
	}
	if fi, err := os.Stat(p.Source); err == nil {
		res.OldSize = fi.Size()
	}
	if fi, err := os.Stat(converted); err == nil {
		res.NewSize = fi.Size()
	}
	if err := os.Rename(converted, out); err != nil {
		return res, fmt.Errorf("installing %s: %w", filepath.Base(converted), err)
	}
	res.Output = out
	res.Took = time.Since(started)
	if out != p.Source && opts.RemoveSource {
		if err := os.Remove(p.Source); err != nil {
			return res, fmt.Errorf("output installed as %s, but removing the source failed: %w", out, err)
		}
		res.Deleted = true
	}
	return res, nil
}

// workFile reserves a unique file in the work directory for out, so two runs
// (or two files with the same name from different folders) never collide.
func workFile(dir, out string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating work directory: %w", err)
	}
	base := filepath.Base(out)
	ext := filepath.Ext(base)
	f, err := os.CreateTemp(dir, strings.TrimSuffix(base, ext)+".*"+TempMarker+strings.TrimPrefix(ext, "."))
	if err != nil {
		return "", fmt.Errorf("creating work file: %w", err)
	}
	name := f.Name()
	return name, f.Close()
}

// upload moves src to dst. On the same volume that is a rename; otherwise it
// is a sequential copy in large blocks, which is what network shares handle
// best.
func upload(ctx context.Context, src, dst string, progress Progress) error {
	if err := renameFile(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	outF, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := copyWithProgress(ctx, outF, in, fi.Size(), progress); err != nil {
		outF.Close()
		return err
	}
	// Write-behind errors on a network share only surface here.
	if err := outF.Sync(); err != nil {
		outF.Close()
		return err
	}
	return outF.Close()
}

// copyWithProgress copies size bytes, reporting PhaseUpload progress at most
// every 250ms and once more when done.
func copyWithProgress(ctx context.Context, w io.Writer, r io.Reader, size int64, progress Progress) error {
	buf := make([]byte, 4<<20)
	start := time.Now()
	var last time.Time
	var written int64
	report := func(done bool) {
		st := Status{Phase: PhaseUpload, Written: written, Elapsed: time.Since(start), Done: done}
		if size > 0 {
			st.Fraction = math.Min(float64(written)/float64(size), 1)
		}
		if secs := st.Elapsed.Seconds(); secs > 0 {
			st.Rate = float64(written) / secs
			if st.Rate > 0 && !done {
				st.ETA = time.Duration(float64(size-written) / st.Rate * float64(time.Second))
			}
		}
		progress(st)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := r.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			written += int64(n)
			if time.Since(last) >= 250*time.Millisecond {
				last = time.Now()
				report(false)
			}
		}
		if rerr == io.EOF {
			report(true)
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
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
	st := Status{Phase: PhaseConvert}
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
				st.Rate = st.Fraction * float64(srcSize) / secs
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
	return verifyStreams(p, info)
}

// verifyStreams checks a probed output against the plan.
func verifyStreams(p *plan.Plan, info *media.Info) error {
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
		if s.Stream.IsCoverArt() && !info.Streams[i].IsCoverArt() {
			return fmt.Errorf("stream #%d: cover art came out as a regular video track", i)
		}
		if s.Reattach && info.Streams[i].CoverLost() {
			return fmt.Errorf("stream #%d: cover art was written as a video track, not an attachment", i)
		}
	}
	return verifyDuration(p, info)
}

// verifyDuration checks the output runs as long as the source streams it
// keeps. A source file is as long as its longest stream, and that may be one
// the plan drops (a dub or subtitle track running past the credits), so the
// file's duration is only a fallback for when no stream reports its own.
func verifyDuration(p *plan.Plan, out *media.Info) error {
	tolerance := func(d float64) float64 { return math.Max(2, d*0.01) }
	want := 0.0
	for i, s := range p.Kept() {
		if !timed(s.Stream) {
			continue
		}
		src, dst := s.Stream.Duration(), out.Streams[i].Duration()
		want = math.Max(want, src)
		if src > 0 && dst > 0 && math.Abs(src-dst) > tolerance(src) {
			return fmt.Errorf("stream #%d: duration %.1fs differs from source %.1fs", i, dst, src)
		}
	}
	if want == 0 {
		want = p.Info.Duration()
	}
	if got := out.Duration(); want > 0 && math.Abs(want-got) > tolerance(want) {
		return fmt.Errorf("duration %.1fs differs from the kept source streams' %.1fs", got, want)
	}
	return nil
}

// timed reports whether a stream's duration says something about the file's:
// audio and video do; cover art, attachments and subtitles (whose last cue
// can end anywhere) don't.
func timed(s media.Stream) bool {
	return (s.CodecType == "video" && !s.IsCoverArt()) || s.CodecType == "audio"
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
