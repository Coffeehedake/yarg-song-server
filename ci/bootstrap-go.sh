#!/usr/bin/env sh
# Put a pinned Go toolchain in the build directory, verified before it is used.
#
# The Vault2 runner is a SHELL executor, not a container one: `image:` is
# ignored and nothing is guaranteed to be installed. Rather than depend on
# whatever happens to be on the host - which makes a pipeline that passes today
# and fails after an unrelated upgrade - each pipeline brings its own toolchain
# and GitLab's cache keeps it between runs.
#
# The SHA-256 is checked BEFORE extraction and the script stops if it does not
# match. A toolchain fetched over the network and run unverified is a supply
# chain nobody is watching.
set -eu

GO_VERSION="${GO_VERSION:?GO_VERSION must be set}"
GO_SHA256="${GO_SHA256:?GO_SHA256 must be set}"
DEST="${1:-.toolchain}"

if [ -x "$DEST/go/bin/go" ]; then
    have="$("$DEST/go/bin/go" version | awk '{print $3}')"
    if [ "$have" = "go${GO_VERSION}" ]; then
        echo "toolchain: go${GO_VERSION} already present (cached)"
        exit 0
    fi
    echo "toolchain: cached $have is not go${GO_VERSION}, replacing"
    rm -rf "$DEST"
fi

mkdir -p "$DEST"
tarball="$DEST/go.tar.gz"
url="https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"

echo "toolchain: fetching go${GO_VERSION}"
curl -sSL --retry 3 --max-time 300 -o "$tarball" "$url"

actual="$(sha256sum "$tarball" | cut -d' ' -f1)"
if [ "$actual" != "$GO_SHA256" ]; then
    echo "toolchain: SHA-256 MISMATCH for $url"
    echo "  expected $GO_SHA256"
    echo "  actual   $actual"
    rm -f "$tarball"
    exit 1
fi
echo "toolchain: SHA-256 verified"

tar -C "$DEST" -xzf "$tarball"
rm -f "$tarball"
"$DEST/go/bin/go" version
