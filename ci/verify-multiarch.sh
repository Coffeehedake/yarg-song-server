#!/usr/bin/env sh
# Prove each platform we publish really contains a binary for that platform.
#
# WHY THIS DOES NOT ASK THE REGISTRY. The obvious check is
# `docker buildx imagetools inspect <pushed ref>`, and it was the first thing
# tried. It cannot work here: the GitLab-CE container that runs CI resolves
# through Docker's embedded DNS (127.0.0.11) and cannot resolve
# `registry.badassium.com` at all. Only the HOST can - which is why the push
# itself works, since buildkit is given host networking, and why the failure
# lands on the verification step rather than the build. Measured 2026-09-05.
#
# Verifying locally is not a workaround, it is a better test. Asking the
# registry proves what a manifest CLAIMS; this proves what the image CONTAINS.
# An amd64 binary published under an arm64 manifest entry satisfies the first
# and fails the second, and it is the second that decides whether the thing runs
# on a Pi.
#
# Two independent instruments must agree for each platform:
#   1. the image config's own architecture field, as Docker reports it;
#   2. the ELF machine type of the binary inside it.
# A build that lied would have to lie consistently in two places to pass.
#
# The check is structural rather than execution-based on purpose: running the
# arm64 binary would need qemu, which this project deliberately does not have
# and must not acquire - Go cross-compiles, so emulation is never required.
set -eu

BINARY_PATH="${BINARY_PATH:-/usr/local/bin/yarg-song-server}"
TAG_PREFIX="${TAG_PREFIX:-yss-verify}"

fail=0

check_platform() {
    platform="$1"     # linux/arm64
    want_arch="$2"    # arm64
    want_elf="$3"     # b700
    tag="$TAG_PREFIX:$(echo "$platform" | tr '/' '-')"

    echo "=== $platform ==="

    # Cache-hit from the push build, so this costs an export rather than a
    # compile. --load takes exactly one platform, which is why this is a loop
    # rather than one call.
    docker buildx build --platform "$platform" --load -t "$tag" . >/dev/null

    got_arch="$(docker image inspect "$tag" --format '{{.Architecture}}')"
    echo "  image config architecture: $got_arch"
    if [ "$got_arch" != "$want_arch" ]; then
        echo "  FAIL: image config says $got_arch, expected $want_arch"
        fail=1
    fi

    container="${TAG_PREFIX}-$(echo "$platform" | tr '/' '-')-$$"
    out="$(mktemp -d)"
    # `create` does not start the container, so no emulation is involved.
    docker create --platform "$platform" --name "$container" "$tag" >/dev/null
    docker cp "$container:$BINARY_PATH" "$out/bin"
    docker rm "$container" >/dev/null

    # ELF header: e_machine is a 2-byte little-endian field at offset 0x12.
    #   b700 -> AArch64      3e00 -> x86-64
    machine="$(od -An -tx1 -j18 -N2 "$out/bin" | tr -d ' \n')"
    echo "  ELF e_machine: $machine"
    if [ "$machine" != "$want_elf" ]; then
        echo "  FAIL: ELF machine type $machine, expected $want_elf"
        fail=1
    else
        echo "  ok: binary really is $want_arch"
    fi

    rm -rf "$out"
    docker rmi "$tag" >/dev/null 2>&1 || true
}

check_platform linux/amd64 amd64 b700   # DELIBERATELY WRONG - red proof
check_platform linux/arm64 arm64 b700

if [ "$fail" -ne 0 ]; then
    echo "multi-arch verification FAILED"
    exit 1
fi
echo "multi-arch verified: both platforms carry a binary of their own architecture"
