## Mediator: reconcile media files with what your player supports

Mediator checks media files against a short list of containers and codecs you
accept. It converts only what doesn't match, using `ffmpeg`.

The typical case is a Plex library. An `.mkv` with TrueHD or E-AC3 audio forces
Plex to transcode on every play. You'd rather convert it once, to AAC, and keep
the video, the other audio tracks and the subtitles exactly as they are.

### What it does
- Probes every media file under the given paths with `ffprobe`.
- Decides per stream: keep it, convert it, or drop it if the target container
  can't hold it.
- Dry run by default: prints what it would do. With `--ack` it does it.
- Copies everything it doesn't need to touch (`-c copy`), so a file with one
  wrong audio track is a fast remux plus one audio encode, never a video
  re-encode.
- Leaves your originals alone by default: `Episode.mkv` gets a converted
  sibling `Episode-mediator.mkv`.
- Is idempotent: a converted file matches the config, and a source that already
  has its `-mediator` copy is skipped, so the next run has nothing to do.

### Quick start

```bash
go build -o mediator .
cp mediator.yaml.dist mediator.yaml

./mediator run "/mnt/media/Shows/Example Show/Season 3"         # dry run
./mediator run "/mnt/media/Shows/Example Show/Season 3" --ack   # convert
```

On Windows, network shares work directly as UNC paths:

```powershell
mediator.exe run "\\NAS\Shows\Example Show\Season 3"
```

Example dry run:

```
Found 4 media files

/mnt/media/Shows/Example Show/Season 3
  S03E01 - Episode One.mkv
    container   mkv                          keep
    video #0    h264 1920x1080               keep
    audio #1    eac3 5.1 eng                 -> aac stereo @ 192k
    audio #2    ac3 5.1 spa                  -> aac stereo @ 192k
    subs #3     subrip eng                   keep
  S03E03 - Old Rip.avi
    container   avi                          -> mkv (S03E03 - Old Rip-mediator.mkv)
    video #0    mpeg4 720x480                -> h264
    audio #1    mp3 stereo                   keep
    ! stream #0: converting video (mpeg4 -> h264) is slow and lossy

----------------------------------------
Would convert: 2  Already OK: 2  Errors: 0
Dry run, nothing was changed. Use --ack to convert.
```

`mediator probe <file>` shows the plan for one file. Add `-v` to also see the
exact `ffmpeg` command it would run.

`mediator doctor` checks the installed ffmpeg against your config:

```
ffmpeg     9.0.2-full_build-www.gyan.dev  C:\...\ffmpeg.exe
ffprobe    9.0.2-full_build-www.gyan.dev  C:\...\ffprobe.exe
config     C:\Users\you\AppData\Roaming\mediator\mediator.yaml

container  mkv ✓   mp4 ✓
video      h264 ✓ libx264   hevc ✓ libx265
audio      aac ✓ aac   mp3 ✓ libmp3lame
subtitles  subrip ✓ srt   ass ✓ ass   hdmv_pgs_subtitle (copy only)

ffmpeg can do everything this config asks for.
```

### Configuration

Mediator uses the first config file it finds:

1. `--config <path>`
2. `./mediator.yaml` in the current directory
3. the user config directory:
   - Linux: `$XDG_CONFIG_HOME/mediator/mediator.yaml`, else
     `~/.config/mediator/mediator.yaml` (also when `$XDG_CONFIG_HOME` is
     unset, empty or relative, as the XDG spec says)
   - Windows: `%AppData%\mediator\mediator.yaml`
   - macOS: `~/Library/Application Support/mediator/mediator.yaml`

`mediator run -v` prints which file it used.

```yaml
container:
  preferred: [mkv, mp4]

video:
  preferred: [h264, hevc]
  convert: {crf: 20, preset: medium}

audio:
  preferred: [aac, mp3]
  convert:
    bitrate: 192k     # default: 64k per output channel
    channels: 2       # downmix surround to stereo; omit to keep the layout

subtitles:
  preferred: [srt, ass]

original: keep        # or: delete
```

