#!/usr/bin/env bash
# Installs the pinned engines of the behavioural harness into .tools: act, gitlab-ci-local and the Tekton and Argo
# manifests. woodpecker-cli, drone and fly come from install-linters.sh. A second run with the same pins does nothing.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
dest="$root/.tools/bin"
mkdir -p "$dest" "$root/.tools/manifests"

ACT_VERSION="v0.2.90"
GITLAB_CI_LOCAL_VERSION="4.77.1"
TEKTON_URL="https://infra.tekton.dev/tekton-releases/pipeline/previous/v1.18.0/release.yaml"
TEKTON_SHA256="029c8400e42d0fcd4fc669140d382af1f1d8ae86202b710d6197ae22bd48be5f"
ARGO_URL="https://github.com/argoproj/argo-workflows/releases/download/v4.1.6/install.yaml"
ARGO_SHA256="d25ba490170e35002b746878233d1310c3fad2f648735e85fa8671366a29dee8"

install_act() {
	local stamp="$dest/.act-$ACT_VERSION"
	if [[ -x "$dest/act" && -f "$stamp" ]]; then
		echo "act $ACT_VERSION is already installed in $dest"
		return 0
	fi
	GOBIN="$dest" go install "github.com/nektos/act@$ACT_VERSION"
	rm -f "$dest"/.act-*
	touch "$stamp"
	echo "installed act $ACT_VERSION in $dest"
}

install_gitlab_ci_local() {
	local prefix="$root/.tools/npm"
	local bin="$prefix/node_modules/.bin/gitlab-ci-local"
	local stamp="$prefix/.gitlab-ci-local-$GITLAB_CI_LOCAL_VERSION"
	if [[ -x "$bin" && -f "$stamp" ]]; then
		echo "gitlab-ci-local $GITLAB_CI_LOCAL_VERSION is already installed in $prefix"
		return 0
	fi
	mkdir -p "$prefix"
	npm install --silent --no-audit --no-fund --prefix "$prefix" "gitlab-ci-local@$GITLAB_CI_LOCAL_VERSION"
	rm -f "$prefix"/.gitlab-ci-local-*
	touch "$stamp"
	echo "installed gitlab-ci-local $GITLAB_CI_LOCAL_VERSION in $prefix"
}

# fetch_manifest downloads url to file and fails unless the sha256 matches, so a moved release never installs.
fetch_manifest() {
	local url="$1" want="$2" file="$root/.tools/manifests/$3"
	if [[ -f "$file" ]] && [[ "$(shasum -a 256 "$file" | cut -d' ' -f1)" == "$want" ]]; then
		echo "$3 is already in $root/.tools/manifests"
		return 0
	fi
	curl -fsSL -o "$file.tmp" "$url"
	local got
	got="$(shasum -a 256 "$file.tmp" | cut -d' ' -f1)"
	if [[ "$got" != "$want" ]]; then
		rm -f "$file.tmp"
		echo "$url has sha256 $got, expected $want" >&2
		return 1
	fi
	mv "$file.tmp" "$file"
	echo "fetched $3"
}

install_act
install_gitlab_ci_local
fetch_manifest "$TEKTON_URL" "$TEKTON_SHA256" tekton-v1.18.0.yaml
fetch_manifest "$ARGO_URL" "$ARGO_SHA256" argo-v4.1.6.yaml
