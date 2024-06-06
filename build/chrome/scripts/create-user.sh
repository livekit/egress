#!/bin/bash

useradd -m -d /home/chrome -s /bin/bash chrome
mkdir /home/chrome/.ssh
cp /root/.ssh/authorized_keys /home/chrome/.ssh/authorized_keys
chown -R chrome:chrome /home/chrome/.ssh
chmod 700 /home/chrome/.ssh
chmod 600 /home/chrome/.ssh/authorized_keys
adduser chrome sudo
sed -i '54i chrome ALL=(ALL:ALL) NOPASSWD: ALL' /etc/sudoers
