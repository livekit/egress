FROM livekit/gstreamer:1.18.5-dev

ARG TARGETPLATFORM

WORKDIR /workspace

RUN apt-get update && apt-get install -y golang

# download go modules
COPY go.mod .
COPY go.sum .
RUN go mod download

# copy source
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY version/ version/

RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} GO111MODULE=on go build -a -o livekit-egress ./cmd/server

FROM livekit/gstreamer:1.18.5-prod

ARG TARGETPLATFORM

# install deps
RUN apt-get update && \
    apt-get install -y \
    curl \
    gnupg \
    gstreamer1.0-pulseaudio \
    pulseaudio \
    unzip \
    wget \
    xvfb

# install chrome
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then \
    apt remove chromium-browser chromium-browser-l10n chromium-codecs-ffmpeg-extra && \
    echo "deb http://deb.debian.org/debian buster main \
          deb http://deb.debian.org/debian buster-updates main \
          deb http://deb.debian.org/debian-security buster/updates main" >> /etc/apt/sources.list.d/debian.list && \
    apt-key adv --keyserver keyserver.ubuntu.com --recv-keys DCC9EFBF77E11517 && \
    apt-key adv --keyserver keyserver.ubuntu.com --recv-keys 648ACFD622F3D138 && \
    apt-key adv --keyserver keyserver.ubuntu.com --recv-keys AA8E81B4331F7F50 && \
    apt-key adv --keyserver keyserver.ubuntu.com --recv-keys 112695A0E562B32A && \
    echo 'Package: * \
          Pin: release a=eoan \
          Pin-Priority: 500 \
          \
          \
          Package: * \
          Pin: origin "deb.debian.org" \
          Pin-Priority: 300 \
          \
          \
          Package: chromium* \
          Pin: origin "deb.debian.org" \
          Pin-Priority: 700' >> /etc/apt/preferences.d/chromium.pref && \
    apt update && \
    apt install -y chromium \
    ; else \
    wget https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb && \
    apt-get install -y ./google-chrome-stable_current_amd64.deb && \
    rm google-chrome-stable_current_amd64.deb \
    ; fi

# install chromedriver
RUN wget -N http://chromedriver.storage.googleapis.com/2.46/chromedriver_linux64.zip && \
    unzip chromedriver_linux64.zip && \
    chmod +x chromedriver && \
    mv -f chromedriver /usr/local/bin/chromedriver && \
    rm chromedriver_linux64.zip

# clean
RUN apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# add root user to group for pulseaudio access
RUN adduser root pulse-access

# create xdg_runtime_dir
RUN mkdir -pv ~/.cache/xdgr

# copy binary
COPY --from=0 /workspace/livekit-egress /
COPY build/entrypoint.sh .

ENTRYPOINT ["./entrypoint.sh"]
