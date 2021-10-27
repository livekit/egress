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
COPY test/ test/
COPY version/ version/

# install rtmp server
RUN wget https://github.com/aler9/rtsp-simple-server/releases/download/v0.17.6/rtsp-simple-server_v0.17.6_linux_amd64.tar.gz && \
    tar -zxvf rtsp-simple-server_v0.17.6_linux_amd64.tar.gz

# install deps
RUN apt-get update && \
    apt-get install -y curl unzip wget gnupg xvfb pulseaudio gstreamer1.0-pulseaudio ffmpeg

# install chrome
RUN wget https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb && \
    apt-get install -y ./google-chrome-stable_current_amd64.deb

# install chromedriver
RUN wget -N http://chromedriver.storage.googleapis.com/2.46/chromedriver_linux64.zip && \
    unzip chromedriver_linux64.zip && \
    chmod +x chromedriver && \
    mv -f chromedriver /usr/local/bin/chromedriver

RUN apt-get clean && \
    rm -rf /var/lib/apt/lists/* && \
    rm google-chrome-stable_current_amd64.deb && \
    rm chromedriver_linux64.zip

# Add root user to group for pulseaudio access
RUN adduser root pulse-access

# create xdg_runtime_dir
RUN mkdir -pv ~/.cache/xdgr

# run
ENTRYPOINT ["./test/entrypoint.sh"]
