# Copyright 2026 LiveKit, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Build image for Anthropic's OSS Scanner. The scanner passes no build args, so the tags are
# hardcoded: keep them in sync with .gst-version and version.TemplateVersion.
FROM livekit/egress-templates:sha-94e7807 AS template

FROM livekit/gstreamer:1.28.7-1-dev

ARG TARGETARCH

ENV DEBIAN_FRONTEND=noninteractive \
    GOTOOLCHAIN=local \
    PATH=/usr/local/go/bin:/root/go/bin:$PATH

# Our GStreamer build is not owned by any package, so keep distro GStreamer packages
# (pulled in by pulseaudio) from overwriting its files.
RUN printf '%s\n' \
        'path-exclude=/usr/lib/*/gstreamer-1.0/*' \
        'path-exclude=/usr/lib/*/libgst*' \
        'path-include=/usr/lib/*/gstreamer1.0/*' \
    > /etc/dpkg/dpkg.cfg.d/exclude-distro-gstreamer

# Xvfb and pulseaudio are what the web source runs Chrome under.
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential ca-certificates curl git pkg-config redis-server \
      fonts-noto pulseaudio pulseaudio-utils xvfb gstreamer1.0-plugins-base- \
    && rm -rf /var/lib/apt/lists/*

# The installer image is multi-arch and carries Google's official deb for amd64 and arm64.
COPY --from=livekit/chrome-installer:157.0.8103.38 /chrome-installer /chrome-installer
RUN /chrome-installer/install-chrome "linux/${TARGETARCH}" && rm -rf /var/lib/apt/lists/*
ENV CHROME_DEVEL_SANDBOX=/usr/local/sbin/chrome-devel-sandbox

WORKDIR /src
COPY . /src

# .go-version is the toolchain the production images use; it must stay >= go.mod's go directive.
RUN ARCH=${TARGETARCH:-$(dpkg --print-architecture)} \
    && curl -fsSL -o /tmp/go.tar.gz "https://go.dev/dl/go$(cat .go-version).linux-${ARCH}.tar.gz" \
    && rm -rf /usr/local/go \
    && tar -C /usr/local -xzf /tmp/go.tar.gz \
    && rm /tmp/go.tar.gz \
    && go version

# cmd/server and the integration suite go:embed the built templates, which are not in git.
COPY --from=template /workspace/build/ /src/cmd/server/templates/
COPY --from=template /workspace/build/ /src/test/templates/

RUN go mod download && go mod verify
RUN go build ./... && go build -o /usr/local/bin/egress ./cmd/server

# Same subset CI's unit stage runs, with no redis, Chrome, or room server; test/ is the integration suite.
RUN go test -count=1 -timeout 10m ./pkg/... || echo 'WARNING: baseline tests failed'

ENV GOPROXY=off \
    GOSUMDB=off

CMD ["bash"]
