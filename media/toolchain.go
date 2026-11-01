package media

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// MinTestedMajor is the oldest ffmpeg major version mediator is tested
// against (8.1 and 9.0 so far). Older ones may well work, but get a warning.
const MinTestedMajor = 8

// Toolchain is what mediator found out about the installed ffmpeg.
type Toolchain struct {
	FFmpeg       string // resolved path
	FFprobe      string // resolved path
	Version      string // e.g. "9.0.2-full_build-www.gyan.dev"
	ProbeVersion string
	// Major and Minor are 0 when the version can't be parsed, as with git
	// snapshot builds ("N-118412-g...").
	Major, Minor int
	encoders     map[string]bool
	muxers       map[string]bool
}

// ErrNotFound is returned when ffmpeg or ffprobe can't be found.
var ErrNotFound = errors.New("not found")

// InstallHint tells the user how to get ffmpeg on this OS.
func InstallHint() string {
	switch runtime.GOOS {
	case "windows":
		return "install it with: winget install Gyan.FFmpeg (then open a new terminal)"
	case "darwin":
		return "install it with: brew install ffmpeg"
	}
	return "install it with your package manager, e.g. sudo apt install ffmpeg, or on Fedora sudo dnf install ffmpeg from RPM Fusion (Fedora's own ffmpeg-free lacks the x264/x265 encoders)"
}

// Detect locates ffmpeg and ffprobe and asks ffmpeg what it can do.
func Detect(ctx context.Context, ffmpeg, ffprobe string) (*Toolchain, error) {
	var tc Toolchain
	var err error
	if tc.FFmpeg, err = exec.LookPath(ffmpeg); err != nil {
		return nil, fmt.Errorf("ffmpeg (%s) %w; %s", ffmpeg, ErrNotFound, InstallHint())
	}
	if tc.FFprobe, err = exec.LookPath(ffprobe); err != nil {
		return nil, fmt.Errorf("ffprobe (%s) %w; it ships with ffmpeg, %s", ffprobe, ErrNotFound, InstallHint())
	}
	out, err := exec.CommandContext(ctx, tc.FFmpeg, "-hide_banner", "-version").Output()
	if err != nil {
		return nil, fmt.Errorf("running %s -version: %w", tc.FFmpeg, err)
	}
	tc.Version = parseVersion(out)
	tc.Major, tc.Minor = parseMajorMinor(tc.Version)
	if out, err := exec.CommandContext(ctx, tc.FFprobe, "-hide_banner", "-version").Output(); err == nil {
		tc.ProbeVersion = parseVersion(out)
	}
	if out, err = exec.CommandContext(ctx, tc.FFmpeg, "-hide_banner", "-encoders").Output(); err != nil {
		return nil, fmt.Errorf("listing ffmpeg encoders: %w", err)
	}
	tc.encoders = parseList(out, 1)
	if out, err = exec.CommandContext(ctx, tc.FFmpeg, "-hide_banner", "-muxers").Output(); err != nil {
		return nil, fmt.Errorf("listing ffmpeg muxers: %w", err)
	}
	tc.muxers = parseList(out, 1)
	return &tc, nil
}

var versionRe = regexp.MustCompile(`(?m)^\S+ version (\S+)`)

func parseVersion(out []byte) string {
	if m := versionRe.FindSubmatch(out); m != nil {
		return string(m[1])
	}
	return ""
}

var majorMinorRe = regexp.MustCompile(`^n?(\d+)\.(\d+)`)

func parseMajorMinor(v string) (int, int) {
	m := majorMinorRe.FindStringSubmatch(v)
	if m == nil {
		return 0, 0
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major, minor
}

// parseList reads the table ffmpeg prints for -encoders and -muxers: a
// legend, a dashed separator line, then one entry per line with the flags
// in the first column and the name in the column given.
func parseList(out []byte, column int) map[string]bool {
	names := map[string]bool{}
	started := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !started {
			started = strings.HasPrefix(line, "--")
			continue
		}
		if f := strings.Fields(line); len(f) > column {
			names[f[column]] = true
		}
	}
	return names
}

// NewToolchain describes an ffmpeg with the given encoders and muxers, for
// tests and for callers that already know what their ffmpeg can do.
func NewToolchain(version string, encoders, muxers []string) *Toolchain {
	tc := &Toolchain{Version: version, ProbeVersion: version, encoders: map[string]bool{}, muxers: map[string]bool{}}
	tc.Major, tc.Minor = parseMajorMinor(version)
	for _, e := range encoders {
		tc.encoders[e] = true
	}
	for _, m := range muxers {
		tc.muxers[m] = true
	}
	return tc
}

// HasEncoder reports whether ffmpeg has the named encoder (e.g. "libx264").
// A nil Toolchain has everything, which keeps pure planning code testable.
func (t *Toolchain) HasEncoder(name string) bool {
	return t == nil || t.encoders[name]
}

// HasMuxer reports whether ffmpeg can write the named format (e.g. "mp4").
func (t *Toolchain) HasMuxer(name string) bool {
	return t == nil || t.muxers[name]
}

// Encoder returns the encoder this ffmpeg will use to produce a codec: the
// first available of the codec's candidate encoders.
func (t *Toolchain) Encoder(codec string) (string, bool) {
	for _, e := range encoders[codec] {
		if t.HasEncoder(e) {
			return e, true
		}
	}
	return "", false
}

// Untested reports whether the ffmpeg version is unknown or older than
// mediator is tested against, and why.
func (t *Toolchain) Untested() (bool, string) {
	switch {
	case t.Major == 0:
		return true, fmt.Sprintf("ffmpeg version %q could not be parsed (a git snapshot?); mediator is tested with ffmpeg %d and newer", t.Version, MinTestedMajor)
	case t.Major < MinTestedMajor:
		return true, fmt.Sprintf("ffmpeg %s is older than mediator is tested with (%d and newer); upgrade if conversions fail", t.Version, MinTestedMajor)
	}
	return false, ""
}
