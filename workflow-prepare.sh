#!/usr/bin/env bash
# Run by the shared release workflow before its test, version and release steps.
# The roundtrip package runs for several minutes under -race; raise the go test
# timeout above its 10-minute default for the steps that follow in the job.
set -euo pipefail
if [ -n "${GITHUB_ENV:-}" ]; then
  echo "GOFLAGS=-timeout=25m" >> "$GITHUB_ENV"
fi
