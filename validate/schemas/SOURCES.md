# Vendored schemas

Fetched 2026-11-18. Both are JSON Schema draft-07 and have no external `$ref`
(checked with grep: every `$ref` starts with `#`), so nothing else is vendored.

| File | URL | Bytes | SHA-256 | Licence |
|---|---|---|---|---|
| github-workflow.json | https://www.schemastore.org/github-workflow.json | 118778 | 0528a890c1a6f4209444c421dbb752888a934a540f2a677146d78ed7f2321cbf | Apache-2.0 (SchemaStore repo LICENSE, verified) |
| gitlab-ci.json | https://gitlab.com/gitlab-org/gitlab/-/raw/master/app/assets/javascripts/editor/schema/ci.json | 129729 | ee4677b5c6f5e15e4379f206c06c8405be433d6297d36f45e13e7ea39583f524 | MIT (gitlab repo LICENSE: non-ee/ content is MIT, verified) |

The gitlab-foss URL from the brief was not used; the gitlab-org/gitlab path
above resolved first try.
