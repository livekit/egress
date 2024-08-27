# Chrome installer

This dockerfile is used to install chrome on ubuntu amd64 and arm64.

There is no official or available arm64 build with H264 support, so we needed to compile it from source. 

## Usage

To install chrome, add the following to your dockerfile:

```dockerfile
ARG TARGETPLATFORM
COPY --from=livekit/chrome-installer:124.0.6367.201 /chrome-installer /chrome-installer
RUN /chrome-installer/install-chrome "$TARGETPLATFORM"
ENV PATH=${PATH}:/chrome
ENV CHROME_DEVEL_SANDBOX=/usr/local/sbin/chrome-devel-sandbox
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
