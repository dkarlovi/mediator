# Static ffmpeg and ffprobe for the target architecture: a full build (x264,
# x265, LAME, Opus, SVT-AV1, ...) without shared libraries, so it runs on a
# distroless base. Keep the version in step with what mediator is tested with.
FROM mwader/static-ffmpeg:9.0.2-1 AS ffmpeg

# Cross-compile on the build machine's own architecture: Go needs no emulation
# to produce a static binary for another one.
FROM --platform=$BUILDPLATFORM golang:1.26.10-trixie AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
# Declared this late on purpose: a build arg is part of the cache key of every
# RUN after it, and VERSION changes on each commit.
ARG VERSION=dev
ARG BUILD_DATE
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X 'main.version=${VERSION}' -X 'main.buildDate=${BUILD_DATE}'" \
    -o /out/mediator .

FROM gcr.io/distroless/static-debian13
COPY --from=ffmpeg /ffmpeg /ffprobe /usr/local/bin/
COPY --from=builder /out/mediator /bin/mediator
# The sample config, for reference and for `mediator doctor -C` checks.
COPY mediator.yaml.dist /etc/mediator/mediator.yaml.dist
# Mount your config at /config/mediator.yaml: mediator looks for
# ./mediator.yaml in the working directory first.
WORKDIR /config
ENTRYPOINT ["/bin/mediator"]
# A dry run over /media; add --ack to convert.
CMD ["run", "/media"]
