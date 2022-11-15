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

FROM livekit/gstreamer:1.20.4-prod

# setup
RUN adduser root pulse-access && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -pv ~/.cache/xdgr

ENV XDG_RUNTIME_DIR=$PATH:~/.cache/xdgr

# install chrome
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then \
      COPY build/chrome/chrome . \
    ; else \
      wget https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb && \
      apt-get install -y ./google-chrome-stable_current_amd64.deb && \
      rm google-chrome-stable_current_amd64.deb \
    ; fi \

RUN wget -N http://chromedriver.storage.googleapis.com/2.46/chromedriver_linux64.zip && \
    unzip chromedriver_linux64.zip && \
    chmod +x chromedriver && \
    mv -f chromedriver /usr/local/bin/chromedriver && \
    rm chromedriver_linux64.zip

# egress
COPY --from=0 /workspace/egress /bin/

# run
COPY build/egress/entrypoint.sh /
ENTRYPOINT ["/entrypoint.sh"]
