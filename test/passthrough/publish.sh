#!/usr/bin/env bash
# Synthetic OBS-like publisher for pass-through testing: pre-encoded H.264
# (fixed 2s GOP, zerolatency) + Opus audio, delivered over WHIP bypass.
#
# Usage:
#   ./publish.sh start [room]   create WHIP ingress (bypass) + start publisher
#   ./publish.sh pause          SIGSTOP the publisher (mute-like: RTP stops)
#   ./publish.sh resume         SIGCONT the publisher
#   ./publish.sh stop           kill publisher container (disconnect)
set -euo pipefail

ROOM="${2:-passthrough-test}"
CONTAINER="passthrough-publisher"
STATE_DIR="${TMPDIR:-/tmp}/passthrough-test"
GST_IMAGE="${GST_IMAGE:-livekit/gstreamer:1.24.12-dev}"

export LIVEKIT_URL="${LIVEKIT_URL:-ws://localhost:7880}"
: "${LIVEKIT_API_KEY:?set LIVEKIT_API_KEY}"
: "${LIVEKIT_API_SECRET:?set LIVEKIT_API_SECRET}"

mkdir -p "$STATE_DIR"

case "${1:-}" in
start)
    # WHIP_URL env overrides ingress creation (e.g. reuse an existing ingress)
    if [[ -n "${WHIP_URL:-}" ]]; then
        echo "$WHIP_URL" > "$STATE_DIR/whip_url"
    fi
    # reuse ingress across reconnects so the stream key stays stable
    if [[ ! -f "$STATE_DIR/whip_url" ]]; then
        cat > "$STATE_DIR/ingress-request.json" <<EOF
{
  "input_type": "WHIP_INPUT",
  "name": "passthrough-test",
  "room_name": "$ROOM",
  "participant_identity": "publisher",
  "enable_transcoding": false
}
EOF
        lk ingress create "$STATE_DIR/ingress-request.json" > "$STATE_DIR/ingress.out"
        cat "$STATE_DIR/ingress.out"
        URL=$(grep -oE 'https?://[^" ]+/w[^" ]*' "$STATE_DIR/ingress.out" | head -1)
        KEY=$(grep -oE '"?stream_?[kK]ey"?[": ]+[A-Za-z0-9_-]+' "$STATE_DIR/ingress.out" | grep -oE '[A-Za-z0-9_-]+$' | head -1)
        echo "${URL%/}/${KEY}" > "$STATE_DIR/whip_url"
    fi
    WHIP_URL=$(cat "$STATE_DIR/whip_url")
    echo "publishing to $WHIP_URL"

    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    # 30fps, key-int-max=60 => fixed 2s GOP like the OBS setup
    docker run -d --name "$CONTAINER" --network host "$GST_IMAGE" \
        gst-launch-1.0 -e \
        videotestsrc is-live=true pattern=smpte \
            ! video/x-raw,width=1280,height=720,framerate=30/1 \
            ! timeoverlay font-desc="Sans 24" \
            ! x264enc tune=zerolatency speed-preset=veryfast key-int-max=60 bitrate=2500 option-string="scenecut=0" \
            ! h264parse \
            ! whip.video_0 \
        audiotestsrc is-live=true wave=sine freq=440 \
            ! audio/x-raw,rate=48000,channels=2 \
            ! opusenc \
            ! whip.audio_0 \
        whipclientsink name=whip signaller::whip-endpoint="$WHIP_URL"
    echo "publisher started (container $CONTAINER)"
    ;;

pause)
    docker kill --signal SIGSTOP "$CONTAINER"
    echo "publisher paused (RTP stopped) at $(date +%s.%N)"
    ;;

resume)
    docker kill --signal SIGCONT "$CONTAINER"
    echo "publisher resumed at $(date +%s.%N)"
    ;;

stop)
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    echo "publisher disconnected at $(date +%s.%N)"
    ;;

clean)
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    if [[ -f "$STATE_DIR/ingress.json" ]]; then
        lk ingress delete "$(jq -r '.ingress_id' "$STATE_DIR/ingress.json")" || true
    fi
    rm -rf "$STATE_DIR"
    ;;

*)
    echo "usage: $0 start [room] | pause | resume | stop | clean" >&2
    exit 1
    ;;
esac
