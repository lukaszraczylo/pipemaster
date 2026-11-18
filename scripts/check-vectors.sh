#!/usr/bin/env bash
# Regenerates the Lean vectors and fails when they differ from the committed files.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

make vectors >&2
git add -N testdata/lean-vectors
git diff --exit-code -- testdata/lean-vectors
echo "PASS: testdata/lean-vectors matches the Lean model"
