#!/usr/bin/env bash
set -euxo pipefail

# Start pulseaudio as system wide daemon; for debugging it helps to start in non-daemon mode
pulseaudio -D --verbose --exit-idle-time=-1 --system --disallow-exit

# Run RTSP server
./rtsp-simple-server &

# Run tests
if [[ -z "${GITHUB_WORKFLOW}" ]]; then
  exec ./test.test -test.v -test.timeout 20m
else
  go install github.com/gotesttools/gotestfmt/v2/cmd/gotestfmt@latest
  exec go tool test2json -p egress ./test.test -test.v -test.timeout 20m 2>&1 | "$HOME"/go/bin/gotestfmt
fi
