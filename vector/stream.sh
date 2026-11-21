#!/bin/bash
# One source of the live stream, started by go2rtc when somebody watches and stopped (SIGTERM)
# when the last viewer leaves:
#   stream.sh video {output}  camera JPEGs (~2 per second) → H.264 at a steady frame rate
#   stream.sh audio {output}  robot microphone (16-bit PCM) → Opus
# ffmpeg replaces this shell (exec), so SIGTERM reaches it directly; it then closes its HTTP
# request and the bridge stops the robot's camera/microphone (and gives control back).
set -euo pipefail

KIND="${1:?video|audio}"
OUT="${2:?go2rtc output URL}"
BASE="http://127.0.0.1:${WS_PORT:-3012}"
TOKEN="$(cat /data/ws_token)"
FPS="${STREAM_FPS:-10}"
RATE="${MIC_SAMPLE_RATE:-16000}"

case "${KIND}" in
video)
    # The mpjpeg demuxer stamps frames as if they came at 25 fps (it ignores
    # -use_wallclock_as_timestamps); at the robot's ~2 fps the stream then ran at 0.08× real time
    # and fell further behind every second. setpts stamps each picture with the real clock
    # instead; fps= repeats the last picture so the player gets a steady stream; a keyframe every
    # 2 s lets a new viewer start quickly. Small probesize: start on the first frame, not 5 MB.
    exec ffmpeg -hide_banner -loglevel error -nostdin \
        -probesize 64k -analyzeduration 0 -fflags nobuffer \
        -f mpjpeg -i "${BASE}/mjpeg?token=${TOKEN}" \
        -an -vf "setpts=(RTCTIME-RTCSTART)/(TB*1000000),fps=${FPS},format=yuv420p" \
        -c:v libx264 -preset ultrafast -tune zerolatency -g "$((FPS * 2))" -bf 0 \
        -f rtsp -rtsp_transport tcp "${OUT}"
    ;;
audio)
    exec ffmpeg -hide_banner -loglevel error -nostdin \
        -probesize 32k -analyzeduration 0 -fflags nobuffer -f s16le -ar "${RATE}" -ac 1 -i "${BASE}/mic?token=${TOKEN}&rate=${RATE}" \
        -vn -c:a libopus -b:a 32k -application lowdelay -ar 48000 \
        -f rtsp -rtsp_transport tcp "${OUT}"
    ;;
*)
    echo "stream.sh: unknown source ${KIND}" >&2
    exit 2
    ;;
esac
