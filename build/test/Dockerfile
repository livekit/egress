# Copyright 2023 LiveKit, Inc.
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

# syntax=docker/dockerfile:1.6
ARG TARGETPLATFORM
ARG TEMPLATE_TAG=latest
ARG LINT_VERSION=v2.11.3

FROM livekit/egress-templates:$TEMPLATE_TAG AS template

FROM livekit/gstreamer:1.24.12-dev AS builder

WORKDIR /workspace

ARG TARGETPLATFORM

# Deadlock 0 = off, 1 = on
ARG DEADLOCK=0

COPY .go-version ./

# Install Go: use the exact patch pinned in .go-version, which Renovate keeps current
# via an approvable PR. It is decoupled from go.mod's "go" directive (the module
# minimum, which the go toolchain may rewrite to bare major.minor). GOTOOLCHAIN=local
# forces exactly this toolchain and fails the build if go.mod ever requires a newer
# one, so a minor-version bump surfaces as a red CI check instead of a silent download.
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    GOVER=$(cat .go-version) && \
    if [ -z "$GOVER" ]; then echo ".go-version is empty" >&2; exit 1; fi && \
    echo "using Go $GOVER from .go-version" && \
    wget -O /tmp/go.tar.gz https://go.dev/dl/go$GOVER.linux-${GOARCH}.tar.gz && \
    rm -rf /usr/local/go && \
    tar -C /usr/local -xzf /tmp/go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOTOOLCHAIN=local

ENV GOMODCACHE=/go/pkg/mod \
    GOCACHE=/go/build-cache

# download go modules
COPY go.mod .
COPY go.sum .
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/go/build-cache \
    go mod download

# Get security updates in case the toolchain was updated.
# SECURITY_REFRESH busts the cache so the upgrade re-runs on every build.
ARG SECURITY_REFRESH
RUN apt-get update && apt-get -y dist-upgrade && rm -rf /var/lib/apt/lists/*

# copy source
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY test/ test/
COPY version/ version/

# copy templates
COPY --from=template workspace/build/ cmd/server/templates/
COPY --from=template workspace/build/ test/templates/

# build (service tests will need to launch the handler)
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/go/build-cache \
    if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    TAGS=""; \
    if [ "${DEADLOCK:-0}" = "1" ]; then TAGS="deadlock"; fi; \
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} GODEBUG=disablethp=1 go build ${TAGS:+-tags} ${TAGS:+"$TAGS"} -o egress ./cmd/server

RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/go/build-cache \
    if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} go test -c -v -race --tags=integration ./test

FROM golangci/golangci-lint:${LINT_VERSION} AS golangci

FROM builder AS lint

COPY --from=golangci /usr/bin/golangci-lint /usr/local/bin/golangci-lint
COPY .golangci.yaml .

RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/go/build-cache \
    CGO_ENABLED=1 \
    golangci-lint run \
        --timeout=5m \
        --modules-download-mode=mod \
        --build-tags="${LINT_TAGS}"
RUN echo ok >/lint_ok


FROM livekit/gstreamer:1.24.12-prod

ARG TARGETPLATFORM

# install deps, pulling in security updates from the base image.
# SECURITY_REFRESH busts the cache so the upgrade re-runs on every build.
ARG SECURITY_REFRESH
RUN apt-get update && \
    apt-get -y dist-upgrade && \
    apt-get install -y \
        curl \
        ffmpeg \
        fonts-noto \
        gnupg \
        pulseaudio \
        python3 \
        python3-pip \
        unzip \
        wget \
        xvfb \
        gstreamer1.0-plugins-base-

# reuse the .go-version-pinned toolchain installed in the builder stage
COPY --from=1 /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOTOOLCHAIN=local

# install chrome
COPY --from=livekit/chrome-installer:150.0.7871.46 /chrome-installer /chrome-installer

RUN /chrome-installer/install-chrome "$TARGETPLATFORM"

# clean up
RUN rm -rf /var/lib/apt/lists/*

# install rtsp server
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then ARCH=arm64v8; else ARCH=amd64; fi && \
    wget https://github.com/bluenviron/mediamtx/releases/download/v1.8.1/mediamtx_v1.8.1_linux_${ARCH}.tar.gz && \
    tar -zxvf mediamtx_v1.8.1_linux_${ARCH}.tar.gz && \
    rm mediamtx_v1.8.1_linux_${ARCH}.tar.gz && \
    sed -i 's_record: no_record: yes_g' mediamtx.yml && \
    sed -i 's_recordPath: ./recordings/%path/_recordPath: /out/output/stream-_g' mediamtx.yml

# install toxiproxy (used by integration tests to simulate wedged RTMP outputs)
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then ARCH=arm64; else ARCH=amd64; fi && \
    wget -O /usr/local/bin/toxiproxy-server \
        https://github.com/Shopify/toxiproxy/releases/download/v2.12.0/toxiproxy-server-linux-${ARCH} && \
    chmod +x /usr/local/bin/toxiproxy-server

# create egress user
RUN useradd -ms /bin/bash -g root -G sudo,pulse,pulse-access egress
RUN mkdir -p home/egress/tmp home/egress/.cache/xdgr && \
    chown -R egress /home/egress
RUN mkdir -p /out/output && chown -R egress /out

# copy files
COPY test/ /workspace/test/
COPY --from=1 /workspace/egress /bin/
COPY --from=1 /workspace/test.test .
COPY media-samples /media-samples
COPY build/test/entrypoint.sh .

# Force lint stage to run successfully
COPY --from=lint /lint_ok /__lint_ok

# run tests
USER egress
ENV PATH=${PATH}:/chrome
ENV XDG_RUNTIME_DIR=/home/egress/.cache/xdgr
ENV CHROME_DEVEL_SANDBOX=/usr/local/sbin/chrome-devel-sandbox
ENTRYPOINT ["./entrypoint.sh"]
