FROM golang:1.16-alpine as builder

WORKDIR /workspace

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY proto/ proto/
COPY tools/ tools/
COPY version/ version/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o livekit-recorder ./cmd/worker

FROM buildkite/puppeteer:latest

COPY --from=builder /workspace/livekit-recorder /livekit-recorder

# Install pulse audio
RUN apt-get -qq update && apt-get install -y pulseaudio

# add root user to group for pulseaudio access
RUN adduser root pulse-access

# Xvfb
RUN apt-get install -y xvfb

# ffmpeg
RUN apt-get install -y ffmpeg

# copy node recorder
WORKDIR /app
COPY package.json package-lock.json tsconfig.json ./
COPY src ./src
RUN npm install \
    && npm install typescript \
    && npm install -g ts-node

# Run the worker
WORKDIR /
COPY entrypoint.sh .
ENTRYPOINT ./entrypoint.sh
