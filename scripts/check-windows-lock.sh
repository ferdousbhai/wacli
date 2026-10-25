#!/usr/bin/env bash
set -euo pipefail

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/wacli-lock-windows.XXXXXX")"
trap 'rm -r "$tmp_dir"' EXIT

GOOS=windows GOARCH=amd64 go test -c ./internal/lock -o "$tmp_dir/lock.test.exe"

# The archive has no cgo; it must build for Windows as-is.
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c ./internal/archive -o "$tmp_dir/archive.test.exe"
