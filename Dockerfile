FROM golang:1.16-alpine as builder

WORKDIR /workspace

# Copy the Go Modules manifests
COPY service/go.mod go.mod
COPY service/go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY service/cmd/ cmd/
COPY service/pkg/ pkg/
COPY service/proto/ proto/
COPY service/tools/ tools/
COPY service/version/ version/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o livekit-recorder-service ./cmd/server

FROM buildkite/puppeteer:latest

COPY --from=builder /workspace/livekit-recorder-service /livekit-recorder-service

# Install pulse audio
RUN apt-get -qq update && apt-get install -y pulseaudio

# Add root user to group for pulseaudio access
RUN adduser root pulse-access

# xvfb
RUN apt-get install -y xvfb

# ffmpeg
RUN apt-get install -y ffmpeg

# node
RUN apt-get install -y nodejs

# Copy recorder
WORKDIR /app
COPY recorder/package.json recorder/package-lock.json recorder/tsconfig.json ./
COPY recorder/src ./src
RUN npm install

# Silence error about livekit-server-sdk protos
RUN npx tsc src/*.ts; exit 0

# Run the service
WORKDIR /
COPY entrypoint.sh .
ENTRYPOINT ["./entrypoint.sh"]
