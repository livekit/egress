#!/bin/bash

image_suffix=(base dev prod prod-rs)
archs=(amd64 arm64)
gst_version=1.22.8

for suffix in ${image_suffix[*]}
do
    digests=()
    for arch in ${archs[*]}
    do
        digest=`docker manifest inspect livekit/gstreamer:$gst_version-$suffix-$arch | jq ".manifests[] | select(.platform.architecture == \"$arch\").digest"`
        # remove quotes
        digest=${digest:1:$[${#digest}-2]}
        digests+=($digest)
    done

    manifests=""
    for digest in ${digests[*]}
    do
        manifests+=" livekit/gstreamer@$digest"
    done

    docker manifest create livekit/gstreamer:$gst_version-$suffix$manifests
    docker manifest push livekit/gstreamer:$gst_version-$suffix
done