**The rule for every `preferred` list:** a stream already in *any* listed
codec is left alone. Anything else is converted to the *first* listed codec
the target container can hold. So with `[aac, mp3]`, MP3 stays MP3 and E-AC3
becomes AAC. Leave a list out and that kind of stream is never touched.

- **Container.** A file in a listed container keeps it. Any other file is
  remuxed into the first one (`.avi` -> `.mkv`).
- **`original`.** `keep` (the default) never touches the source and writes
  `<name>-mediator.<ext>` beside it. `delete` gives the converted file the
  source's name (with the new extension if the container changed) and removes
  the source. A file that already ends in `-mediator` is mediator's own output,
  so it is converted in place instead of getting a second suffix.
- **Container limits win.** If the target is `mp4`, SRT subtitles become
  `mov_text`, the only text format MP4 holds. Bitmap subtitles (PGS, VobSub)
  and font attachments are dropped, and the dry run says so.
- **Subtitles.** Text formats (SRT, ASS, WebVTT, mov_text) convert into each
  other. Bitmap formats would need OCR, so they are kept when the container
  allows it, with a warning.
- **`languages`** (audio and subtitles) keeps only tracks in the listed
  languages and drops the rest, e.g. `audio: {languages: [eng]}`. Codes are
  normalized, so `en`, `eng` and both forms of `ger`/`deu`, `fre`/`fra` and
  `dut`/`nld` match each other. Tracks without a language tag are always kept.
  A file never loses all its audio: if none of its audio tracks match, all
  are kept, with a warning. If the default audio track is dropped, the first
  remaining one becomes the default.
- **`convert` settings** apply only to streams that are actually converted.
  `channels: 2` downmixes a converted 7.1 TrueHD track, but leaves a 5.1 AAC
  track alone, because it's already acceptable. Mono is never upmixed.
- **Names** follow `ffprobe`, with common aliases accepted: `srt`=`subrip`,
  `h265`=`hevc`, `pgs`=`hdmv_pgs_subtitle`, `vobsub`=`dvd_subtitle`.
- **Unknown keys are an error**, so a typo can't silently switch a policy off.
- `workdir:` is a local folder to convert in; see "Network shares" below.
- `paths:` sets the folders to scan when none are given on the command line.
- `ffmpeg:` / `ffprobe:` point to the binaries when they aren't on `PATH`.

### Network shares (`workdir`)

Without a work folder, ffmpeg reads the source from the share and writes the
new file back to it at the same time. Over Wi-Fi (half-duplex) or with a hard
disk in the NAS, the two compete, and both run well below what a plain copy
achieves.

```yaml
workdir: '${TEMP}\mediator'    # or /var/tmp/mediator, or --workdir per run
```

With a work folder, every file goes through two steps, each with its own
progress bar and a summary line when it finishes:

```
    convert [████████████████████] 100%  1.9x  read 17.8 MB/s  took 1:02:13
    upload  [████████░░░░░░░░░░░░]  41%  write 18.2 MB/s  elapsed 14:02  ETA 20:11
```

1. **convert**: ffmpeg reads from the share and writes into the work folder.
   The `read` rate is your download speed from the share.
2. **upload**: the finished file is copied back in one sequential pass with
   large blocks, then checked (size, and a re-probe of streams, codecs and
   duration) before it is moved into place.

The work folder needs free space for one converted file, which is at most
about the size of the source when the video is kept; mediator warns when it
has less. Its temporary files are removed after every file, whether it
succeeded or not.

### Docker, and running on a NAS

Converting audio is light work, and on a NAS it skips the network entirely:
the file is read and written on the NAS's own disks. A 2-core Synology
DS220+ converts two audio tracks of a Blu-ray remux at about 9x real time,
several times faster than a desktop pulling the file over Wi-Fi.

The image bundles a static ffmpeg build, so there is nothing else to install.
It is built for `linux/amd64` and `linux/arm64`, and every image is
smoke-tested (doctor plus a real conversion) before it is published:

