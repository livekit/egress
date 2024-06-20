#!/bin/bash
set -xeuo pipefail

wget https://dl.google.com/linux/chrome/deb/pool/main/g/google-chrome-stable/google-chrome-stable_"$1"-1_amd64.deb
mkdir -p "$HOME/output/amd64"
mv google-chrome-stable_"$1"-1_amd64.deb "$HOME/output/amd64/google-chrome-stable_amd64.deb"
