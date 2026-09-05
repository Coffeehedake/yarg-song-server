#!/usr/bin/env sh
# Assert every package in this module is gofmt-clean.
#
# Two things this does not do, both of which were tried and were wrong:
#
#   `gofmt -l .` walks the WHOLE directory, and CI bootstraps its Go toolchain
#   into .toolchain/ inside the project. The Go distribution ships deliberately
#   malformed files under test/ to exercise its own parser, so `gofmt -l .`
#   reported forty syntax errors from Go's own test suite. Asking the module
#   what its packages are also survives a new top-level package, which a
#   hardcoded directory list would not.
#
#   `gofmt -l $(go list -f '{{.Dir}}' ./...)` splits every path on whitespace.
#   The working copy on ENG-1 lives under "YARG - Open Source Contributions",
#   so every directory became five nonexistent arguments - and gofmt writes
#   those errors to STDERR, which does not land in the command substitution.
#   The check printed "gofmt clean" having examined nothing. A green from an
#   instrument that cannot fail is worse than no check.
set -eu

dirs="$(mktemp)"
trap 'rm -f "$dirs"' EXIT
go list -f '{{.Dir}}' ./... > "$dirs"

count=0
failed=0
unformatted=""

while IFS= read -r dir; do
    [ -n "$dir" ] || continue
    count=$((count + 1))
    if ! out="$(gofmt -l "$dir")"; then
        echo "gofmt could not read: $dir"
        failed=1
        continue
    fi
    if [ -n "$out" ]; then
        unformatted="${unformatted}${out}
"
    fi
done < "$dirs"

if [ "$count" -eq 0 ]; then
    echo "no packages found - this check examined nothing, which is a failure"
    exit 1
fi

if [ -n "$unformatted" ]; then
    echo "not gofmt-clean:"
    printf '%s' "$unformatted"
    failed=1
fi

if [ "$failed" -eq 0 ]; then
    echo "gofmt clean across $count packages"
fi
exit "$failed"
