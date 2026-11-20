#!/usr/bin/env bash
# Builds the pinned woodpecker-cli into .tools/bin. A second run with the same pin does nothing.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
tag="v3.20.1"
dest="$root/.tools/bin"
stamp="$dest/.woodpecker-cli-$tag"

if [[ -x "$dest/woodpecker-cli" && -f "$stamp" ]]; then
	echo "woodpecker-cli $tag is already built in $dest"
	exit 0
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$dest"
git clone --quiet --depth 1 --branch "$tag" https://github.com/woodpecker-ci/woodpecker "$tmp/src"
(cd "$tmp/src" && go build -o "$dest/woodpecker-cli" ./cmd/cli)
rm -f "$dest"/.woodpecker-cli-*
touch "$stamp"
echo "built woodpecker-cli $tag in $dest"
