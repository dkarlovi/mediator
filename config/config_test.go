package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParseNormalizes(t *testing.T) {
	c, err := Parse([]byte(`
container: {preferred: [MKV, .mp4]}
video: {preferred: [H265, x264]}
subtitles: {preferred: [srt, pgs]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mkv", "mp4"}; !reflect.DeepEqual(c.Container.Preferred, want) {
		t.Errorf("container = %v", c.Container.Preferred)
	}
	if want := []string{"hevc", "h264"}; !reflect.DeepEqual(c.Video.Preferred, want) {
		t.Errorf("video = %v", c.Video.Preferred)
	}
	if want := []string{"subrip", "hdmv_pgs_subtitle"}; !reflect.DeepEqual(c.Subtitles.Preferred, want) {
		t.Errorf("subtitles = %v", c.Subtitles.Preferred)
	}
	if c.Original != OriginalKeep || c.FFmpeg != "ffmpeg" || c.FFprobe != "ffprobe" || c.WorkDir != "" {
		t.Errorf("defaults not applied: %+v", c)
	}
}

func TestParseErrors(t *testing.T) {
	for yaml, want := range map[string]string{
		`container: {preferred: [flv]}`:          "unknown container",
		`audio: {preferred: [truehd]}`:           "cannot encode",
		`audio: {convert: {bitrate: loud}}`:      "not a bitrate",
		`original: shred`:                        "unknown policy",
		`audio: {prefered: [aac]}`:               "field prefered not found",
		``:                                       "", // empty config is valid, does nothing
		`audio: {convert: {channels: -1}}`:       "must be positive",
		`video: {preferred: [mpeg2video, h264]}`: "cannot encode",
		`video: {languages: [eng]}`:              "audio and subtitles only",
		`audio: {languages: [english]}`:          "not a language code",
	} {
		_, err := Parse([]byte(yaml))
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", yaml, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: error = %v, want %q", yaml, err, want)
		}
	}
}

func TestLoadPreferLookupOrder(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("XDG lookup applies to Linux and other Unixes")
	}
	root := t.TempDir()
	write := func(path string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("audio: {preferred: [aac]}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	home := filepath.Join(root, "home")
	xdg := filepath.Join(root, "xdg")
	homeCfg := write(filepath.Join(home, ".config", "mediator", FileName))
	xdgCfg := write(filepath.Join(xdg, "mediator", FileName))
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir()) // no ./mediator.yaml

	for _, tt := range []struct{ xdg, want string }{
		{xdg, xdgCfg},
		{"", homeCfg},
		{"relative/dir", homeCfg}, // spec: ignore relative paths
	} {
		t.Setenv("XDG_CONFIG_HOME", tt.xdg)
		_, got, err := LoadPrefer("")
		if err != nil || got != tt.want {
			t.Errorf("XDG_CONFIG_HOME=%q: got %q, %v; want %q", tt.xdg, got, err, tt.want)
		}
	}

	// ./mediator.yaml beats the user config dir, --config beats both.
	t.Setenv("XDG_CONFIG_HOME", xdg)
	write(FileName)
	if _, got, _ := LoadPrefer(""); got != FileName {
		t.Errorf("cwd config not preferred: %q", got)
	}
	if _, got, _ := LoadPrefer(homeCfg); got != homeCfg {
		t.Errorf("explicit config not preferred: %q", got)
	}
}

func TestWorkDirIsExpandedAndAbsolute(t *testing.T) {
	t.Setenv("MEDIATOR_TEST_WORK", "scratch")
	c, err := Parse([]byte("workdir: ${MEDIATOR_TEST_WORK}/mediator\n"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(filepath.Join("scratch", "mediator"))
	if c.WorkDir != want {
		t.Errorf("workdir = %q, want %q", c.WorkDir, want)
	}
}

func TestLanguagesAreCanonicalized(t *testing.T) {
	c, err := Parse([]byte("audio: {languages: [EN, ger, fra]}\nsubtitles: {languages: [hr]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"eng", "deu", "fra"}; !reflect.DeepEqual(c.Audio.Languages, want) {
		t.Errorf("audio = %v", c.Audio.Languages)
	}
	if want := []string{"hrv"}; !reflect.DeepEqual(c.Subtitles.Languages, want) {
		t.Errorf("subtitles = %v", c.Subtitles.Languages)
	}
}
