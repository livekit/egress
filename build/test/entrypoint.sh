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

set -eo pipefail

# Start pulseaudio
rm -rf /var/run/pulse /var/lib/pulse /home/egress/.config/pulse /home/egress/.cache/xdgr/pulse
pulseaudio -D --verbose --exit-idle-time=-1 --disallow-exit > /dev/null 2>&1

# Run RTSP server
./mediamtx > /dev/null 2>&1 &

# emit_avsync_stats prints the stats JSON between sentinels after go test
# exits. Writing from inside the test process races zap's log flush.
emit_avsync_stats() {
  local path="${AVSYNC_STATS_PATH:-/tmp/avsync-stats.json}"
  if [[ -f "$path" ]]; then
    echo "===AVSYNC_STATS_BEGIN==="
    cat "$path"
    echo
    echo "===AVSYNC_STATS_END==="
  fi
}

# Run tests
if [[ -z ${GITHUB_WORKFLOW+x} ]]; then
  ./test.test -test.v -test.timeout 30m
  status=$?
  emit_avsync_stats
  exit $status
else
  go install github.com/gotesttools/gotestfmt/v2/cmd/gotestfmt@latest
  go tool test2json -p egress ./test.test -test.v -test.timeout 30m 2>&1 | "$HOME"/go/bin/gotestfmt
  status=${PIPESTATUS[0]}
  emit_avsync_stats
  exit $status
fi
