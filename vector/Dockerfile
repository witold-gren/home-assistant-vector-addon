# Fixed FROM, no build.yaml: since Supervisor 2026.04 BUILD_FROM is no longer passed, so an
# `ARG BUILD_FROM` image silently became base:latest. Both images below are multi-arch manifests
# (amd64 + aarch64): the Supervisor builder picks the one for this machine.

# Build the bridge from source for the architecture of this Home Assistant (no cgo, static binary).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY bridge/go.mod bridge/go.sum ./
RUN go mod download
COPY bridge/ ./
ARG BUILD_VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${BUILD_VERSION}" \
        -o /out/vector-bridge ./cmd/vector-bridge

FROM ghcr.io/home-assistant/base:3.21

# The Home Assistant base image has bash, jq and curl (options, Supervisor API);
# tzdata: local time in the log; ffmpeg: decodes audio for the speaker and encodes the live stream
# (camera JPEGs → H.264, microphone PCM → Opus).
RUN apk add --no-cache tzdata ffmpeg

# go2rtc serves the live stream (RTSP) that Home Assistant turns into WebRTC; on demand only.
ARG GO2RTC_VERSION=1.9.9
RUN case "$(uname -m)" in x86_64) g=amd64 ;; aarch64) g=arm64 ;; *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;; esac \
    && curl -fsSL -o /usr/local/bin/go2rtc \
        "https://github.com/AlexxIT/go2rtc/releases/download/v${GO2RTC_VERSION}/go2rtc_linux_${g}" \
    && chmod +x /usr/local/bin/go2rtc

COPY --from=build /out/vector-bridge /usr/local/bin/vector-bridge
COPY stream.sh /opt/vector/stream.sh
COPY run.sh /run.sh
RUN chmod a+x /run.sh /opt/vector/stream.sh

ENTRYPOINT ["/run.sh"]
