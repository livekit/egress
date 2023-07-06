# Chrome installer

This dockerfile is used to install chrome on ubuntu amd64 and arm64.

There is no official or available arm64 build with H264 support, so we needed to compile it from source. 

## Usage

To install chrome, add the following to your dockerfile:

```dockerfile
ARG TARGETPLATFORM
COPY --from=livekit:chrome-installer /chrome-installer /chrome-installer
RUN /chrome-installer/install-chrome "$TARGETPLATFORM" && \
    rm -rf /chrome-installer \
ENV PATH=${PATH}:/chrome
```

## Compilation 

It must be cross compiled from an amd64 builder. This build takes multiple hours, even on fast machines.

Relevant docs:
* [Build instructions](https://chromium.googlesource.com/chromium/src/+/main/docs/linux/build_instructions.md)
* [Cross compiling](https://chromium.googlesource.com/chromium/src/+/main/docs/linux/chromium_arm.md)

### Requirements 

* 64-bit Intel machine (x86_64)
* Ubuntu 22.04 LTS
* 64+ CPU cores
* 128GB+ RAM
* 100GB+ disk space

### Build steps

```shell
export CHROME_BUILDER={ip}
ssh root@$CHROME_BUILDER
```
```shell
adduser chrome
adduser chrome sudo
su - chrome
```
```shell
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
export PATH="$PATH:/home/chrome/depot_tools"
mkdir chromium && cd chromium
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
cd src
./build/install-build-deps.sh
./build/linux/sysroot_scripts/install-sysroot.py --arch=arm64
gclient runhooks
gn gen out/default --args='target_cpu="arm64" proprietary_codecs=true ffmpeg_branding="Chrome" enable_nacl=false is_debug=false symbol_level=0 v8_symbol_level=0 dcheck_always_on=false is_official_build=true'
autoninja -C out/default chrome chrome_sandbox
cd out/default
zip arm64.zip \
  chrome \
  chrome-wrapper \
  chrome_sandbox \
  chrome_100_percent.pak \
  chrome_200_percent.pak \
  chrome_crashpad_handler \
  headless_lib_data.pak \
  headless_lib_strings.pak \
  icudtl.dat \
  locales/en-US.pak \
  libEGL.so \
  libGLESv2.so \
  resources.pak \
  snapshot_blob.bin \
  v8_context_snapshot.bin
exit
```
```shell
exit
```
```shell
scp root@$CHROME_BUILDER:/home/chrome/chromium/src/out/default/arm64.zip ~/livekit/egress/build/chrome/arm64.zip
cd ~/livekit/egress/build/chrome
mkdir arm64 && unzip arm64.zip -d arm64 && rm arm64.zip
```
