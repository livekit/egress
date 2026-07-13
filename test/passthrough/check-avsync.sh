#!/usr/bin/env bash
# Check audio/video PTS alignment across HLS segments.
# Pass-through video + transcoded audio (Opus->AAC) take different pipeline
# paths; this prints the audio-video first-PTS offset per segment so drift or
# jumps after loss/mute events are visible.
#
# Usage: ./check-avsync.sh <dir-with-ts-segments>
# Requires ffprobe (falls back to a dockerized ffprobe if not installed).
set -euo pipefail

DIR="${1:?directory with .ts segments required}"

ffprobe_cmd() {
    if command -v ffprobe >/dev/null; then
        ffprobe "$@"
    else
        docker run --rm -v "$(realpath "$DIR")":/data -w /data --entrypoint ffprobe \
            linuxserver/ffmpeg "$@"
    fi
}

printf '%-40s %12s %12s %12s\n' "segment" "video_pts" "audio_pts" "offset_ms"

first_offset=""
for f in "$DIR"/*.ts; do
    name=$(basename "$f")
    probe_target="$name"
    command -v ffprobe >/dev/null && probe_target="$f"

    v=$(ffprobe_cmd -v error -select_streams v:0 -show_entries packet=pts_time \
        -of csv=p=0 -read_intervals '%+#1' "$probe_target" 2>/dev/null | head -1)
    a=$(ffprobe_cmd -v error -select_streams a:0 -show_entries packet=pts_time \
        -of csv=p=0 -read_intervals '%+#1' "$probe_target" 2>/dev/null | head -1)

    if [[ -z "$v" || -z "$a" ]]; then
        printf '%-40s %12s %12s %12s\n' "$name" "${v:-none}" "${a:-none}" "n/a"
        continue
    fi

    offset=$(awk -v v="$v" -v a="$a" 'BEGIN { printf "%.1f", (a - v) * 1000 }')
    drift=""
    if [[ -z "$first_offset" ]]; then
        first_offset="$offset"
    else
        drift=$(awk -v o="$offset" -v f="$first_offset" 'BEGIN {
            d = o - f
            if (d > 100 || d < -100) printf "  <-- drift %.1fms", d
        }')
    fi
    printf '%-40s %12s %12s %12s%s\n' "$name" "$v" "$a" "$offset" "$drift"
done
