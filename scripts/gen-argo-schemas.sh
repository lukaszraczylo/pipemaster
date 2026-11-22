#!/usr/bin/env bash
# Regenerates validate/schemas/argo/ from the pinned datree CRDs-catalog files. It is a recipe, not part of
# `make check`: the files are committed, and a pin change here must come with a SOURCES.md update.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/validate/schemas/argo"
sha="784ec895547a36e548e6810b41f27baade3a0890"
raw="https://raw.githubusercontent.com/datreeio/CRDs-catalog/$sha/argoproj.io"

# bash 3.2 on macOS has no associative arrays.
want_for() {
	case "$1" in
	workflow) echo "6dc457258ac10c9bb8c9903ac9e566c919b99feaa0d13bb91f085fd07f5ee231" ;;
	workflowtemplate) echo "bc9b2c61354fac39039165024d5152b52fb23cadc657c60c298848466bdfb68d" ;;
	clusterworkflowtemplate) echo "d65a2a2bc08500f136821f031df8b2f4bafd35c1c7105c0a15df86ea7b14bdf6" ;;
	cronworkflow) echo "90e36f74bfcb5862081203ec02ef7e191928aea50ea4af529f004ea0eb4293ed" ;;
	sensor | eventsource) echo "226fc910586c36fb7cea4fcdbd5ac60e3715d0fdc849edbe5875232b588df354" ;;
	esac
}

mkdir -p "$out"
rm -f "$out"/*.json
for k in workflow workflowtemplate clusterworkflowtemplate cronworkflow sensor eventsource; do
	f="${k}_v1alpha1.json"
	curl -fsSL -o "$out/$f" "$raw/$f"
	got="$( (sha256sum "$out/$f" 2>/dev/null || shasum -a 256 "$out/$f") | cut -d' ' -f1)"
	if [[ "$got" != "$(want_for "$k")" ]]; then
		echo "$f has sha256 $got, expected $(want_for "$k")" >&2
		exit 1
	fi
done

python3 -I - "$out" <<'PY'
import json, os, sys

dst = sys.argv[1]
base = "https://pipemaster.invalid/schemas/argo/"
kinds = [("Workflow", "workflow"), ("WorkflowTemplate", "workflowtemplate"),
         ("ClusterWorkflowTemplate", "clusterworkflowtemplate"), ("CronWorkflow", "cronworkflow"),
         ("Sensor", "sensor"), ("EventSource", "eventsource")]

def rule(kind, file):
    return {"if": {"properties": {"apiVersion": {"const": "argoproj.io/v1alpha1"}, "kind": {"const": kind}},
                   "required": ["apiVersion", "kind"]},
            "then": {"$ref": base + file + "_v1alpha1.json"}}

dispatch = {"$schema": "https://json-schema.org/draft/2020-12/schema", "$id": base + "dispatch.json",
            "allOf": [rule(k, f) for k, f in sorted(kinds)]}
with open(os.path.join(dst, "dispatch.json"), "w") as f:
    json.dump(dispatch, f, indent=2)
    f.write("\n")
PY
echo "wrote $out"
