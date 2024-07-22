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

FROM livekit/gstreamer:1.24.4-dev

ARG TARGETPLATFORM
ARG TARGETARCH
ENV TARGETARCH=${TARGETARCH}
ENV TARGETPLATFORM=${TARGETPLATFORM}

WORKDIR /workspace

# install go
RUN wget https://go.dev/dl/go1.22.1.linux-${TARGETARCH}.tar.gz && \
    rm -rf /usr/local/go && \
    tar -C /usr/local -xzf go1.22.1.linux-${TARGETARCH}.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"

# download go modules
COPY go.mod .
COPY go.sum .
RUN go mod download

# copy source
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY version/ version/

# copy templates
COPY --from=livekit/egress-templates workspace/build/ cmd/server/templates/
# delete .map files
RUN find cmd/server/templates/ -name *.map | xargs rm

# build
RUN CGO_ENABLED=1 GOOS=linux GOARCH=${TARGETARCH} GO111MODULE=on GODEBUG=disablethp=1 go build -a -o egress ./cmd/server

# install tini
ENV TINI_VERSION v0.19.0

ADD https://github.com/krallin/tini/releases/download/${TINI_VERSION}/tini-${TARGETARCH} /tini
RUN chmod +x /tini

FROM livekit/gstreamer:1.24.4-prod

ARG TARGETPLATFORM

# install deps
RUN apt-get update && \
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
COPY --from=livekit/chrome-installer:125.0.6422.141 /chrome-installer /chrome-installer
RUN /chrome-installer/install-chrome "$TARGETPLATFORM"

# clean up
RUN rm -rf /var/lib/apt/lists/*

# create egress user
RUN useradd -ms /bin/bash -g root -G sudo,pulse,pulse-access egress
RUN mkdir -p home/egress/tmp home/egress/.cache/xdgr && \
    chown -R egress /home/egress


# copy files
COPY --from=0 /workspace/egress /bin/
COPY --from=0 /tini /tini
COPY build/egress/entrypoint.sh /

# run
USER egress
ENV PATH=${PATH}:/chrome
ENV XDG_RUNTIME_DIR=/home/egress/.cache/xdgr
ENV CHROME_DEVEL_SANDBOX=/usr/local/sbin/chrome-devel-sandbox
ENTRYPOINT ["/entrypoint.sh"]
