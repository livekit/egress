#!/usr/bin/env bash
set -euo pipefail

REPO="livekit/media-samples"
DEST="media-samples"
REF="${1:-main}"

if ! command -v git-lfs >/dev/null 2>&1; then
  echo "git-lfs not found. Install it (e.g., brew install git-lfs or apt-get install git-lfs)"; exit 1
fi
git lfs install --local

# If a token is provided, wire it into git for github.com
if [[ -n "${GITHUB_TOKEN:-}" ]]; then
  git config --global url."https://${GITHUB_TOKEN}:x-oauth-basic@github.com/".insteadOf "https://github.com/"
fi

if [[ -d "$DEST/.git" ]]; then
  git -C "$DEST" fetch --depth=1 origin "$REF"
  git -C "$DEST" checkout -f FETCH_HEAD
else
  git clone --depth 1 --branch "$REF" "https://github.com/${REPO}.git" "$DEST"
fi

git -C "$DEST" lfs pull
