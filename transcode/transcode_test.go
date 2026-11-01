package transcode

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dkarlovi/mediator/config"
	"github.com/dkarlovi/mediator/media"
	"github.com/dkarlovi/mediator/plan"
)

func mustConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestArgs(t *testing.T) {
	c := mustConfig(t, `
container: {preferred: [mp4]}
audio: {preferred: [aac], convert: {bitrate: 192k, channels: 2}}
subtitles: {preferred: [srt]}
`)
	p, err := plan.Build(&media.Info{Path: "/in/a.mkv", Streams: []media.Stream{
		{Index: 0, CodecType: "video", CodecName: "hevc"},
		{Index: 1, CodecType: "attachment", CodecName: "ttf"},
		{Index: 2, CodecType: "audio", CodecName: "eac3", Channels: 6},
		{Index: 3, CodecType: "audio", CodecName: "aac", Channels: 2},
		{Index: 4, CodecType: "subtitle", CodecName: "subrip"},
	}}, c)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(Args(p, "/in/a.mediator-tmp.mp4"), " ")
	// The attachment (input #1) is dropped, so output indexes shift down.
	for _, want := range []string{
		"-map 0:0 -c:0 copy -tag:0 hvc1",
		"-map 0:2 -c:1 aac -ac:1 2 -b:1 192k",
		"-map 0:3 -c:2 copy",
		"-map 0:4 -c:3 mov_text",
		"-movflags +faststart",
		"-f mp4 /in/a.mediator-tmp.mp4",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("args missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "0:1") {
		t.Errorf("dropped attachment was mapped: %s", got)
	}
}

// TestExecute runs real ffmpeg on a generated file.
func TestExecute(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	c := mustConfig(t, `
container: {preferred: [mkv]}
video: {preferred: [h264], convert: {preset: ultrafast}}
audio: {preferred: [aac]}
`)
	sample := func(t *testing.T) (string, *plan.Plan) {
		t.Helper()
		src := filepath.Join(t.TempDir(), "Episode.avi")
		gen := exec.Command("ffmpeg", "-v", "error",
			"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=2",
			"-f", "lavfi", "-i", "sine=f=440:duration=2",
			"-c:v", "mpeg4", "-c:a", "ac3", src)
		if out, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("generating sample: %v\n%s", err, out)
		}
		info, err := media.Probe(context.Background(), c.FFprobe, src)
		if err != nil {
			t.Fatal(err)
		}
		p, err := plan.Build(info, c)
		if err != nil {
			t.Fatal(err)
		}
		return src, p
	}
	execute := func(t *testing.T, p *plan.Plan, out string, remove bool) Result {
		t.Helper()
		res, err := Execute(context.Background(), p, out, Options{FFmpeg: c.FFmpeg, FFprobe: c.FFprobe, RemoveSource: remove})
		if err != nil {
			t.Fatal(err)
		}
		if res.Output != out {
			t.Errorf("output = %s, want %s", res.Output, out)
		}
		if exists(TempPath(out)) {
			t.Errorf("temp file left behind")
		}
		info, err := media.Probe(context.Background(), c.FFprobe, out)
		if err != nil {
			t.Fatal(err)
		}
		if info.Streams[0].CodecName != "h264" || info.Streams[1].CodecName != "aac" {
			t.Errorf("output streams: %+v", info.Streams)
		}
		return res
	}

	t.Run("beside the source, keeping it", func(t *testing.T) {
		src, p := sample(t)
		res := execute(t, p, filepath.Join(filepath.Dir(src), "Episode-converted.mkv"), false)
		if res.Deleted || !exists(src) {
			t.Errorf("source must be kept")
		}
	})
	t.Run("replacing the source", func(t *testing.T) {
		src, p := sample(t)
		res := execute(t, p, filepath.Join(filepath.Dir(src), "Episode.mkv"), true)
		if !res.Deleted || exists(src) {
			t.Errorf("source must be removed")
		}
	})
	t.Run("copying B-frame h264 out of avi", func(t *testing.T) {
		// AVI has no presentation timestamps; muxing copied H.264 with
		// B-frames into MKV fails unless ffmpeg generates them.
		src := filepath.Join(t.TempDir(), "Rip.avi")
		gen := exec.Command("ffmpeg", "-v", "error",
			"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=2",
			"-f", "lavfi", "-i", "sine=f=440:duration=2",
			"-c:v", "libx264", "-bf", "3", "-c:a", "ac3", src)
		if out, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("generating sample: %v\n%s", err, out)
		}
		info, err := media.Probe(context.Background(), c.FFprobe, src)
		if err != nil {
			t.Fatal(err)
		}
		p, err := plan.Build(info, c)
		if err != nil {
			t.Fatal(err)
		}
		if p.Streams[0].Action != plan.Copy {
			t.Fatalf("video should be copied, got %s", p.Streams[0].Action)
		}
		execute(t, p, filepath.Join(filepath.Dir(src), "Rip.mkv"), true)
	})
	t.Run("through a work directory", func(t *testing.T) {
		// Force the cross-volume path: the work file must be copied, not
		// renamed, into place.
		renameFile = func(string, string) error { return errors.New("cross-device link") }
		defer func() { renameFile = os.Rename }()

		src, p := sample(t)
		work := t.TempDir()
		out := filepath.Join(filepath.Dir(src), "Episode.mkv")
		var phases []string
		res, err := Execute(context.Background(), p, out, Options{
			FFmpeg: c.FFmpeg, FFprobe: c.FFprobe, WorkDir: work, RemoveSource: true,
			Progress: func(st Status) {
				if st.Done {
					phases = append(phases, string(st.Phase))
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(phases, ","); got != "convert,upload" {
			t.Errorf("finished phases = %q", got)
		}
		if res.Output != out || !res.Deleted || exists(src) || exists(TempPath(out)) {
			t.Errorf("result %+v", res)
		}
		if left, _ := os.ReadDir(work); len(left) != 0 {
			t.Errorf("work directory not cleaned up: %v", left)
		}
		info, err := media.Probe(context.Background(), c.FFprobe, out)
		if err != nil || info.Streams[0].CodecName != "h264" {
			t.Errorf("uploaded output: %v %+v", err, info)
		}
	})
	t.Run("work directory is cleaned up on failure", func(t *testing.T) {
		src, p := sample(t)
		work := t.TempDir()
		out := filepath.Join(filepath.Dir(src), "Episode.mkv")
		_, err := Execute(context.Background(), p, out, Options{FFmpeg: "false", FFprobe: c.FFprobe, WorkDir: work})
		if err == nil {
			t.Fatal("expected ffmpeg failure")
		}
		if left, _ := os.ReadDir(work); len(left) != 0 {
			t.Errorf("work directory not cleaned up: %v", left)
		}
		if !exists(src) || exists(out) || exists(TempPath(out)) {
			t.Errorf("source must be untouched and no output left")
		}
	})
	t.Run("into a new directory elsewhere", func(t *testing.T) {
		// What a caller with its own target layout (fileferry) would do.
		src, p := sample(t)
		out := filepath.Join(t.TempDir(), "2024", "2024-06-02", "Episode.mkv")
		execute(t, p, out, false)
		if !exists(src) {
			t.Errorf("source must be kept")
		}
	})
}

func TestTempPath(t *testing.T) {
	got := TempPath(filepath.Join("s", "E01-mediator.mkv"))
	if want := filepath.Join("s", "E01-mediator.mediator-tmp.mkv"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestPreflight(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.avi")
	taken := filepath.Join(dir, "a.mkv")
	for _, f := range []string{src, taken} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := mustConfig(t, `container: {preferred: [mkv]}`)
	p, err := plan.Build(&media.Info{Path: src, Streams: []media.Stream{{Index: 0, CodecType: "video", CodecName: "h264"}}}, c)
	if err != nil {
		t.Fatal(err)
	}
	for out, want := range map[string]string{
		taken:                          "already exists",
		filepath.Join(dir, "a.mp4"):    "planned .mkv extension",
		filepath.Join(dir, "free.mkv"): "",
	} {
		err := Preflight(p, out)
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("Preflight(%s) = %v, want %q", filepath.Base(out), err, want)
		}
	}
}

func TestReadProgress(t *testing.T) {
	// Two blocks as ffmpeg prints them with -progress, for a 100s file of
	// 1,000,000 bytes, read 10s apart.
	in := strings.Join([]string{
		"frame=100", "total_size=N/A", "out_time_us=N/A", "speed=N/A", "progress=continue",
		"frame=500", "total_size=250000", "out_time_us=25000000", "speed=2.5x", "progress=continue",
		"total_size=900000", "out_time_us=100000000", "speed=2.5x", "progress=end",
	}, "\n")
	ticks := []time.Duration{time.Second, 10 * time.Second, 40 * time.Second}
	var got []Status
	readProgress(strings.NewReader(in), 100, 1_000_000, func() time.Duration {
		d := ticks[0]
		ticks = ticks[1:]
		return d
	}, func(s Status) { got = append(got, s) })

	if len(got) != 3 {
		t.Fatalf("got %d updates", len(got))
	}
	if s := got[0]; s.Fraction != 0 || s.ETA != 0 || s.Speed != 0 {
		t.Errorf("before first packet: %+v", s)
	}
	s := got[1]
	if s.Fraction != 0.25 || s.Speed != 2.5 || s.Written != 250000 {
		t.Errorf("mid: %+v", s)
	}
	// 75s of media left at 2.5x = 30s.
	if s.ETA != 30*time.Second {
		t.Errorf("ETA = %v", s.ETA)
	}
	// 25% of 1MB in 10s = 25kB/s.
	if s.Rate != 25000 {
		t.Errorf("Rate = %v", s.Rate)
	}
	if s := got[2]; !s.Done || s.Fraction != 1 || s.ETA != 0 {
		t.Errorf("end: %+v", s)
	}
}

func TestETAFallsBackToElapsed(t *testing.T) {
	// No speed reported: 20% done in 10s -> 40s left.
	if got := eta(Status{Fraction: 0.2, Elapsed: 10 * time.Second}, 100); got != 40*time.Second {
		t.Errorf("eta = %v", got)
	}
}

func TestCopyWithProgress(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 9<<20) // spans several 4 MiB blocks
	var dst bytes.Buffer
	var last Status
	if err := copyWithProgress(context.Background(), &dst, bytes.NewReader(data), int64(len(data)), func(st Status) { last = st }); err != nil {
		t.Fatal(err)
	}
	if dst.Len() != len(data) {
		t.Errorf("copied %d of %d bytes", dst.Len(), len(data))
	}
	if !last.Done || last.Phase != PhaseUpload || last.Fraction != 1 || last.Written != int64(len(data)) || last.ETA != 0 {
		t.Errorf("final status %+v", last)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyWithProgress(ctx, &dst, bytes.NewReader(data), int64(len(data)), func(Status) {}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled copy: err = %v", err)
	}
}
