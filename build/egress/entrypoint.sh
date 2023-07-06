#!/usr/bin/env bash
set -euxo pipefail

export TMPDIR=/tmp/lkegress

# Start pulseaudio
rm -rf /var/run/pulse /var/lib/pulse /home/egress/.config/pulse
pulseaudio -D --verbose --exit-idle-time=-1 --disallow-exit

# cleanup old temporary files
if ! [ -z $TMPDIR ]; then
	mkdir -p $TMPDIR
fi

# Run egress service
exec egress
