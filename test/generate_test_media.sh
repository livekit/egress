#!/bin/bash
# Copyright 2026 LiveKit, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Generate per-participant test media for multi-participant integration tests.
# Run once; commit the output files to /media-samples/.
#
# Each participant gets a unique frequency + color, with codecs rotating:
#   Audio: 0,3=Opus  1,4=PCMU  2,5=PCMA
#   Video: 0,3=H264  1,4=VP8   2,5=VP9
#
# The video pattern matches the existing flash cadence:
# 1 flash per second, bright top stripe (YAVG >= 130 in top 8px), dark otherwise.

set -euo pipefail

OUTDIR="${1:-/media-samples}"
DURATION=120
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

# Codec rotation (mod 3)
AUDIO_CODECS=("opus" "pcmu" "pcma")
VIDEO_CODECS=("h264" "vp8" "vp9")

vf_flash() {
  echo "drawbox=x=0:y=0:w=iw:h=8:c=0x${1}:t=fill:enable='lt(mod(t,1),0.5)'"
}

for entry in "${PARTICIPANTS[@]}"; do
  read -r idx freq color_name color_hex <<< "$entry"

  audio_codec=${AUDIO_CODECS[$((idx % 3))]}
  video_codec=${VIDEO_CODECS[$((idx % 3))]}

  echo "Generating participant ${idx}: ${freq}Hz/${color_name} audio=${audio_codec} video=${video_codec}"

  RAW_VIDEO="color=c=black:s=${VIDEO_SIZE}:d=${DURATION}:r=${VIDEO_FPS}"
  VF=$(vf_flash "$color_hex")

  # Audio
  case $audio_codec in
    opus)
      ffmpeg -y -f lavfi \
        -i "sine=frequency=${freq}:duration=${DURATION}:sample_rate=48000" \
        -c:a libopus -b:a 48k \
        "${OUTDIR}/participant_${idx}_${freq}hz.ogg"
      ;;
    pcmu)
      ffmpeg -y -f lavfi \
        -i "sine=frequency=${freq}:duration=${DURATION}:sample_rate=8000" \
        -c:a pcm_mulaw -ar 8000 -ac 1 \
        "${OUTDIR}/participant_${idx}_${freq}hz_pcmu.wav"
      ;;
    pcma)
      ffmpeg -y -f lavfi \
        -i "sine=frequency=${freq}:duration=${DURATION}:sample_rate=8000" \
        -c:a pcm_alaw -ar 8000 -ac 1 \
        "${OUTDIR}/participant_${idx}_${freq}hz_pcma.wav"
      ;;
  esac

  # Video
  case $video_codec in
    h264)
      ffmpeg -y -f lavfi -i "$RAW_VIDEO" \
        -vf "$VF" \
        -c:v libx264 -preset ultrafast -tune zerolatency \
        -profile:v baseline -g ${VIDEO_FPS} \
        "${OUTDIR}/participant_${idx}_${color_name}.h264"
      ;;
    vp8)
      ffmpeg -y -f lavfi -i "$RAW_VIDEO" \
        -vf "$VF" \
        -c:v libvpx -b:v 1M -g ${VIDEO_FPS} \
        "${OUTDIR}/participant_${idx}_${color_name}_vp8.ivf"
      ;;
    vp9)
      ffmpeg -y -f lavfi -i "$RAW_VIDEO" \
        -vf "$VF" \
        -c:v libvpx-vp9 -b:v 1M -g ${VIDEO_FPS} \
        "${OUTDIR}/participant_${idx}_${color_name}_vp9.ivf"
      ;;
  esac
done

echo "Done. Generated 12 files for ${#PARTICIPANTS[@]} participants."
