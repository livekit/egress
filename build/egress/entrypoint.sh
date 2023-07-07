#!/usr/bin/env bash
set -euxo pipefail

# Clean out tmp
rm -rf /home/egress/tmp/*

# Start pulseaudio
rm -rf /var/run/pulse /var/lib/pulse /home/egress/.config/pulse
pulseaudio -D --verbose --exit-idle-time=-1 --disallow-exit

# Run egress service
exec egress
