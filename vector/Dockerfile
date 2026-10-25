ARG BUILD_FROM

# Build the bridge from source for the architecture of this Home Assistant (no cgo, static binary).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY bridge/go.mod bridge/go.sum ./
RUN go mod download
COPY bridge/ ./
ARG BUILD_VERSION
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${BUILD_VERSION}" \
        -o /out/vector-bridge ./cmd/vector-bridge

FROM ${BUILD_FROM}

# The Home Assistant base image has bash, jq and curl (options, Supervisor API);
# tzdata: local time in the log; ffmpeg: decodes audio (TTS, files) to 16 kHz PCM for the speaker.
RUN apk add --no-cache tzdata ffmpeg

COPY --from=build /out/vector-bridge /usr/local/bin/vector-bridge
COPY run.sh /run.sh
RUN chmod a+x /run.sh

ENTRYPOINT ["/run.sh"]
