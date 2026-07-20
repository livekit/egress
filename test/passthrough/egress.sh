#!/usr/bin/env bash
# Start/stop a Participant egress with HLS segment output for the test room.
#
# Usage:
#   ./egress.sh start [--transcode] [room]   start egress (pass-through by default)
#   ./egress.sh stop                          stop the last started egress
#
# --transcode adds an encoding preset to the request. The config gate treats
# any preset as a transcoding request, so this forces the decode+encode
# fallback on the same deployment - used for the CPU comparison (5.6).
set -euo pipefail

STATE_DIR="${TMPDIR:-/tmp}/passthrough-test"
mkdir -p "$STATE_DIR"

export LIVEKIT_URL="${LIVEKIT_URL:-ws://localhost:7880}"
: "${LIVEKIT_API_KEY:?set LIVEKIT_API_KEY}"
: "${LIVEKIT_API_SECRET:?set LIVEKIT_API_SECRET}"

case "${1:-}" in
start)
    shift
    TRANSCODE=false
    ROOM="passthrough-test"
    for arg in "$@"; do
        case "$arg" in
        --transcode) TRANSCODE=true ;;
        *) ROOM="$arg" ;;
        esac
    done

    PREFIX="passthrough-test/$(date +%Y%m%d-%H%M%S)"
    REQ="$STATE_DIR/egress-request.json"
    {
        echo '{'
        echo "  \"room_name\": \"$ROOM\","
        echo '  "identity": "publisher",'
        if $TRANSCODE; then
            echo '  "preset": "H264_720P_30",'
        fi
        echo '  "segment_outputs": [{'
        echo "    \"filename_prefix\": \"$PREFIX/seg\","
        echo "    \"playlist_name\": \"$PREFIX/playlist.m3u8\","
        echo "    \"live_playlist_name\": \"$PREFIX/live.m3u8\","
        echo '    "segment_duration": 4'
        echo '  }]'
        echo '}'
    } > "$REQ"

    lk egress start --type participant "$REQ" | tee "$STATE_DIR/egress-start.out"
    grep -oE 'EG_[A-Za-z0-9]+' "$STATE_DIR/egress-start.out" | head -1 > "$STATE_DIR/egress_id"
    echo "egress id: $(cat "$STATE_DIR/egress_id"), output prefix: $PREFIX"
    echo "mode: $($TRANSCODE && echo 'transcode (forced fallback)' || echo 'pass-through')"
    ;;

stop)
    lk egress stop "$(cat "$STATE_DIR/egress_id")"
    ;;

*)
    echo "usage: $0 start [--transcode] [room] | stop" >&2
    exit 1
    ;;
esac
