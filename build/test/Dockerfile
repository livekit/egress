FROM livekit/gstreamer:1.18.5-dev

WORKDIR /workspace

# install deps
RUN apt-get update && \
    apt-get install -y \
    curl \
    ffmpeg \
    gnupg \
    golang \
    gstreamer1.0-pulseaudio \
    pulseaudio \
    unzip \
    wget \
    xvfb

# install rtmp server
RUN wget https://github.com/aler9/rtsp-simple-server/releases/download/v0.17.6/rtsp-simple-server_v0.17.6_linux_amd64.tar.gz && \
    tar -zxvf rtsp-simple-server_v0.17.6_linux_amd64.tar.gz && \
    rm rtsp-simple-server_v0.17.6_linux_amd64.tar.gz

# install chrome
RUN wget https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb && \
    apt-get install -y ./google-chrome-stable_current_amd64.deb && \
    rm google-chrome-stable_current_amd64.deb

# install chromedriver
RUN wget -N http://chromedriver.storage.googleapis.com/2.46/chromedriver_linux64.zip && \
    unzip chromedriver_linux64.zip && \
    chmod +x chromedriver && \
    mv -f chromedriver /usr/local/bin/chromedriver && \
    rm chromedriver_linux64.zip

# clean up
RUN apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# add root user to group for pulseaudio access
RUN adduser root pulse-access

# create xdg_runtime_dir
RUN mkdir -pv ~/.cache/xdgr

# download go modules
COPY go.mod .
COPY go.sum .
RUN go mod download

# copy source
COPY pkg/ pkg/
COPY test/ test/
COPY build/test/entrypoint.sh .

# run
ENTRYPOINT ["./entrypoint.sh"]
