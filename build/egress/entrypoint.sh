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

set -euo pipefail

# Clean out tmp
rm -rf /home/egress/tmp/*

# Start pulseaudio
rm -rf /var/run/pulse /var/lib/pulse /home/egress/.config/pulse /home/egress/.cache/xdgr/pulse
# PulseAudio defaults to rlimit-nofile=256, exhausted at ~18 concurrent Chrome
# room-composite recordings (~13.5 fds each) — well below the max_pulse_clients
# admission limit. Raise the process limit AND PulseAudio's own cap (it re-applies
# its daemon.conf default via setrlimit, so ulimit alone is insufficient) so fd
# exhaustion isn't the binding constraint (see #1272).
ulimit -n 65536 || true
echo "rlimit-nofile = 65536" >> /etc/pulse/daemon.conf
pulseaudio -D --verbose --exit-idle-time=-1 --disallow-exit > /dev/null 2>&1

# Run egress service
exec /tini -- egress
