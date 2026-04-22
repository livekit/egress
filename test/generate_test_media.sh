#!/bin/bash
# Generate per-participant test media for multi-participant integration tests.
# Run once; commit the output files to /media-samples/.
#
# Audio: continuous tone at a unique frequency per participant (Opus, 48kHz, 120s)
# Video: flashing top-stripe pattern with a unique color per participant (H264, 1080p25, 120s)
#
# The video pattern matches the existing test media's flash cadence:
# 1 flash per second, bright top stripe (YAVG >= 130 in top 8px), dark otherwise.

set -euo pipefail

OUTDIR="${1:-/media-samples}"
DURATION=120
SAMPLE_RATE=48000
VIDEO_SIZE="1920x1080"
VIDEO_FPS=25

# Participant definitions: index, frequency (Hz), color name, RGB hex
PARTICIPANTS=(
  "0 440 red FF0000"
  "1 880 green 00FF00"
  "2 1320 blue 0000FF"
  "3 1760 yellow FFFF00"
  "4 2200 cyan 00FFFF"
  "5 2640 magenta FF00FF"
)

for entry in "${PARTICIPANTS[@]}"; do
  read -r idx freq color_name color_hex <<< "$entry"

  audio_out="${OUTDIR}/participant_${idx}_${freq}hz.ogg"
  video_out="${OUTDIR}/participant_${idx}_${color_name}.h264"

  echo "Generating participant ${idx}: ${freq}Hz / ${color_name}"

  # Audio: continuous sine tone
  ffmpeg -y -f lavfi \
    -i "sine=frequency=${freq}:duration=${DURATION}:sample_rate=${SAMPLE_RATE}" \
    -c:a libopus -b:a 48k \
    "$audio_out"

  # Video: 1-second flash cycle.
  # Each second: 0.5s of bright color in top 8px (rest black), then 0.5s all black.
  # This produces YAVG >= 130 in the top-8px crop during the bright half,
  # matching extractFlashTimestamps() detection logic.
  ffmpeg -y -f lavfi \
    -i "color=c=black:s=${VIDEO_SIZE}:d=${DURATION}:r=${VIDEO_FPS}" \
    -vf "drawbox=x=0:y=0:w=iw:h=8:c=0x${color_hex}:t=fill:enable='lt(mod(t,1),0.5)'" \
    -c:v libx264 -preset ultrafast -tune zerolatency \
    -profile:v baseline -g ${VIDEO_FPS} \
    "$video_out"

  echo "  -> ${audio_out}"
  echo "  -> ${video_out}"
done

echo "Done. Generated media for ${#PARTICIPANTS[@]} participants."
