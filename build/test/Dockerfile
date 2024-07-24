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

WORKDIR /workspace

ARG TARGETPLATFORM

# install go
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    wget https://go.dev/dl/go1.22.1.linux-${GOARCH}.tar.gz && \
    rm -rf /usr/local/go && \
    tar -C /usr/local -xzf go1.22.1.linux-${GOARCH}.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"

# download go modules
COPY go.mod .
COPY go.sum .
RUN go mod download

# copy source
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY version/ version/

COPY test/ test/
# copy templates
COPY --from=livekit/egress-templates workspace/build/ cmd/server/templates/
COPY --from=livekit/egress-templates workspace/build/ test/templates/

# build (service tests will need to launch the handler)
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} GO111MODULE=on GODEBUG=disablethp=1 go build -a -o egress ./cmd/server

RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} GO111MODULE=on go test -c -v -race --tags=integration ./test


FROM livekit/gstreamer:1.24.4-prod

ARG TARGETPLATFORM

# install deps
RUN apt-get update && \
    apt-get install -y \
        curl \
        ffmpeg \
        fonts-noto \
        gnupg \
        pulseaudio \
        unzip \
        wget \
        xvfb \
        gstreamer1.0-plugins-base-

# install go
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    wget https://go.dev/dl/go1.22.1.linux-${GOARCH}.tar.gz && \
    rm -rf /usr/local/go && \
    tar -C /usr/local -xzf go1.22.1.linux-${GOARCH}.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"

# install chrome
COPY --from=livekit/chrome-installer:125.0.6422.141 /chrome-installer /chrome-installer
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

# create egress user
RUN useradd -ms /bin/bash -g root -G sudo,pulse,pulse-access egress
RUN mkdir -p home/egress/tmp home/egress/.cache/xdgr && \
    chown -R egress /home/egress

# copy files
COPY test/ /workspace/test/
COPY --from=0 /workspace/egress /bin/
COPY --from=0 /workspace/test.test .
COPY build/test/entrypoint.sh .

# run tests
USER egress
ENV PATH=${PATH}:/chrome
ENV XDG_RUNTIME_DIR=/home/egress/.cache/xdgr
ENV CHROME_DEVEL_SANDBOX=/usr/local/sbin/chrome-devel-sandbox
ENTRYPOINT ["./entrypoint.sh"]
