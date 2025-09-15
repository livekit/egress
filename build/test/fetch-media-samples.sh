#!/usr/bin/env bash
set -euo pipefail

REPO="livekit/media-samples"
DEST="media-samples"
REF="${1:-main}"

export GIT_TERMINAL_PROMPT=0

if ! command -v git-lfs >/dev/null 2>&1; then
  echo "git-lfs not found. Install it (brew install git-lfs / apt-get install git-lfs)" >&2
  exit 1
fi
git lfs install --local

# run git with an Authorization header only if GITHUB_TOKEN is set
g() {
  if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    local b64
    b64="$(printf 'x-access-token:%s' "$GITHUB_TOKEN" | base64)"
    git -c "http.https://github.com/.extraheader=AUTHORIZATION: basic $b64" "$@"
  else
    git "$@"
  fi
}

if [[ -d "$DEST/.git" ]]; then
  git -C "$DEST" config core.hooksPath /dev/null
  g -C "$DEST" fetch --depth=1 origin "$REF"
  git -C "$DEST" checkout -f FETCH_HEAD
else
  tmpl="$(mktemp -d)"
  g -c core.hooksPath=/dev/null \
    clone --template "$tmpl" --depth 1 --branch "$REF" \
    "https://github.com/${REPO}.git" "$DEST"
  rm -rf "$tmpl"
fi

g -C "$DEST" lfs pull

