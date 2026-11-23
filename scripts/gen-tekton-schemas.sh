#!/usr/bin/env bash
# Regenerates validate/schemas/tekton/*.json.gz from the pinned Tekton CRDs. It is a recipe, not part of `make check`:
# the generated files are committed, and a pin change here must come with a SOURCES.md update.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/validate/schemas/tekton"

pipelines_tag="v1.18.0"
pipelines_sha="d55f53a622c2bd82e89ce7c1b6cd59d52b24849c"
triggers_tag="v0.38.0"
triggers_sha="f5277a41e76b17e68fc6785da0dec4377b9b62aa"
kubeconform_tag="v0.8.0"
# sha256 of scripts/openapi2jsonschema.py at the kubeconform tag.
converter_sha256="d145babfbb765004030764e1b4e518bfb7a4bd7f111691a08fa57983b81881f3"
k8s_sha="88b0235585da4efb0e4bc6cc00baccf0e327e54d"
cronjob_sha256="fb921edacc3ded080ca322233197727f8c110254ba3514e6faade5fa06513ae4"
raw="https://raw.githubusercontent.com"

# A moved or replaced tag fails the run instead of generating schemas from other CRDs.
check_tag() {
	local repo="$1" tag="$2" want="$3" got
	got="$(git ls-remote "https://github.com/$repo" "refs/tags/$tag" | cut -f1)"
	if [[ "$got" != "$want" ]]; then
		echo "$repo $tag is at $got, expected $want" >&2
		exit 1
	fi
}
check_tag tektoncd/pipeline "$pipelines_tag" "$pipelines_sha"
check_tag tektoncd/triggers "$triggers_tag" "$triggers_sha"

# Each download lands in its own empty directory, and Python runs isolated (-I) so it loads no code from them.
work="$(mktemp -d "${TMPDIR:-/tmp}/gen-tekton.XXXXXX")"
trap 'rm -rf "$work"' EXIT
mkdir "$work/crds" "$work/conv" "$work/gen"

for c in pipeline task stepaction pipelinerun taskrun; do
	curl -fsSL -o "$work/crds/300-$c.yaml" "$raw/tektoncd/pipeline/$pipelines_tag/config/300-crds/300-$c.yaml"
done
for c in eventlistener triggerbinding triggertemplate trigger clusterinterceptor clustertriggerbinding; do
	curl -fsSL -o "$work/crds/300-$c.yaml" "$raw/tektoncd/triggers/$triggers_tag/config/300-$c.yaml"
done
curl -fsSL -o "$work/conv/openapi2jsonschema.py" "$raw/yannh/kubeconform/$kubeconform_tag/scripts/openapi2jsonschema.py"

got="$( (sha256sum "$work/conv/openapi2jsonschema.py" 2>/dev/null || shasum -a 256 "$work/conv/openapi2jsonschema.py") | cut -d' ' -f1)"
if [[ "$got" != "$converter_sha256" ]]; then
	echo "openapi2jsonschema.py has sha256 $got, expected $converter_sha256" >&2
	exit 1
fi

(cd "$work/gen" && FILENAME_FORMAT='{kind}_{version}' python3 -I "$work/conv/openapi2jsonschema.py" "$work"/crds/300-*.yaml)

mkdir -p "$out"
rm -f "$out"/*.json "$out"/*.json.gz
# The converter puts a boolean additionalProperties key into the properties map of any node that declares a field
# named "properties" (ParamSpec.properties). kubeconform then reports the schema as missing. The fix drops that key.
python3 -I - "$work/gen" "$out" <<'PY'
import json, os, sys

src, dst = sys.argv[1], sys.argv[2]
groups = {"tekton.dev": {"pipeline", "task", "stepaction", "pipelinerun", "taskrun"},
          "triggers.tekton.dev": {"eventlistener", "triggerbinding", "triggertemplate", "trigger",
                                  "clusterinterceptor", "clustertriggerbinding"}}

def fix(x):
    if isinstance(x, dict):
        pr = x.get("properties")
        if isinstance(pr, dict) and isinstance(pr.get("additionalProperties"), bool):
            del pr["additionalProperties"]
        for v in x.values():
            fix(v)
    elif isinstance(x, list):
        for v in x:
            fix(v)

base = "https://pipemaster.invalid/schemas/tekton/"
rules = []
for name in sorted(os.listdir(src)):
    if not name.endswith(".json"):
        continue
    kind, version = name[:-5].split("_")
    group = next(g for g, kinds in groups.items() if kind in kinds)
    with open(os.path.join(src, name)) as f:
        doc = json.load(f)
    fix(doc)
    with open(os.path.join(dst, name), "w") as f:
        json.dump(doc, f, indent=2)
        f.write("\n")
    rules.append((group + "/" + version, kind, name))

kinds = {"pipeline": "Pipeline", "task": "Task", "stepaction": "StepAction", "pipelinerun": "PipelineRun",
         "taskrun": "TaskRun", "eventlistener": "EventListener", "triggerbinding": "TriggerBinding",
         "triggertemplate": "TriggerTemplate", "trigger": "Trigger", "clusterinterceptor": "ClusterInterceptor",
         "clustertriggerbinding": "ClusterTriggerBinding"}
rules = [(av, kinds[k], n) for av, k, n in rules]
rules.append(("batch/v1", "CronJob", "cronjob-batch-v1.json"))

def rule(av, kind, name):
    return {"if": {"properties": {"apiVersion": {"const": av}, "kind": {"const": kind}},
                   "required": ["apiVersion", "kind"]},
            "then": {"$ref": base + name}}

dispatch = {"$schema": "https://json-schema.org/draft/2020-12/schema", "$id": base + "dispatch.json",
            "allOf": [rule(*r) for r in sorted(rules)]}
with open(os.path.join(dst, "dispatch.json"), "w") as f:
    json.dump(dispatch, f, indent=2)
    f.write("\n")
PY
# The standalone-strict variant has no external $ref; it is the batch/v1 CronJob the CRD set lacks.
curl -fsSL -o "$out/cronjob-batch-v1.json" "$raw/yannh/kubernetes-json-schema/$k8s_sha/v1.34.12-standalone-strict/cronjob-batch-v1.json"
got="$( (sha256sum "$out/cronjob-batch-v1.json" 2>/dev/null || shasum -a 256 "$out/cronjob-batch-v1.json") | cut -d' ' -f1)"
if [[ "$got" != "$cronjob_sha256" ]]; then
	echo "cronjob-batch-v1.json has sha256 $got, expected $cronjob_sha256" >&2
	exit 1
fi
# Deterministic: -n drops the name and mtime. The sha256 pins above are of the plain files.
for f in "$out"/*.json; do
	gzip -9n -f "$f"
done
echo "wrote $out"
