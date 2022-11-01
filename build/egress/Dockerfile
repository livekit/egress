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

# build
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} GO111MODULE=on go build -a -o egress ./cmd/server

FROM livekit/chromium:1.20.4-prod

# add root user to group for pulseaudio access
RUN adduser root pulse-access && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -pv ~/.cache/xdgr

# set xdg_runtime_dir
ENV XDG_RUNTIME_DIR=$PATH:~/.cache/xdgr

# copy binary
COPY --from=0 /workspace/egress /bin/

# run
COPY build/egress/entrypoint.sh /
ENTRYPOINT ["/entrypoint.sh"]
