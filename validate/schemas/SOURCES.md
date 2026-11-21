# Vendored schemas

Fetched 2026-11-18. Both are JSON Schema draft-07 and have no external `$ref`
(checked with grep: every `$ref` starts with `#`), so nothing else is vendored.

| File | URL | Bytes | SHA-256 | Licence |
|---|---|---|---|---|
| github-workflow.json | https://www.schemastore.org/github-workflow.json | 118778 | 0528a890c1a6f4209444c421dbb752888a934a540f2a677146d78ed7f2321cbf | Apache-2.0 (SchemaStore repo LICENSE, verified) |
| gitlab-ci.json | https://gitlab.com/gitlab-org/gitlab/-/raw/master/app/assets/javascripts/editor/schema/ci.json | 129729 | ee4677b5c6f5e15e4379f206c06c8405be433d6297d36f45e13e7ea39583f524 | MIT (gitlab repo LICENSE: non-ee/ content is MIT, verified) |

The gitlab-foss URL from the brief was not used; the gitlab-org/gitlab path
above resolved first try.
| woodpecker.json | https://raw.githubusercontent.com/woodpecker-ci/woodpecker/v3.20.1/pipeline/frontend/yaml/linter/schema/schema.json | 38991 | b448916fea06650f833a355496b33a5672213d6801ca9e3def60bf198c1a492e | Apache-2.0 (woodpecker-ci/woodpecker repo LICENSE), tag v3.20.1, fetched 2026-11-20, draft-07, no external `$ref` |
| drone.json | https://raw.githubusercontent.com/SchemaStore/schemastore/ef1dcef4af190a099e973571b13125fb3e2a40ea/src/schemas/json/drone.json | 22069 | 5f25f36d7b1c75507bbfbe13f78a96b31610fc8a10dd6f0bfe07eca5f721aef7 | Apache-2.0 (SchemaStore repo LICENSE at the same commit), SchemaStore commit ef1dcef4 (2026-04-29, last change of the file), fetched 2026-11-21, draft-07; its only external `$ref`s point to kubernetes-definitions.json; PATCHED: `step_docker` also allows `working_dir` (string) and `entrypoint` (string list), because drone-runner-docker reads both and `drone lint --trusted` accepts them |
| kubernetes-definitions.json | https://raw.githubusercontent.com/SchemaStore/schemastore/ef1dcef4af190a099e973571b13125fb3e2a40ea/src/schemas/json/kubernetes-definitions.json | 991968 | e12ff558a488df7fb521815e41c01b8e1e9bf740fe7dd77bce38ae546fc460a1 | Apache-2.0 (SchemaStore repo LICENSE at the same commit), same commit, fetched 2026-11-21, draft-07, no external `$ref`; registered under https://json.schemastore.org/kubernetes-definitions.json so the offline loader resolves drone.json |
