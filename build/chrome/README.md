# Chrome installer

This dockerfile packages the official `google-chrome-stable` deb for amd64 and arm64, alongside
the `install-chrome` script that installs it.

The image exists so that consumers can pin a Chrome version indefinitely: Google's apt pool only
keeps the most recent handful of releases, so a version pinned directly against the pool stops
being downloadable after a few weeks. Publishing the deb inside an immutable image tag freezes it.

`.github/workflows/publish-chrome.yaml` publishes a new tag weekly, skipping the build when the
current stable release has already been published.

## Usage

To install chrome, add the following to your dockerfile:

```dockerfile
ARG TARGETPLATFORM
COPY --from=livekit/chrome-installer:150.0.7871.46 /chrome-installer /chrome-installer
RUN /chrome-installer/install-chrome "$TARGETPLATFORM"
ENV CHROME_DEVEL_SANDBOX=/usr/local/sbin/chrome-devel-sandbox
```
