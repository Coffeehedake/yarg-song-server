#!/usr/bin/env sh
# The Dockerfile's builder image must not be older than go.mod's language
# version, or the container build fails in a way nothing local reproduces.
#
# This exists because it already happened: `go get golang.org/x/text` raised the
# go directive from 1.24 to 1.25 as a side effect, and the Dockerfile went on
# saying golang:1.24-alpine. Nothing on a workstation notices, because a
# workstation builds with its own newer toolchain.
set -eu

mod_version="$(awk '/^go /{print $2; exit}' go.mod)"
img_version="$(awk 'match($0, /golang:[0-9]+\.[0-9]+/) { print substr($0, RSTART+7, RLENGTH-7); exit }' Dockerfile)"

if [ -z "$mod_version" ] || [ -z "$img_version" ]; then
    echo "could not read both versions: go.mod='$mod_version' Dockerfile='$img_version'"
    exit 1
fi

# Compare as major.minor only; go.mod may carry a patch, the image tag does not.
mod_mm="$(echo "$mod_version" | cut -d. -f1,2)"
echo "go.mod requires go $mod_version (major.minor $mod_mm); Dockerfile builds on golang:$img_version"

lowest="$(printf '%s\n%s\n' "$mod_mm" "$img_version" | sort -t. -k1,1n -k2,2n | head -1)"
if [ "$lowest" != "$mod_mm" ] && [ "$mod_mm" != "$img_version" ]; then
    echo "MISMATCH: the Dockerfile's Go is older than go.mod requires."
    echo "Raise the golang: tag in Dockerfile to at least $mod_mm."
    exit 1
fi
echo "ok"