```bash
docker run --rm \
  --user 1026:100 \
  -v /volume1/Movies:/media \
  -v /volume1/docker/mediator/mediator.yaml:/config/mediator.yaml:ro \
  ghcr.io/dkarlovi/mediator run /media          # dry run; add --ack to convert
```

- **`/media`** is where your files are mounted. Mount several shares as
  subfolders (`-v /volume1/Shows:/media/Shows`) to process them together.
- **`/config/mediator.yaml`** is your config. Don't set `workdir` here: the
  disks are local, which is exactly the case it is not for.
- **`--user`** makes new files belong to you, not root. Find your IDs with
  `id` over SSH; on Synology they are typically `1026:100`.
- **Check the setup** with `... ghcr.io/dkarlovi/mediator doctor`.
- Recycle bins, snapshots and thumbnail folders (`#recycle`, `#snapshot`,
  `@eaDir`, `@Recycle`) are always skipped.

#### Synology

1. Install **Container Manager** from Package Center.
2. Put your `mediator.yaml` in a shared folder, e.g. `docker/mediator/`.
3. Try a dry run over SSH: `sudo docker run --rm ...` as above.
4. To convert new files automatically: **Control Panel → Task Scheduler →
   Create → Scheduled Task → User-defined script**, user `root`, daily, with
   the same `docker run` command plus `--ack`. Use the `--user` flag, or the
   converted files will belong to root.

Images are signed with cosign; to verify one:

```bash
cosign verify \
  --certificate-identity-regexp 'https://github.com/dkarlovi/mediator' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/dkarlovi/mediator:latest
```

### Safety
- The output goes to `<name>.mediator-tmp.<ext>` next to the source, on the
  same share, so the final swap is a rename, not a second network copy.
- Before anything is replaced, the output is probed again. It must have every
  planned stream, in the planned codec, and the same duration within 1%.
  Otherwise it is deleted and the source is untouched.
- With `original: delete`, the source is removed only after the verified
  output is in place.
- Mediator refuses to overwrite: with `original: delete`, if `S01E01.mkv`
  already exists next to `S01E01.avi`, that file is reported as an error.
- Ctrl+C stops the current `ffmpeg`, removes its partial output and leaves the
  source untouched.
- Scans skip mediator's own temp files, plus dotfiles such as
  macOS `._` files on NAS shares.

### Requirements
`ffmpeg` and `ffprobe` on `PATH`, or set in the config (`ffmpeg:`,
`ffprobe:`). Builds are native for Linux, Windows and macOS (amd64/arm64),
via goreleaser.

ffmpeg builds differ in which encoders they include, so mediator checks the
installed one rather than its version alone:

- Every `run` and `probe` starts by asking ffmpeg for its version, encoders
  and output formats. If ffmpeg or ffprobe is missing, mediator says how to
  install it for your OS. Problems that affect your config are printed
  before the first file.
- A preferred codec this ffmpeg has no encoder for is skipped in favour of
  the next one in the list: with `video: {preferred: [h264, hevc]}` and no
  `libx264`, video is converted to HEVC instead. With none available, a
  stream is kept as-is when its container can hold it.
- A preferred container this ffmpeg can't write makes the files that need
  it fail, and `mediator doctor` exits with 1.
- Mediator is tested with ffmpeg 8.1 and 9.0. Older or unparseable (git
  snapshot) versions get a warning but are not refused.
- Watch out for Fedora's default `ffmpeg-free`: it has no x264/x265
  encoders. Install `ffmpeg` from RPM Fusion instead.

### Development

```bash
go test ./...          # integration tests run when ffmpeg is installed
gofmt -l . && go vet ./... && staticcheck ./...
```

Package layout:
- `media/`: ffprobe parsing, codec aliases, encoders, container capabilities.
- `plan/`: the decision engine. Pure: probe data + config in, plan out.
- `transcode/`: plan -> ffmpeg arguments, execution, verification, swap.
- `scan/`: finding media files under the given paths.
- `commands/`: the CLI.
