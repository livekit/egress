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
# Run once; commit the output files to test/samples/ via git-lfs.
#
# Each participant gets a unique frequency + color, with codecs rotating:
#   Audio: 0,3=Opus  1,4=PCMU  2,5=PCMA
#   Video: 0,3=H264  1,4=VP8   2,5=VP9
#
# Video: black background with large centered timer (SS.CC format).
#   t=**.00-**.09: timer text is the participant's bright color (flash)
#   t=**.10-**.99: timer text is dark grey
# Audio: brief beep at a unique frequency (50ms per second), matching flash cadence.

set -euo pipefail

OUTDIR="${1:-test/samples}"
DURATION=120
VIDEO_SIZE="1920x1080"
VIDEO_FPS=25

# Participant definitions: index, frequency (Hz), color name, RGB hex
PARTICIPANTS=(
  "0 440 red FF4444"
  "1 880 green 00FF00"
  "2 1320 blue 44AAFF"
  "3 1760 yellow FFFF00"
  "4 2200 cyan 00FFFF"
  "5 2640 magenta FF66FF"
)

# Codec rotation (mod 3)
AUDIO_CODECS=("opus" "pcmu" "pcma")
VIDEO_CODECS=("h264" "vp8" "vp9")

# Timer text expression: SS.CC (seconds.centiseconds)
TIMER_TEXT='%{eif\:t\:d\:2}.%{eif\:mod(t*100\,100)\:d\:2}'
TIMER_POS="x=(w-tw)/2:y=(h-th)/2:fontsize=220"

for entry in "${PARTICIPANTS[@]}"; do
  read -r idx freq color_name color_hex <<< "$entry"

  audio_codec=${AUDIO_CODECS[$((idx % 3))]}
  video_codec=${VIDEO_CODECS[$((idx % 3))]}

  echo "Generating participant ${idx}: ${freq}Hz/${color_name} audio=${audio_codec} video=${video_codec}"

  RAW_VIDEO="color=c=black:s=${VIDEO_SIZE}:d=${DURATION}:r=${VIDEO_FPS}"

  # Video filter: two drawtext layers for the flash effect.
  # Grey timer shown most of the time, colored timer shown for first 90ms each second.
  VF="drawtext=text='${TIMER_TEXT}':${TIMER_POS}:fontcolor=0x333333:enable='gte(mod(t,1),0.08)'"
  VF="${VF},drawtext=text='${TIMER_TEXT}':${TIMER_POS}:fontcolor=0x${color_hex}:enable='lt(mod(t,1),0.08)'"
  VF="${VF},format=yuv420p"

  # Audio: brief beep (50ms) once per second, matching the video flash cadence.
  BEEP_EXPR="sin(${freq}*2*PI*t)*(lt(mod(t,1),0.05))"

  case $audio_codec in
    opus)
      ffmpeg -y -f lavfi \
        -i "aevalsrc='${BEEP_EXPR}':s=48000:d=${DURATION}" \
        -c:a libopus -b:a 48k -application audio \
        "${OUTDIR}/participant_${idx}_${freq}hz.ogg"
      ;;
    pcmu)
      ffmpeg -y -f lavfi \
        -i "aevalsrc='${BEEP_EXPR}':s=8000:d=${DURATION}" \
        -c:a pcm_mulaw -ar 8000 -ac 1 \
        "${OUTDIR}/participant_${idx}_${freq}hz_pcmu.wav"
      ;;
    pcma)
      ffmpeg -y -f lavfi \
        -i "aevalsrc='${BEEP_EXPR}':s=8000:d=${DURATION}" \
        -c:a pcm_alaw -ar 8000 -ac 1 \
        "${OUTDIR}/participant_${idx}_${freq}hz_pcma.wav"
      ;;
  esac

  case $video_codec in
    h264)
      ffmpeg -y -f lavfi -i "$RAW_VIDEO" \
        -vf "$VF" \
        -c:v libx264 -pix_fmt yuv420p -profile:v baseline -level 4.0 -bf 0 \
        -colorspace bt2020nc -color_primaries bt2020 -color_trc smpte2084 -color_range tv \
        -x264-params "keyint=$((VIDEO_FPS*2)):min-keyint=$((VIDEO_FPS*2)):scenecut=0:ref=1:8x8dct=0:cabac=0:weightp=0" \
        -f h264 \
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

rm -f "${OUTDIR}/test_preview.mp4"
echo "Done. Generated 12 files for ${#PARTICIPANTS[@]} participants."
