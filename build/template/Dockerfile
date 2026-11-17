# Copyright 2023 LiveKit, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

FROM ubuntu:24.04

WORKDIR /workspace

RUN apt update
RUN apt install -y curl

# install the Node version pinned in .node-version, shared with CI
COPY .node-version /tmp/.node-version
RUN NODE_VERSION=$(cat /tmp/.node-version) && \
    case "$(dpkg --print-architecture)" in \
      amd64) NODE_ARCH=x64 ;; \
      arm64) NODE_ARCH=arm64 ;; \
      *) echo "unsupported architecture" >&2; exit 1 ;; \
    esac && \
    NODE_TAR=node-v${NODE_VERSION}-linux-${NODE_ARCH}.tar.gz && \
    cd /tmp && \
    curl -fsSLO https://nodejs.org/dist/v${NODE_VERSION}/${NODE_TAR} && \
    curl -fsSL https://nodejs.org/dist/v${NODE_VERSION}/SHASUMS256.txt | grep " ${NODE_TAR}\$" | sha256sum -c - && \
    tar -xzf ${NODE_TAR} -C /usr/local --strip-components=1 --no-same-owner && \
    rm ${NODE_TAR} /tmp/.node-version
RUN npm install -g pnpm@12

# copy templates
COPY template-default/ .

# build
RUN pnpm install
RUN pnpm build
