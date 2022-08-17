#!/usr/bin/env bash
set -euxo pipefail

export TMPDIR=/tmp/lkegress

# Start pulseaudio as system wide daemon; for debugging it helps to start in non-daemon mode
pulseaudio -D --verbose --exit-idle-time=-1 --system --disallow-exit

# cleanup old temporary files
if ! [ -z $TMPDIR ]; then
	rm -rf $TMPDIR
	mkdir -p $TMPDIR
fi

# Run egress service
exec egress
