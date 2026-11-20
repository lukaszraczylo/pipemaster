#!/usr/bin/env bash
# Builds the pinned woodpecker-cli into .tools/bin. A second run with the same pin does nothing.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
tag="v3.20.1"
# Commit of the tag; a moved or replaced tag fails the build instead of installing other code.
sha="3f1bb0bd0db28e410ad797034d75780116122c91"
dest="$root/.tools/bin"
stamp="$dest/.woodpecker-cli-$tag-$sha"

if [[ -x "$dest/woodpecker-cli" && -f "$stamp" ]]; then
	echo "woodpecker-cli $tag is already built in $dest"
	exit 0
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$dest"
git clone --quiet --depth 1 --branch "$tag" https://github.com/woodpecker-ci/woodpecker "$tmp/src"
got="$(git -C "$tmp/src" rev-parse HEAD)"
if [[ "$got" != "$sha" ]]; then
	echo "woodpecker $tag is at $got, expected $sha" >&2
	exit 1
fi
# The linttool package reads this version to reject a different woodpecker-cli.
(cd "$tmp/src" && go build -ldflags "-X go.woodpecker-ci.org/woodpecker/v3/version.Version=${tag#v}" -o "$dest/woodpecker-cli" ./cmd/cli)
rm -f "$dest"/.woodpecker-cli-*
touch "$stamp"
echo "built woodpecker-cli $tag in $dest"
