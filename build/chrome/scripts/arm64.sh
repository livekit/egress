#!/bin/bash
set -xeuo pipefail

sudo apt-get update
sudo apt-get install -y \
  apt-utils \
  build-essential \
  curl \
  git \
  python3 \
  sudo \
  zip

if [ ! -d "$HOME/depot_tools" ]; then
  git clone https://chromium.googlesource.com/chromium/tools/depot_tools.git "$HOME/depot_tools"
fi
export PATH="$PATH:$HOME/depot_tools"

mkdir -p "$HOME/chromium"
cd "$HOME/chromium"

fetch --nohooks --no-history chromium

cat > .gclient <<'EOF'
solutions = [
  {
    "name": "src",
    "url": "https://chromium.googlesource.com/chromium/src.git",
    "managed": False,
    "custom_deps": {},
    "custom_vars": {
      "checkout_pgo_profiles": True,
    },
    "target_cpu": "arm64",
  },
]
EOF

cd src

git fetch origin "refs/tags/$1:refs/tags/$1"
git checkout -B stable "tags/$1"

gclient sync -D --with_branch_heads

./build/install-build-deps.sh
./build/linux/sysroot_scripts/install-sysroot.py --arch=arm64
gclient runhooks

gn gen out/default --args='target_cpu="arm64" proprietary_codecs=true ffmpeg_branding="Chrome" enable_nacl=false is_debug=false symbol_level=0 v8_symbol_level=0 dcheck_always_on=false is_official_build=true'
autoninja -C out/default chrome chrome_sandbox

cd out/default

mkdir -p "$HOME/output/arm64/locales"

mv locales/en-US.pak "$HOME/output/arm64/locales/"
mv \
  chrome \
  chrome-wrapper \
  chrome_100_percent.pak \
  chrome_200_percent.pak \
  chrome_crashpad_handler \
  chrome_sandbox \
  headless_lib_data.pak \
  headless_lib_strings.pak \
  icudtl.dat \
  libEGL.so \
  libGLESv2.so \
  resources.pak \
  snapshot_blob.bin \
  v8_context_snapshot.bin \
  "$HOME/output/arm64/"
