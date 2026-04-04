#!/bin/bash
set -xeuo pipefail

export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get install -y sudo zip unzip curl git netcat-openbsd

id chrome >/dev/null 2>&1 || useradd -m -d /home/chrome -s /bin/bash chrome
mkdir -p /home/chrome/.ssh
cp /root/.ssh/authorized_keys /home/chrome/.ssh/authorized_keys
chown -R chrome:chrome /home/chrome/.ssh
chmod 700 /home/chrome/.ssh
chmod 600 /home/chrome/.ssh/authorized_keys

usermod -aG sudo chrome

grep -q '^chrome ALL=(ALL:ALL) NOPASSWD: ALL$' /etc/sudoers || \
  echo 'chrome ALL=(ALL:ALL) NOPASSWD: ALL' >> /etc/sudoers

grep -q '^ClientAliveInterval 60$' /etc/ssh/sshd_config || \
  echo 'ClientAliveInterval 60' >> /etc/ssh/sshd_config

grep -q '^ClientAliveCountMax 3$' /etc/ssh/sshd_config || \
  echo 'ClientAliveCountMax 3' >> /etc/ssh/sshd_config

systemctl restart ssh
