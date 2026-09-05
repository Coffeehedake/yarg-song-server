#!/usr/bin/env sh
# Make sure `docker buildx` is available, verified before it is trusted.
#
# Why this cannot simply be assumed: the GitLab runner lives INSIDE the
# GitLab-CE container, and Unraid recreates that container nightly at 04:00 with
# restart=no. buildx is not part of the image - it is a CLI plugin under
# $HOME/.docker/cli-plugins, which the recreate takes with it. So "is buildx
# here?" is a question this pipeline has to ask every run rather than answer
# once.
#
# The download is checked against docker/buildx's own published checksums.txt
# BEFORE the file is made executable. A build tool fetched over the network and
# run unverified is a supply chain nobody is watching, and this pipeline already
# holds its Go toolchain to that standard.
set -eu

BUILDX_VERSION="${BUILDX_VERSION:-v0.34.0}"
BUILDX_SHA256="${BUILDX_SHA256:-0144479d5a1cd710be3464ae898628cfa68033e16b225aef52f81930c45ac9b5}"

if docker buildx version >/dev/null 2>&1; then
    echo "buildx: already present - $(docker buildx version)"
    exit 0
fi

echo "buildx: not present, installing ${BUILDX_VERSION}"
plugins="$HOME/.docker/cli-plugins"
mkdir -p "$plugins"
tmp="$plugins/.docker-buildx.partial"

url="https://github.com/docker/buildx/releases/download/${BUILDX_VERSION}/buildx-${BUILDX_VERSION}.linux-amd64"
curl -fsSL --retry 3 --max-time 300 -o "$tmp" "$url"

actual="$(sha256sum "$tmp" | cut -d' ' -f1)"
if [ "$actual" != "$BUILDX_SHA256" ]; then
    echo "buildx: SHA-256 MISMATCH for $url"
    echo "  expected $BUILDX_SHA256"
    echo "  actual   $actual"
    rm -f "$tmp"
    exit 1
fi
echo "buildx: SHA-256 verified"

chmod +x "$tmp"
mv "$tmp" "$plugins/docker-buildx"
docker buildx version
