#!/usr/bin/env bash
set -euxo pipefail

export TMPDIR=/tmp/lkegress

# Cleanup to be "stateless" on startup, otherwise pulseaudio daemon can't start again
rm -rf /var/run/pulse /var/lib/pulse /root/.config/pulse

# Start pulseaudio as system wide daemon; for debugging it helps to start in non-daemon mode
pulseaudio -D --verbose --exit-idle-time=-1 --system --disallow-exit

# cleanup old temporary files
if ! [ -z $TMPDIR ]; then
	mkdir -p $TMPDIR
fi

# Run egress service
exec egress
