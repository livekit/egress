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
git clone https://chromium.googlesource.com/chromium/tools/depot_tools.git
export PATH="$PATH:$HOME/depot_tools"
mkdir chromium
cd chromium || exit
fetch --nohooks --no-history chromium
echo 'solutions = [
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
]' | tee '.gclient' > /dev/null
cd src || exit
git fetch --tags
git checkout -b stable "$1"
gclient sync -D --with_branch_heads --with_tags
./build/install-build-deps.sh
./build/linux/sysroot_scripts/install-sysroot.py --arch=arm64
gclient runhooks
gn gen out/default --args='target_cpu="arm64" proprietary_codecs=true ffmpeg_branding="Chrome" enable_nacl=false is_debug=false symbol_level=0 v8_symbol_level=0 dcheck_always_on=false is_official_build=true'
autoninja -C out/default chrome chrome_sandbox
cd out/default || exit
mkdir -p "$HOME/output/arm64/locales"
mv locales/en-US.pak "$HOME/output/arm64/locales/"
mv chrome \
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
