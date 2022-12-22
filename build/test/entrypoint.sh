#!/usr/bin/env bash
set -euxo pipefail

# Start pulseaudio as system wide daemon; for debugging it helps to start in non-daemon mode
pulseaudio -D --verbose --exit-idle-time=-1 --system --disallow-exit

# Run RTSP server
./rtsp-simple-server &

# Run tests
exec ./test.test -test.v -test.timeout 20m
