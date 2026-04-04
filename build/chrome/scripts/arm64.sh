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

git fetch --no-tags --depth=1 origin "refs/tags/$1:refs/tags/$1"
git checkout -B stable "tags/$1"

gclient sync -D --with_branch_heads

./build/install-build-deps.sh
./build/linux/sysroot_scripts/install-sysroot.py --arch=arm64
gclient runhooks

gn gen out/default --args='
  target_cpu="arm64"
  proprietary_codecs=true
  ffmpeg_branding="Chrome"

  is_official_build=true
  is_debug=false

  symbol_level=0
  blink_symbol_level=0
  v8_symbol_level=0

  enable_nacl=false
  enable_printing=false
  enable_basic_printing=false
  enable_pdf=false
  enable_extensions=false

  rtc_use_pipewire=false

  is_component_build=false
  use_jumbo_build=true

  dcheck_always_on=false
'

export NINJA_SUMMARIZE_BUILD=1
autoninja -C out/default chrome chrome_sandbox -j "$(nproc)"

cd out/default

mkdir -p "$HOME/output/arm64/locales"
mkdir -p "$HOME/output/arm64"

mv locales/en-US.pak "$HOME/output/arm64/locales/"

required_files=(
  chrome
  chrome-wrapper
  chrome_100_percent.pak
  chrome_200_percent.pak
  chrome_crashpad_handler
  chrome_sandbox
  icudtl.dat
  libEGL.so
  libGLESv2.so
  resources.pak
  snapshot_blob.bin
  v8_context_snapshot.bin
)

for f in "${required_files[@]}"; do
  if [ ! -e "$f" ]; then
    echo "Missing required build output: $f"
    exit 1
  fi
  mv "$f" "$HOME/output/arm64/"
done
