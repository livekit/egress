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

ARG TEMPLATE_TAG=latest

FROM livekit/egress-templates:$TEMPLATE_TAG AS template

FROM livekit/gstreamer:1.24.12-dev

ARG TARGETPLATFORM
ARG TARGETARCH
ENV TARGETARCH=${TARGETARCH}
ENV TARGETPLATFORM=${TARGETPLATFORM}

WORKDIR /workspace

COPY .go-version ./

# Install Go: use the exact patch pinned in .go-version, which Renovate keeps current
# via an approvable PR. It is decoupled from go.mod's "go" directive (the module
# minimum, which the go toolchain may rewrite to bare major.minor). GOTOOLCHAIN=local
# forces exactly this toolchain and fails the build if go.mod ever requires a newer
# one, so a minor-version bump surfaces as a red CI check instead of a silent download.
RUN GOVER=$(cat .go-version) && \
    if [ -z "$GOVER" ]; then echo ".go-version is empty" >&2; exit 1; fi && \
    echo "using Go $GOVER from .go-version" && \
    wget -O /tmp/go.tar.gz https://go.dev/dl/go$GOVER.linux-${TARGETARCH}.tar.gz && \
    rm -rf /usr/local/go && \
    tar -C /usr/local -xzf /tmp/go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOTOOLCHAIN=local

# download go modules
COPY go.mod .
COPY go.sum .
RUN go mod download

# Get security updates in case the toolchain was updated.
# SECURITY_REFRESH busts the cache so the upgrade re-runs on every build.
ARG SECURITY_REFRESH
RUN apt-get update && apt-get -y dist-upgrade && rm -rf /var/lib/apt/lists/*

# copy source
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY version/ version/

# copy templates
COPY --from=template workspace/build/ cmd/server/templates/
# delete .map files
RUN find cmd/server/templates/ -name *.map | xargs rm

# build
RUN CGO_ENABLED=1 GOOS=linux GOARCH=${TARGETARCH} GO111MODULE=on GODEBUG=disablethp=1 go build -a -o egress ./cmd/server

# install tini
ENV TINI_VERSION v0.19.0

ADD https://github.com/krallin/tini/releases/download/${TINI_VERSION}/tini-${TARGETARCH} /tini
RUN chmod +x /tini

FROM livekit/gstreamer:1.24.12-prod

ARG TARGETPLATFORM

# install deps, pulling in security updates from the base image.
# SECURITY_REFRESH busts the cache so the upgrade re-runs on every build.
ARG SECURITY_REFRESH
RUN apt-get update && \
    apt-get -y dist-upgrade && \
    apt-get install -y \
    curl \
    fonts-noto \
    gnupg \
    pulseaudio \
    unzip \
    wget \
    xvfb \
    gstreamer1.0-plugins-base-

# install chrome
COPY --from=livekit/chrome-installer:150.0.7871.46 /chrome-installer /chrome-installer

RUN /chrome-installer/install-chrome "$TARGETPLATFORM"

# clean up
RUN rm -rf /var/lib/apt/lists/*

# create egress user
RUN useradd -ms /bin/bash -g root -G sudo,pulse,pulse-access egress
RUN mkdir -p home/egress/tmp home/egress/.cache/xdgr && \
    chown -R egress /home/egress

# copy files
COPY --from=1 /workspace/egress /bin/
COPY --from=1 /tini /tini
COPY build/egress/entrypoint.sh /

# PulseAudio defaults to rlimit-nofile=256. Set it to a more sensible value to avoid
# hitting a max Chrome instances bottleneck
RUN echo "rlimit-nofile = -1" >> /etc/pulse/daemon.conf

# run
USER egress
ENV PATH=${PATH}:/chrome
ENV XDG_RUNTIME_DIR=/home/egress/.cache/xdgr
ENV CHROME_DEVEL_SANDBOX=/usr/local/sbin/chrome-devel-sandbox

ENTRYPOINT ["/entrypoint.sh"]
