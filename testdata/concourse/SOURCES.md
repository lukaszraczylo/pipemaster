# Concourse fixtures

Real pipelines stored verbatim for the Concourse tests. No test run edits them.

| File | Repository | Commit | Path | SPDX licence | SHA-256 | Fetched |
|---|---|---|---|---|---|---|
| capi-ci-cve-pipeline.yml | <https://github.com/cloudfoundry/capi-ci> | `889f7ae99544ba51a1b2a60f758a51b01c5a377b` (HEAD of main) | `ci/cve-pipeline.yml` | Apache-2.0 | 5fdd02d49aee46e1e45ff8c96ce36edfbb481556d3a88968a46586c172f5cb9d | 2026-11-22 |

The licence text is the `LICENSE` file at the root of the same commit (Apache License 2.0, read before use). The file
holds only `((var))` references, no secret. It is research corpus row 8 (a minimal smoke case: git trigger,
`in_parallel`, `input_mapping`).
