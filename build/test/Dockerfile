FROM livekit/gstreamer:1.22.5-dev

WORKDIR /workspace

ARG TARGETPLATFORM

# install go
RUN apt-get update && apt-get install -y golang

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
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} GO111MODULE=on go build -a -o egress ./cmd/server

RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} GO111MODULE=on go test -c -v --tags=integration ./test


FROM livekit/gstreamer:1.22.5-prod

ARG TARGETPLATFORM

# install deps
RUN apt-get update && \
    apt-get install -y \
        curl \
        ffmpeg \
        fonts-noto \
        gnupg \
        golang \
        pulseaudio \
        unzip \
        wget \
        xvfb \
        gstreamer1.0-plugins-base-

# install chrome
COPY --from=livekit/chrome-installer:117.0.5874.0 /chrome-installer /chrome-installer
RUN /chrome-installer/install-chrome "$TARGETPLATFORM"

# clean up
RUN rm -rf /var/lib/apt/lists/*

# install rtsp server
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then ARCH=arm64v8; else ARCH=amd64; fi && \
    wget https://github.com/aler9/rtsp-simple-server/releases/download/v0.17.6/rtsp-simple-server_v0.17.6_linux_${ARCH}.tar.gz && \
    tar -zxvf rtsp-simple-server_v0.17.6_linux_${ARCH}.tar.gz && \
    rm rtsp-simple-server_v0.17.6_linux_${ARCH}.tar.gz

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
