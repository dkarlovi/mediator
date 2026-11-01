package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"

	"github.com/dkarlovi/mediator/media"
	"gopkg.in/yaml.v3"
)

// FileName is the config file name mediator looks for.
const FileName = "mediator.yaml"

// OriginalPolicy is what happens to the source file once its converted
// version has been written and verified.
type OriginalPolicy string

const (
	// OriginalKeep leaves the source untouched and writes the converted file
	// beside it as "<name>-mediator.<ext>". This is the default.
	OriginalKeep OriginalPolicy = "keep"
	// OriginalDelete replaces the source: the converted file takes its name
	// (with the new extension if the container changed) and the source is
	// removed.
	OriginalDelete OriginalPolicy = "delete"
)

// ContainerConfig lists the acceptable containers, most preferred first.
type ContainerConfig struct {
	Preferred []string `yaml:"preferred"`
}

// ConvertConfig tunes the encoder used when a stream has to be converted. It
// never applies to streams that are already in a preferred codec.
type ConvertConfig struct {
	// Bitrate is the target bitrate, e.g. "192k". For audio, empty means 64k
	// per output channel. For video, empty means quality-based (CRF).
	Bitrate string `yaml:"bitrate,omitempty"`
	// Channels caps the channel count of converted audio, e.g. 2 to downmix
	// surround to stereo. 0 keeps the source layout.
	Channels int `yaml:"channels,omitempty"`
	// CRF is the video quality (lower is better). 0 means the encoder default
	// picked by mediator.
	CRF int `yaml:"crf,omitempty"`
	// Preset is the video encoder speed/size trade-off, e.g. "medium".
	Preset string `yaml:"preset,omitempty"`
}

// StreamConfig is the policy for one kind of stream.
type StreamConfig struct {
	// Preferred lists the acceptable codecs, most preferred first. A stream in
	// any of them is left alone; any other stream is converted to the first
	// one the target container can hold. Empty means "leave these streams
	// alone".
	Preferred []string      `yaml:"preferred"`
	Convert   ConvertConfig `yaml:"convert"`
}

// Config is the contents of mediator.yaml.
type Config struct {
	// Paths are scanned when none are given on the command line.
	Paths     []string        `yaml:"paths,omitempty"`
	Container ContainerConfig `yaml:"container"`
	Video     StreamConfig    `yaml:"video"`
	Audio     StreamConfig    `yaml:"audio"`
	Subtitles StreamConfig    `yaml:"subtitles"`
	Original  OriginalPolicy  `yaml:"original,omitempty"`
	// WorkDir is a local directory ffmpeg writes into before the result is
	// copied to its destination in one pass. Set it when files live on a
	// network share. Empty means write next to the source.
	WorkDir string `yaml:"workdir,omitempty"`
	FFmpeg  string `yaml:"ffmpeg,omitempty"`
	FFprobe string `yaml:"ffprobe,omitempty"`
}

var bitrateRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[kKmM]?$`)

// Normalize canonicalizes codec and container names, fills defaults and
// validates the config.
func (c *Config) Normalize() error {
	for i, name := range c.Container.Preferred {
		ct, ok := media.LookupContainer(name)
		if !ok {
			return fmt.Errorf("container.preferred: unknown container %q", name)
		}
		c.Container.Preferred[i] = ct.Name
	}
	for _, s := range []struct {
		name string
		cfg  *StreamConfig
	}{{"video", &c.Video}, {"audio", &c.Audio}, {"subtitles", &c.Subtitles}} {
		for i, codec := range s.cfg.Preferred {
			s.cfg.Preferred[i] = media.Canonical(codec)
		}
		if len(s.cfg.Preferred) > 0 {
			if _, ok := media.Encoder(s.cfg.Preferred[0]); !ok && s.name != "subtitles" {
				return fmt.Errorf("%s.preferred: mediator cannot encode %q, so it cannot be the first choice", s.name, s.cfg.Preferred[0])
			}
		}
		if b := s.cfg.Convert.Bitrate; b != "" && !bitrateRe.MatchString(b) {
			return fmt.Errorf("%s.convert.bitrate: %q is not a bitrate like 192k", s.name, b)
		}
		if s.cfg.Convert.Channels < 0 {
			return fmt.Errorf("%s.convert.channels: must be positive", s.name)
		}
	}
	switch c.Original {
	case "":
		c.Original = OriginalKeep
	case OriginalKeep, OriginalDelete:
	default:
		return fmt.Errorf("original: unknown policy %q (want %q or %q)", c.Original, OriginalKeep, OriginalDelete)
	}
	if c.WorkDir != "" {
		dir, err := filepath.Abs(os.ExpandEnv(c.WorkDir))
		if err != nil {
			return fmt.Errorf("workdir: %w", err)
		}
		c.WorkDir = dir
	}
	if c.FFmpeg == "" {
		c.FFmpeg = "ffmpeg"
	}
	if c.FFprobe == "" {
		c.FFprobe = "ffprobe"
	}
	return nil
}

// Parse reads a config from YAML.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	// Reject unknown keys: a typo such as "prefered" must not silently turn a
	// policy off.
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Load reads a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// userConfigDir is os.UserConfigDir, except that a relative $XDG_CONFIG_HOME
// falls back to ~/.config instead of disabling the lookup: Go rejects such a
// value outright, while the XDG spec says to ignore it.
func userConfigDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err == nil || runtime.GOOS == "windows" || runtime.GOOS == "darwin" || runtime.GOOS == "ios" || runtime.GOOS == "plan9" {
		return dir, err
	}
	home, herr := os.UserHomeDir()
	if herr != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// LoadPrefer loads the first config file that exists, in order:
//  1. the explicit path, if given,
//  2. ./mediator.yaml,
//  3. the user config dir: $XDG_CONFIG_HOME/mediator/mediator.yaml, else
//     ~/.config/mediator/mediator.yaml on Linux and other Unixes;
//     %AppData%\mediator\mediator.yaml on Windows;
//     ~/Library/Application Support/mediator/mediator.yaml on macOS.
func LoadPrefer(explicit string) (*Config, string, error) {
	if explicit != "" {
		cfg, err := Load(explicit)
		return cfg, explicit, err
	}
	candidates := []string{FileName}
	if dir, err := userConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, "mediator", FileName))
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			cfg, err := Load(p)
			return cfg, p, err
		}
	}
	return nil, "", fmt.Errorf("no config file found (tried: %v)", candidates)
}
