#!/bin/bash
set -euxo pipefail

if [ "$1" = "linux/arm64" ]
then
  apt-get update
  apt-get install -y \
    ca-certificates \
    libasound2 \
    libatk-bridge2.0-0 \
    libatk1.0-0 \
    libc6 \
    libcairo2 \
    libcups2 \
    libdbus-1-3 \
    libexpat1 \
    libfontconfig1 \
    libgbm1 \
    libgcc1 \
    libglib2.0-0 \
    libnspr4 \
    libnss3 \
    libpango-1.0-0 \
    libpangocairo-1.0-0 \
    libstdc++6 \
    libx11-6 \
    libx11-xcb1 \
    libxcb1 \
    libxcomposite1 \
    libxcursor1 \
    libxdamage1 \
    libxext6 \
    libxfixes3 \
    libxi6 \
    libxrandr2 \
    libxrender1 \
    libxss1 \
    libxtst6
  mv /chrome-installer/arm64/ /chrome
  cp /chrome/chrome_sandbox /usr/local/sbin/chrome-devel-sandbox
  chown root:root /usr/local/sbin/chrome-devel-sandbox
  chmod 4755 /usr/local/sbin/chrome-devel-sandbox
else
  wget https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb
  apt-get install -y ./google-chrome-stable_current_amd64.deb
  rm google-chrome-stable_current_amd64.deb
fi

wget -N https://chromedriver.storage.googleapis.com/2.46/chromedriver_linux64.zip
unzip chromedriver_linux64.zip
chmod +x chromedriver
mv -f chromedriver /usr/local/bin/chromedriver
rm chromedriver_linux64.zip

rm -rf /chrome-installer
