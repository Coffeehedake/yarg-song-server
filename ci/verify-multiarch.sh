#!/usr/bin/env sh
# Prove a pushed image really is multi-arch, and that its arm64 half really
# contains an arm64 binary.
#
# The weak version of this check - "buildx exited 0, so the manifest must be
# fine" - is the failure this exists to prevent. A build that silently produces
# an amd64 binary under an arm64 manifest entry passes every test that only
# reads exit codes, and then does not start on the Pi. So this asserts two
# separate things:
#
#   1. the manifest list actually advertises linux/amd64 AND linux/arm64;
#   2. the binary inside the arm64 image has ELF machine type AArch64.
#
# The second is a structural check rather than an execution one, deliberately.
# Running the arm64 binary would need qemu, which this project specifically does
# NOT depend on - Go cross-compiles, so no emulation is installed. Reading the
# ELF header proves the same thing without it.
set -eu

IMAGE="${1:?usage: verify-multiarch.sh <image-ref>}"
BINARY_PATH="${2:-/usr/local/bin/yarg-song-server}"

echo "=== manifest for $IMAGE ==="
manifest="$(docker buildx imagetools inspect "$IMAGE")"
printf '%s\n' "$manifest"

missing=0
for want in linux/amd64 linux/arm64; do
    if printf '%s\n' "$manifest" | grep -q "$want"; then
        echo "  ok: $want present"
    else
        echo "  MISSING: $want"
        missing=1
    fi
done
[ "$missing" -eq 0 ] || { echo "manifest does not cover both platforms"; exit 1; }

echo "=== ELF check: the arm64 image must contain an aarch64 binary ==="
container="yss-verify-$$"
out="$(mktemp -d)"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true; rm -rf "$out"' EXIT

# `create` does not start the container, so this needs no emulation.
docker create --platform linux/arm64 --name "$container" "$IMAGE" >/dev/null
docker cp "$container:$BINARY_PATH" "$out/bin"

# ELF header: e_machine is a 2-byte little-endian field at offset 0x12.
#   0xB7 0x00 -> AArch64      0x3E 0x00 -> x86-64
machine="$(od -An -tx1 -j18 -N2 "$out/bin" | tr -d ' \n')"
echo "  e_machine bytes: $machine"
case "$machine" in
    b700) echo "  ok: AArch64" ;;
    3e00) echo "  FAIL: this is an x86-64 binary wearing an arm64 manifest entry"; exit 1 ;;
    *)    echo "  FAIL: unrecognised ELF machine type"; exit 1 ;;
esac

echo "multi-arch verified: manifest covers both platforms and the arm64 image really is arm64"
