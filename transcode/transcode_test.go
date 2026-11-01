package transcode

import (
	"context"
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
	dir := t.TempDir()
	src := filepath.Join(dir, "Episode.avi")
	gen := exec.Command("ffmpeg", "-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=2",
		"-f", "lavfi", "-i", "sine=f=440:duration=2",
		"-c:v", "mpeg4", "-c:a", "ac3", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generating sample: %v\n%s", err, out)
	}

	convert := func(t *testing.T, policy config.OriginalPolicy) Result {
		t.Helper()
		c := mustConfig(t, `
container: {preferred: [mkv]}
video: {preferred: [h264], convert: {preset: ultrafast}}
audio: {preferred: [aac]}
original: `+string(policy))
		info, err := media.Probe(context.Background(), c.FFprobe, src)
		if err != nil {
			t.Fatal(err)
		}
		p, err := plan.Build(info, c)
		if err != nil {
			t.Fatal(err)
		}
		res, err := Execute(context.Background(), p, Options{FFmpeg: c.FFmpeg, FFprobe: c.FFprobe, Original: c.Original})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(TempPath(p)); !os.IsNotExist(err) {
			t.Errorf("temp file left behind")
		}
		out, err := media.Probe(context.Background(), c.FFprobe, res.Output)
		if err != nil {
			t.Fatal(err)
		}
		if out.Streams[0].CodecName != "h264" || out.Streams[1].CodecName != "aac" {
			t.Errorf("output streams: %+v", out.Streams)
		}
		return res
	}

	// keep: original untouched, converted copy beside it.
	res := convert(t, config.OriginalKeep)
	if want := filepath.Join(dir, "Episode-mediator.mkv"); res.Output != want || res.Deleted {
		t.Errorf("keep: output = %s, deleted = %v", res.Output, res.Deleted)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("keep: source gone: %v", err)
	}
	if err := os.Remove(res.Output); err != nil {
		t.Fatal(err)
	}

	// delete: converted file takes the source's name, source removed.
	res = convert(t, config.OriginalDelete)
	if want := filepath.Join(dir, "Episode.mkv"); res.Output != want || !res.Deleted {
		t.Errorf("delete: output = %s, deleted = %v", res.Output, res.Deleted)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("delete: source not removed")
	}
}

func TestOutputPath(t *testing.T) {
	mkv, _ := media.LookupContainer("mkv")
	for _, tt := range []struct {
		src    string
		policy config.OriginalPolicy
		want   string
	}{
		{"/s/E01.mkv", config.OriginalKeep, "/s/E01-mediator.mkv"},
		{"/s/E01.avi", config.OriginalKeep, "/s/E01-mediator.mkv"},
		{"/s/E01-mediator.mkv", config.OriginalKeep, "/s/E01-mediator.mkv"}, // our own output: in place
		{"/s/E01.mkv", config.OriginalDelete, "/s/E01.mkv"},
		{"/s/E01.avi", config.OriginalDelete, "/s/E01.mkv"},
	} {
		if got := plan.OutputPath(tt.src, mkv, tt.policy); got != tt.want {
			t.Errorf("OutputPath(%s, %s) = %s, want %s", tt.src, tt.policy, got, tt.want)
		}
	}
}

func TestPreflightRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.avi")
	for _, f := range []string{src, filepath.Join(dir, "a.mkv")} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := mustConfig(t, "container: {preferred: [mkv]}\noriginal: delete")
	p, err := plan.Build(&media.Info{Path: src, Streams: []media.Stream{{Index: 0, CodecType: "video", CodecName: "h264"}}}, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := Preflight(p); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v", err)
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
	if s.ReadRate != 25000 {
		t.Errorf("ReadRate = %v", s.ReadRate)
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
