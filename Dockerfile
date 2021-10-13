FROM restreamio/gstreamer:1.18.5.0-dev as builder

WORKDIR /workspace

RUN apt-get update && apt-get install -y golang

# Copy the Go Modules manifests
COPY go.mod .
COPY go.sum .
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY version/ version/

WORKDIR /workspace
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o livekit-recorder ./cmd/server

FROM restreamio/gstreamer:1.18.5.0-prod

COPY --from=builder /workspace/livekit-recorder /livekit-recorder

# install deps
RUN apt-get update && \
    apt-get install -y curl unzip wget gnupg xvfb pulseaudio gstreamer1.0-pulseaudio

# install chrome
RUN wget https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb && \
    apt-get install -y ./google-chrome-stable_current_amd64.deb

# install chromedriver
RUN wget -N http://chromedriver.storage.googleapis.com/2.46/chromedriver_linux64.zip && \
    unzip chromedriver_linux64.zip && \
    chmod +x chromedriver && \
    mv -f chromedriver /usr/local/bin/chromedriver

# Add root user to group for pulseaudio access
RUN adduser root pulse-access

# create xdg_runtime_dir
RUN mkdir -pv ~/.cache/xdgr

# run
COPY entrypoint.sh .
ENTRYPOINT ["./entrypoint.sh"]
