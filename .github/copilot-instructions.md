# Mediator

Mediator is a Go CLI that checks media files against preferred containers and
codecs from `mediator.yaml`, and converts only what differs using `ffmpeg`
(e.g. E-AC3/TrueHD audio -> AAC for Plex, keeping video and subtitles as-is).
It is dry-run by default; `--ack` performs the conversion.

Always reference these instructions first, and fall back to searching the code
only when something here does not match what you find.

## Build and test
- Requires Go (see `go.mod`) and, for integration tests and real use, `ffmpeg`
  and `ffprobe` on `PATH` (`sudo apt-get install -y ffmpeg`).
- Build: `go build -o mediator .`
- Windows build: `GOOS=windows GOARCH=amd64 go build -o mediator.exe .`
- Tests: `go test ./...` (the `transcode` integration test is skipped when
  ffmpeg is not installed).
- Before committing, ALWAYS run: `gofmt -l .` (must print nothing),
  `go vet ./...`, `staticcheck ./...`.

## Usage
```bash
cp mediator.yaml.dist mediator.yaml
./mediator run /path/to/Shows            # dry run: what would change
./mediator run /path/to/Shows --ack      # convert
./mediator probe -v /path/to/file.mkv    # plan + exact ffmpeg command
```
Config lookup: `--config`, then `./mediator.yaml`, then
`<os.UserConfigDir()>/mediator/mediator.yaml`. Unknown config keys are errors.

## Code structure
- `main.go`: application entry point (symfony-cli/console).
- `commands/`: CLI commands (`run`, `probe`) and terminal output, including
  the progress line.
- `config/`: `mediator.yaml` schema, normalization (codec aliases) and
  validation.
- `media/`: ffprobe JSON parsing (`probe.go`); codec aliases, candidate
  encoders and container capabilities (`codecs.go`); detecting the installed
  ffmpeg's version, encoders and muxers (`toolchain.go`); language codes
  (`lang.go`).
- `commands/doctor.go`: `mediator doctor` and the compatibility check every
  `run` prints before starting.
- `plan/`: the decision engine. It is pure (probe data + config + toolchain
  in, plan out) and must stay free of I/O, so it is unit-tested without
  ffmpeg. `BuildFor` plans for a detected toolchain; `Build` (nil toolchain)
  assumes every encoder exists.
- `transcode/`: plan -> ffmpeg arguments, execution with `-progress` parsing,
  verification by re-probing, and installing the output. The caller passes
  the output path (`Execute(ctx, plan, out, opts)`) and whether to remove the
  source; `transcode` must not learn about mediator's naming or `original`
  policy, so it stays reusable by other tools (e.g. fileferry). With
  `Options.WorkDir`, ffmpeg writes locally and the result is uploaded in a
  separate phase; progress is reported per `Phase` (convert, upload).
- `commands/naming.go`: mediator's output naming (`-mediator` suffix or
  replace), derived from the `original` policy.
- `scan/`: finding media files under the given paths, skipping mediator's
  temp files, dotfiles, and NAS recycle bin/snapshot/metadata folders.
- `Dockerfile`: distroless image with mediator and a static ffmpeg build;
  `.github/workflows/docker.yml` smoke-tests it (doctor plus a real
  conversion as a non-root user) before pushing to ghcr.io.

## Behaviour that must be preserved
- A stream in any codec of a `preferred` list is copied; any other stream is
  converted to the first preferred codec the target container can hold.
- Every stream is mapped explicitly with its own `-c:<n>`, never a blanket
  `-c:a`, so already-acceptable tracks are never re-encoded.
- Output is written as `<name>.mediator-tmp.<ext>` next to the source, verified
  (stream count, codecs, duration), then renamed into place. On any failure
  the source is left untouched.
- `original: keep` (default) writes `<name>-mediator.<ext>` beside the source;
  `original: delete` replaces the source only after the output is in place.
- `languages` drops audio/subtitle tracks in other languages, but never
  untagged tracks, and never all audio tracks of a file.
- With `original: delete`, an existing `-mediator` copy is installed
  (`transcode.Adopt`, after `Verify` against the current plan) instead of
  converting again; one that no longer matches is reconverted and removed.
- Runs are idempotent: a converted file, or a source whose `-mediator` copy
  exists, is not converted again.
