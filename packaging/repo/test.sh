#!/bin/bash
# Checks for the signed-repository installer and the package recipe.
# add_signed_repo is shared verbatim with the installers of the owner's other
# products (cloud-calendar, iCloud for Omarchy, ...), and each repository pins
# its hash: change it in all of them together.
set -euo pipefail
cd "$(dirname "$0")"

bash -n install.sh

hash_of() { sed -n '/^# --- add_signed_repo (shared) ---$/,/^# --- end add_signed_repo ---$/p' "$1" | sed '1d;$d' | sha256sum | cut -d' ' -f1; }
[[ "$(hash_of install.sh)" == "$(cat add_signed_repo.sha256)" ]] \
  || { echo "add_signed_repo in install.sh differs from the pinned shared copy" >&2; exit 1; }

grep -q '^SIGNING_KEY_FINGERPRINT=[0-9A-F]\{40\}$' install.sh \
  || { echo "install.sh has no signing key fingerprint pinned" >&2; exit 1; }

source_version=$(sed -n 's/^const version = "\(.*\)"$/\1/p' ../../cmd/wacli/main.go)
pkgver=$(sed -n 's/^pkgver=//p' ../aur/wacli/PKGBUILD)
[[ -n $source_version && $pkgver == "$source_version" ]] \
  || { echo "PKGBUILD pkgver ($pkgver) differs from cmd/wacli/main.go's version ($source_version)" >&2; exit 1; }

echo "ok - installer syntax, shared add_signed_repo hash, pinned key and package version"
