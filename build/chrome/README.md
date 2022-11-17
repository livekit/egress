# Chrome

This chrome is for arm64 architectures. A custom build was required to enable H264 support. 
so we need to build it.

## Compilation 

It must be cross compiled from an amd64 builder. This build takes multiple hours, even on fast machines.

Relevant docs:
* [Build instructions](https://chromium.googlesource.com/chromium/src/+/main/docs/linux/build_instructions.md)
* [Cross compiling](https://chromium.googlesource.com/chromium/src/+/main/docs/linux/chromium_arm.md)
* [Linking](https://chromium.googlesource.com/chromium/src/+/main/docs/linux/dev_build_as_default_browser.md)

### Requirements 

* 64-bit Intel machine (x86_64)
* Ubuntu 22.04 LTS
* 64+ CPU cores
* 128GB+ RAM
* 100GB+ disk space

### Build steps

```
ssh root@{ip}

adduser chrome
adduser chrome sudo
su - chrome

sudo apt-get update
sudo apt-get install -y \
  apt-utils \
  build-essential \
  curl \
  git \
  python3 \
  sudo

git clone https://chromium.googlesource.com/chromium/tools/depot_tools.git
export PATH="$PATH:/home/chrome/depot_tools"
mkdir chromium && cd chromium
fetch --nohooks --no-history chromium
vim .gclient
```

```
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
```

```
cd src
./build/install-build-deps.sh
./build/linux/sysroot_scripts/install-sysroot.py --arch=arm64
gclient runhooks
gn gen out/default --args='target_cpu="arm64" proprietary_codecs=true ffmpeg_branding="Chrome" enable_nacl=false is_debug=false symbol_level=0 v8_symbol_level=0 dcheck_always_on=false is_official_build=true'
autoninja -C out/default chrome
exit

scp root@{ip}:/home/chrome/chromium/src/out/default/chrome ~/livekit/egress/build/chrome/chrome
```
