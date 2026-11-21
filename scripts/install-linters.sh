#!/usr/bin/env bash
# Installs the pinned linters into .tools/bin. A second run with the same pins does nothing.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
dest="$root/.tools/bin"
mkdir -p "$dest"

install_woodpecker() {
	local tag="v3.20.1"
	# Commit of the tag; a moved or replaced tag fails the build instead of installing other code.
	local sha="3f1bb0bd0db28e410ad797034d75780116122c91"
	local stamp="$dest/.woodpecker-cli-$tag-$sha"

	if [[ -x "$dest/woodpecker-cli" && -f "$stamp" ]]; then
		echo "woodpecker-cli $tag is already built in $dest"
		return 0
	fi

	local tmp
	tmp="$(mktemp -d "${TMPDIR:-/tmp}/install-linters.XXXXXX")"
	trap "rm -rf '$tmp'" EXIT
	# The path expands now: the local variable is gone when the trap runs at script exit.
	git clone --quiet --depth 1 --branch "$tag" https://github.com/woodpecker-ci/woodpecker "$tmp/src"
	local got
	got="$(git -C "$tmp/src" rev-parse HEAD)"
	if [[ "$got" != "$sha" ]]; then
		rm -rf "$tmp"
		echo "woodpecker $tag is at $got, expected $sha" >&2
		return 1
	fi
	# The linttool package reads this version to reject a different woodpecker-cli.
	(cd "$tmp/src" && go build -ldflags "-X go.woodpecker-ci.org/woodpecker/v3/version.Version=${tag#v}" -o "$dest/woodpecker-cli" ./cmd/cli)
	rm -rf "$tmp"
	trap - EXIT
	rm -f "$dest"/.woodpecker-cli-*
	touch "$stamp"
	echo "built woodpecker-cli $tag in $dest"
}

# drone-cli needs a replace directive in go.mod that `go install pkg@version` rejects, so the script installs the
# release tarball. The sha256 values come from the release asset digests and drone_checksums.txt of v1.9.0.
install_drone() {
	local tag="v1.9.0"
	local os arch asset want
	os="$(uname -s | tr '[:upper:]' '[:lower:]')"
	arch="$(uname -m)"
	case "$arch" in
	x86_64) arch="amd64" ;;
	aarch64) arch="arm64" ;;
	esac
	asset="drone_${os}_${arch}.tar.gz"
	case "$asset" in
	drone_darwin_amd64.tar.gz) want="048bb11b7c2879e7228bcc354eb72d1724369a5f2a65ddcfa32bab1580b4c661" ;;
	drone_darwin_arm64.tar.gz) want="02258fff5227c3d4880124da33b0a57da07c0672425d7d7c9325ca0ab62458a9" ;;
	drone_linux_amd64.tar.gz) want="9a9b8a254829edbce4fd3a895d6338fc59164a5a6b78a3d945120c4ff6963f3b" ;;
	drone_linux_arm64.tar.gz) want="040a31552c60e49550eeeffd875bffda517f71eee30075826d3cf3e64e584ed3" ;;
	*)
		echo "no pinned drone-cli $tag release for $os/$arch" >&2
		return 1
		;;
	esac
	local stamp="$dest/.drone-$tag-$want"

	if [[ -x "$dest/drone" && -f "$stamp" ]]; then
		echo "drone $tag is already installed in $dest"
		return 0
	fi

	local tmp got
	tmp="$(mktemp -d "${TMPDIR:-/tmp}/install-linters.XXXXXX")"
	trap "rm -rf '$tmp'" EXIT
	curl -fsSL -o "$tmp/$asset" "https://github.com/harness/drone-cli/releases/download/$tag/$asset"
	got="$( (sha256sum "$tmp/$asset" 2>/dev/null || shasum -a 256 "$tmp/$asset") | cut -d' ' -f1)"
	if [[ "$got" != "$want" ]]; then
		rm -rf "$tmp"
		echo "$asset has sha256 $got, expected $want" >&2
		return 1
	fi
	tar -xzf "$tmp/$asset" -C "$tmp" drone
	install -m 0755 "$tmp/drone" "$dest/drone"
	rm -rf "$tmp"
	trap - EXIT
	rm -f "$dest"/.drone-*
	touch "$stamp"
	echo "installed drone $tag in $dest"
}

# kubeconform ships static release tarballs. The sha256 values come from the release asset digests and the CHECKSUMS
# file of v0.8.0.
install_kubeconform() {
	local tag="v0.8.0"
	local os arch asset want
	os="$(uname -s | tr '[:upper:]' '[:lower:]')"
	arch="$(uname -m)"
	case "$arch" in
	x86_64) arch="amd64" ;;
	aarch64) arch="arm64" ;;
	esac
	asset="kubeconform-${os}-${arch}.tar.gz"
	case "$asset" in
	kubeconform-darwin-amd64.tar.gz) want="71dbc87ac9f24099a62b93570e65aa06312ba6ac8aea63b7f86e9d999edf5a92" ;;
	kubeconform-darwin-arm64.tar.gz) want="f84f4dfbebf4a6b0b230385fa065a39ea35e02608c2b50d025dcf64775a69d67" ;;
	kubeconform-linux-amd64.tar.gz) want="9bc2bffbf71f261128533edaf912153948b7ff238f9a531ae6d34466ec287883" ;;
	kubeconform-linux-arm64.tar.gz) want="1f53fc8e81258197a35e8603054162a5af1de8c5af13746c71ab680d9534ed87" ;;
	*)
		echo "no pinned kubeconform $tag release for $os/$arch" >&2
		return 1
		;;
	esac
	local stamp="$dest/.kubeconform-$tag-$want"

	if [[ -x "$dest/kubeconform" && -f "$stamp" ]]; then
		echo "kubeconform $tag is already installed in $dest"
		return 0
	fi

	local tmp got
	tmp="$(mktemp -d "${TMPDIR:-/tmp}/install-linters.XXXXXX")"
	trap "rm -rf '$tmp'" EXIT
	curl -fsSL -o "$tmp/$asset" "https://github.com/yannh/kubeconform/releases/download/$tag/$asset"
	got="$( (sha256sum "$tmp/$asset" 2>/dev/null || shasum -a 256 "$tmp/$asset") | cut -d' ' -f1)"
	if [[ "$got" != "$want" ]]; then
		rm -rf "$tmp"
		echo "$asset has sha256 $got, expected $want" >&2
		return 1
	fi
	tar -xzf "$tmp/$asset" -C "$tmp" kubeconform
	install -m 0755 "$tmp/kubeconform" "$dest/kubeconform"
	rm -rf "$tmp"
	trap - EXIT
	rm -f "$dest"/.kubeconform-*
	touch "$stamp"
	echo "installed kubeconform $tag in $dest"
}

install_woodpecker
install_drone
install_kubeconform
