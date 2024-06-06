#!/bin/bash

version_full=$(tr '\n' ' ' < /home/chrome/chromium/src/chrome/VERSION)
version_delim="${version_full// /=}"

IFS='='
split=($version_delim)
unset IFS

echo "${split[1]}.${split[3]}.${split[5]}.${split[7]}"
