#!/usr/bin/env bash
# Copyright 2023 LiveKit, Inc.
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

set -exo pipefail

# Start pulseaudio
rm -rf /var/run/pulse /var/lib/pulse /home/egress/.config/pulse /home/egress/.cache/xdgr/pulse
pulseaudio -D --verbose --exit-idle-time=-1 --disallow-exit

# Run RTSP server
./rtsp-simple-server > /dev/null 2>&1 &

# Run tests
if [[ -z ${GITHUB_WORKFLOW+x} ]]; then
  exec ./test.test -test.v -test.timeout 30m
else
  go install github.com/gotesttools/gotestfmt/v2/cmd/gotestfmt@latest
  exec go tool test2json -p egress ./test.test -test.v -test.timeout 30m 2>&1 | "$HOME"/go/bin/gotestfmt
fi
