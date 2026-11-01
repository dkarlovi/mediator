package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	for out, want := range map[string]struct {
		version      string
		major, minor int
	}{
		"ffmpeg version 9.0.2-full_build-www.gyan.dev Copyright (c) 2000-2026": {"9.0.2-full_build-www.gyan.dev", 9, 0},
		"ffmpeg version 8.1.3 Copyright (c) 2000-2026 the FFmpeg developers":   {"8.1.3", 8, 1},
		"ffmpeg version n7.1 Copyright":                                        {"n7.1", 7, 1},
		"ffmpeg version 6.1.1-3ubuntu5 Copyright":                              {"6.1.1-3ubuntu5", 6, 1},
		"ffmpeg version N-118412-g1a2b3c4d5e Copyright":                        {"N-118412-g1a2b3c4d5e", 0, 0},
	} {
		v := parseVersion([]byte(out + "\nbuilt with gcc\n"))
		major, minor := parseMajorMinor(v)
		if v != want.version || major != want.major || minor != want.minor {
			t.Errorf("%q: got %q %d.%d, want %+v", out, v, major, minor, want)
		}
	}
}

func TestParseList(t *testing.T) {
	encoders := " V..... = Video\n A..... = Audio\n ------\n V....D libx264              libx264 H.264\n A....D aac                  AAC (Advanced Audio Coding)\n"
	if got, want := parseList([]byte(encoders), 1), map[string]bool{"libx264": true, "aac": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("encoders = %v", got)
	}
	muxers := "Formats:\n D.. = Demuxing supported\n .E. = Muxing supported\n ---\n  E  matroska        Matroska\n  E  mp4             MP4 (MPEG-4 Part 14)\n"
	if got, want := parseList([]byte(muxers), 1), map[string]bool{"matroska": true, "mp4": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("muxers = %v", got)
	}
}

func TestToolchainEncoderFallsBack(t *testing.T) {
	tc := NewToolchain("9.0", []string{"libaom-av1", "aac"}, nil)
	if e, ok := tc.Encoder("av1"); !ok || e != "libaom-av1" {
		t.Errorf("av1 -> %q, %v; want libaom-av1 when libsvtav1 is missing", e, ok)
	}
	if _, ok := tc.Encoder("h264"); ok {
		t.Errorf("h264 should be unavailable")
	}
	var none *Toolchain // nil assumes everything, for pure planning
	if e, ok := none.Encoder("av1"); !ok || e != "libsvtav1" {
		t.Errorf("nil toolchain: av1 -> %q, %v", e, ok)
	}
}

func TestUntested(t *testing.T) {
	for v, want := range map[string]bool{"9.0.2": false, "8.1.3": false, "7.1": true, "N-118412-gabc": true} {
		if got, _ := NewToolchain(v, nil, nil).Untested(); got != want {
			t.Errorf("%s: untested = %v, want %v", v, got, want)
		}
	}
}

func TestDetect(t *testing.T) {
	if _, err := Detect(context.Background(), "mediator-no-such-ffmpeg", "ffprobe"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing ffmpeg: err = %v", err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg is a shell script")
	}
	dir := t.TempDir()
	fake := func(name, script string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ffmpeg := fake("ffmpeg", `case "$*" in
*-version*) echo "ffmpeg version 6.1.2 Copyright";;
*-encoders*) printf ' ------\n A....D aac  AAC\n';;
*-muxers*) printf ' ---\n  E  matroska  Matroska\n';;
esac`)
	ffprobe := fake("ffprobe", `echo "ffprobe version 6.1.2 Copyright"`)
	tc, err := Detect(context.Background(), ffmpeg, ffprobe)
	if err != nil {
		t.Fatal(err)
	}
	if tc.Version != "6.1.2" || tc.ProbeVersion != "6.1.2" || tc.Major != 6 {
		t.Errorf("version: %+v", tc)
	}
	if !tc.HasEncoder("aac") || tc.HasEncoder("libx264") || !tc.HasMuxer("matroska") || tc.HasMuxer("mp4") {
		t.Errorf("capabilities: encoders %v muxers %v", tc.encoders, tc.muxers)
	}

	// And the real thing, when installed.
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		real, err := Detect(context.Background(), "ffmpeg", "ffprobe")
		if err != nil || !real.HasEncoder("aac") || !real.HasMuxer("matroska") {
			t.Errorf("real ffmpeg: %v %+v", err, real)
		}
	}
}
