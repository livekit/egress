#!/bin/bash
set -xeuo pipefail

wget -N https://chromedriver.storage.googleapis.com/2.46/chromedriver_linux64.zip
unzip chromedriver_linux64.zip -d "$HOME/output"
