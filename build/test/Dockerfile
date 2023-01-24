FROM livekit/gstreamer:1.20.4-dev

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

# copy templates
COPY --from=livekit/egress-templates workspace/build/ pkg/service/templates/

COPY test/ test/

# build (service tests will need to launch the handler)
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} GO111MODULE=on go build -a -o egress ./cmd/server

RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} GO111MODULE=on go test -c -v --tags=integration ./test


FROM livekit/gstreamer:1.20.4-prod

ARG TARGETPLATFORM

# install deps
RUN apt-get update && \
    apt-get install -y \
        curl \
        ffmpeg \
        fonts-noto \
        gnupg \
        gstreamer1.0-pulseaudio \
        pulseaudio \
        unzip \
        wget \
        xvfb

# install chrome
COPY --from=livekit/chrome-installer:latest /chrome-installer /chrome-installer
RUN /chrome-installer/install-chrome "$TARGETPLATFORM" && \
    rm -rf /chrome-installer

# install rtsp server
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then ARCH=arm64v8; else ARCH=amd64; fi && \
    wget https://github.com/aler9/rtsp-simple-server/releases/download/v0.17.6/rtsp-simple-server_v0.17.6_linux_${ARCH}.tar.gz && \
    tar -zxvf rtsp-simple-server_v0.17.6_linux_${ARCH}.tar.gz && \
    rm rtsp-simple-server_v0.17.6_linux_${ARCH}.tar.gz

# add root user to group for pulseaudio access
RUN adduser root pulse-access

# clean up
RUN rm -rf /var/lib/apt/lists/* && \
    rm -rf /var/run/pulse /var/lib/pulse /root/.config/pulse && \
    mkdir -pv ~/.cache/xdgr

# update env
ENV PATH=${PATH}:/chrome
ENV XDG_RUNTIME_DIR=$PATH:~/.cache/xdgr
ENV RTSP_LOGDESTINATIONS=file

COPY test/ /workspace/test/

# egress
COPY --from=0 /workspace/egress /bin/

# test
COPY --from=0 /workspace/test.test .

# run
COPY build/test/entrypoint.sh .
ENTRYPOINT ["./entrypoint.sh"]
