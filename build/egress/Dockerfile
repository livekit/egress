FROM livekit/gstreamer:1.20.4-dev

ARG TARGETPLATFORM

WORKDIR /workspace

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
COPY --from=livekit/egress-templates workspace/build/ cmd/server/templates/
# delete .map files
RUN find cmd/server/templates/ -name *.map | xargs rm

# build
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} GO111MODULE=on go build -a -o egress ./cmd/server

FROM livekit/gstreamer:1.20.4-prod

ARG TARGETPLATFORM

# install deps
RUN apt-get update && \
    apt-get install -y \
    curl \
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

# setup
RUN adduser root pulse-access && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -pv ~/.cache/xdgr

# update env
ENV PATH=${PATH}:/chrome
ENV XDG_RUNTIME_DIR=$PATH:~/.cache/xdgr

# egress
COPY --from=0 /workspace/egress /bin/

# run
COPY build/egress/entrypoint.sh /
ENTRYPOINT ["/entrypoint.sh"]
