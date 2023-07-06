#!/usr/bin/env bash
set -exo pipefail

# Start pulseaudio
rm -rf /var/run/pulse /var/lib/pulse /home/egress/.config/pulse
pulseaudio -D --verbose --exit-idle-time=-1 --disallow-exit

# Run RTSP server
./rtsp-simple-server > /dev/null 2>&1 &

# Run tests
if [[ -z ${GITHUB_WORKFLOW+x} ]]; then
  exec ./test.test -test.v -test.timeout 20m
else
  go install github.com/gotesttools/gotestfmt/v2/cmd/gotestfmt@latest
  exec go tool test2json -p egress ./test.test -test.v -test.timeout 20m 2>&1 | "$HOME"/go/bin/gotestfmt
fi
