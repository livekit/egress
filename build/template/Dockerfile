FROM ubuntu:22.04

WORKDIR /workspace

RUN apt update
RUN apt install -y curl
RUN curl -sL https://deb.nodesource.com/setup_14.x | bash -
RUN curl -sL https://dl.yarnpkg.com/debian/pubkey.gpg | apt-key add -
RUN echo "deb https://dl.yarnpkg.com/debian/ stable main" | tee /etc/apt/sources.list.d/yarn.list
RUN apt update
RUN apt install -y nodejs yarn

# copy templates
COPY template-default/ .

# build
RUN yarn install
RUN yarn lint
RUN yarn build
