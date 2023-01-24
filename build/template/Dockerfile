FROM ubuntu:22.04

WORKDIR /workspace

# install go
RUN apt-get update && apt-get install -y yarnpkg

# copy templates
COPY template-default/ .

# build
RUN yarnpkg install
RUN yarnpkg lint
RUN yarnpkg build
