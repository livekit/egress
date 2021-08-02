FROM buildkite/puppeteer:latest

# Install pulse audio
RUN apt-get -qq update && apt-get install -y pulseaudio

# Add root user to group for pulseaudio access
RUN adduser root pulse-access

# xvfb
RUN apt-get install -y xvfb

# ffmpeg
RUN apt-get install -y ffmpeg

# Copy recorder
WORKDIR /app
COPY package.json package-lock.json tsconfig.json ./
COPY src ./src
RUN npm install

# Silence error about livekit-server-sdk protos
RUN npx tsc src/*.ts; exit 0

COPY entrypoint.sh .
ENTRYPOINT ["./entrypoint.sh"]
