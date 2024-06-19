#!/bin/bash
set -xeuo pipefail

wget https://storage.googleapis.com/chrome-for-testing-public/"$1"/linux64/chromedriver-linux64.zip
unzip chromedriver-linux64.zip -d "$HOME/output/amd64"
wget https://storage.googleapis.com/chrome-for-testing-public/"$1"/mac-arm64/chromedriver-mac-arm64.zip
unzip chromedriver-mac-arm64.zip -d "$HOME/output/arm64"
