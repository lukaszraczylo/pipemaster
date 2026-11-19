# Corpus sources

Real-world CI pipelines stored verbatim for `TestCorpus` (`internal/roundtrip/corpus_test.go`).
The directory of an entry is `testdata/corpus/<#>-<gha, gitea, forgejo or gitlab>-<slug>`.
Every file is byte-identical to the file at the pinned commit; the test run does not edit them.

- Fetch date: 2026-11-18 for entries 01 to 27 and 2026-11-19 for entries 28 to 40.
- Each repository is public, and its licence was read before use: from the host API (GitHub `license.spdx_id`, GitLab `license.key`), or, for entries 28 to 40,
  from the `LICENSE` file of the repository at the pinned commit.
  Every licence is MIT, Apache-2.0 or BSD-3-Clause.
- No private repository and no repository of the operator is used. No file holds a secret.
- Attribution: this file names the source repository, the commit, each path and the SPDX licence of every
  copied file, and the licence text sits at the repository root of that commit. This attribution satisfies the
  licence notice requirement of MIT, BSD-3-Clause and Apache-2.0 for these verbatim copies.
- Entries 10 (kustomize) and 14 (fastapi) replace pytest and vite, whose workflows use a `uses: $/...` form
  that the pinned GitHub schema and actionlint reject in the source files.
- Entry 22 pins the last commit that changed `.gitlab-ci.yml`: the file at that commit is byte-identical
  to the file fetched on the fetch date.
- Entry 23 (gitlab-org/labkit) is not in the corpus: its CI file holds a database URL with a user name and password, which the secret scanner of the commit hook flags, so the corpus does not copy it. The entry numbers do not shift.
- Entries 28 to 35 are `.gitea/workflows` files from gitea.com. Entries 36 to 40 are `.forgejo/workflows` files from codeberg.org.
  Every file comes from `<repository>/raw/commit/<commit>/<path>`, and holds only secret references.
- Entries 15 to 27 include the local files the root `.gitlab-ci.yml` references (`include: local:`), at the same commit.

| # | Slug | Provider | Repository | Commit | SPDX licence | Paths |
|---|---|---|---|---|---|---|
| 01 | cli | GHA | <https://github.com/cli/cli> | `acee710547dcf7dfb6490afea77972187e74b819` | MIT | `.github/workflows/acceptance.yml`<br>`.github/workflows/agentics-maintenance.yml`<br>`.github/workflows/bump-go.yml`<br>`.github/workflows/codeql.yml`<br>`.github/workflows/copilot-setup-steps.yml` |
| 02 | django | GHA | <https://github.com/django/django> | `9e2e5a31e2ef2e8673d3c2a38d2a391dbf42cf23` | BSD-3-Clause | `.github/workflows/benchmark.yml`<br>`.github/workflows/check-migrations.yml`<br>`.github/workflows/check_commit_messages.yml`<br>`.github/workflows/check_pr_limit.yml`<br>`.github/workflows/check_pr_quality.yml` |
| 03 | build-push-action | GHA | <https://github.com/docker/build-push-action> | `562e51947939164e4fe808a6eab5101977e509e0` | Apache-2.0 | `.github/workflows/.e2e-run.yml`<br>`.github/workflows/ci.yml`<br>`.github/workflows/codeql.yml`<br>`.github/workflows/e2e.yml`<br>`.github/workflows/pr-assign-author.yml` |
| 04 | gin | GHA | <https://github.com/gin-gonic/gin> | `0f09c3a9b4626d9fe9979cf4d0521d6ae32ab646` | MIT | `.github/workflows/codeql.yml`<br>`.github/workflows/gin.yml`<br>`.github/workflows/goreleaser.yml`<br>`.github/workflows/trivy-scan.yml` |
| 05 | helm | GHA | <https://github.com/helm/helm> | `01b541c8e4e15ac2f1fa4616234d1cb9883fc381` | Apache-2.0 | `.github/workflows/build-test.yml`<br>`.github/workflows/codeql-analysis.yml`<br>`.github/workflows/e2e-registries.yml`<br>`.github/workflows/e2e.yml`<br>`.github/workflows/golangci-lint.yml` |
| 06 | flask | GHA | <https://github.com/pallets/flask> | `49943b5a94413a530761350f04e896f76e8d620e` | BSD-3-Clause | `.github/workflows/lock.yaml`<br>`.github/workflows/pre-commit.yaml`<br>`.github/workflows/publish.yaml`<br>`.github/workflows/tests.yaml`<br>`.github/workflows/zizmor.yaml` |
| 07 | pandas | GHA | <https://github.com/pandas-dev/pandas> | `264b9d8f97b579a009f77fb171bf0c24d903604a` | BSD-3-Clause | `.github/workflows/cache-cleanup.yml`<br>`.github/workflows/code-checks.yml`<br>`.github/workflows/codeql.yml`<br>`.github/workflows/comment-commands.yml`<br>`.github/workflows/deprecation-tracking-bot.yml` |
| 08 | node_exporter | GHA | <https://github.com/prometheus/node_exporter> | `0488fdbdc3cb3cc3021850b00b093427a561073e` | Apache-2.0 | `.github/workflows/approve-workflows.yml`<br>`.github/workflows/bsd.yml`<br>`.github/workflows/ci.yml`<br>`.github/workflows/container_description.yml`<br>`.github/workflows/golangci-lint.yml` |
| 09 | black | GHA | <https://github.com/psf/black> | `c0e143e75443afd3be50c3b63ed2ee2fd108f509` | MIT | `.github/workflows/changelog.yml`<br>`.github/workflows/cut_release.yml`<br>`.github/workflows/diff_shades.yml`<br>`.github/workflows/diff_shades_comment.yml`<br>`.github/workflows/docker.yml` |
| 10 | kustomize | GHA | <https://github.com/kubernetes-sigs/kustomize> | `a3837055905a0c3a08ec517d502c92e8613e7730` | Apache-2.0 | `.github/workflows/apidiff.yml`<br>`.github/workflows/go.yml`<br>`.github/workflows/release.yaml`<br>`.github/workflows/test-install-kustomize.yml` |
| 11 | cobra | GHA | <https://github.com/spf13/cobra> | `adbc8813901bba65827259daa8e22ff94ec1f30e` | Apache-2.0 | `.github/workflows/labeler.yml`<br>`.github/workflows/test.yml` |
| 12 | tokio | GHA | <https://github.com/tokio-rs/tokio> | `fc0d151753b5fb10125ecef2d48f29b192c9abcf` | MIT | `.github/workflows/audit.yml`<br>`.github/workflows/ci.yml`<br>`.github/workflows/labeler.yml`<br>`.github/workflows/loom.yml`<br>`.github/workflows/pr-audit.yml` |
| 13 | traefik | GHA | <https://github.com/traefik/traefik> | `36b2bd1bd4538dec67b2e7ca6eb395eae47396ee` | MIT | `.github/workflows/build.yaml`<br>`.github/workflows/check_doc.yaml`<br>`.github/workflows/close-org-fork-pr.yaml`<br>`.github/workflows/codeql.yml`<br>`.github/workflows/dispatch.yaml` |
| 14 | fastapi | GHA | <https://github.com/fastapi/fastapi> | `05e6df7e906c3decba377c37548019973f40bf15` | MIT | `.github/workflows/add-to-project.yml`<br>`.github/workflows/build-docs.yml`<br>`.github/workflows/bump-pre-commit-hooks.yml`<br>`.github/workflows/create-draft-release.yml`<br>`.github/workflows/deploy-docs.yml` |
| 15 | charts-gitlab | GitLab | <https://gitlab.com/gitlab-org/charts/gitlab> | `9c6c8633ae3f4fa3a59457c0543e6d69c4cbb858` | MIT | `.gitlab-ci.yml`<br>`.gitlab/ci/checks.gitlab-ci.yml`<br>`.gitlab/ci/k3d-templates.gitlab-ci.yml`<br>`.gitlab/ci/k3d-version-pipeline-arm64.gitlab-ci.yml`<br>`.gitlab/ci/k3d-version-pipeline.gitlab-ci.yml`<br>`.gitlab/ci/kubeconform.gitlab-ci.yml`<br>`.gitlab/ci/operator.gitlab-ci.yml`<br>`.gitlab/ci/review-docs.gitlab-ci.yml`<br>`.gitlab/ci/rules.gitlab-ci.yml`<br>`.gitlab/ci/scanners.gitlab-ci.yml` |
| 16 | cli | GitLab | <https://gitlab.com/gitlab-org/cli> | `2b5d4bcb95432a7502d413b96ad78efa47e037f9` | MIT | `.gitlab-ci.yml`<br>`.gitlab/ci/review-docs.gitlab-ci.yml` |
| 17 | gitaly | GitLab | <https://gitlab.com/gitlab-org/gitaly> | `ca6ca9dbe8bc12efc4a0272131c450a680e5c28e` | MIT | `.gitlab-ci.yml` |
| 18 | gitlab-docs | GitLab | <https://gitlab.com/gitlab-org/gitlab-docs> | `12db688a47e929038daf99a2473ddce3bf26154a` | MIT | `.gitlab-ci.yml` |
| 19 | gitlab-pages | GitLab | <https://gitlab.com/gitlab-org/gitlab-pages> | `15c0fe6ac4ef55de694231e30be98302488d0dab` | MIT | `.gitlab-ci.yml`<br>`.gitlab/ci/prepare.yml`<br>`.gitlab/ci/test.yml` |
| 20 | gitlab-runner | GitLab | <https://gitlab.com/gitlab-org/gitlab-runner> | `d22d8753a6b96df6131d1d500decb4192d7f6f31` | MIT | `.gitlab-ci.yml`<br>`.gitlab/ci/_common.gitlab-ci.yml`<br>`.gitlab/ci/_kubernetes.gitlab-ci.yml`<br>`.gitlab/ci/_project_canonical.gitlab-ci.yml`<br>`.gitlab/ci/_project_fork.gitlab-ci.yml`<br>`.gitlab/ci/_rules.gitlab-ci.yml`<br>`.gitlab/ci/build.gitlab-ci.yml`<br>`.gitlab/ci/coverage.gitlab-ci.yml`<br>`.gitlab/ci/deploy.gitlab-ci.yml`<br>`.gitlab/ci/docs.gitlab-ci.yml`<br>`.gitlab/ci/hosted-runners-bridge.gitlab-ci.yml`<br>`.gitlab/ci/package.gitlab-ci.yml`<br>`.gitlab/ci/postrelease.gitlab-ci.yml`<br>`.gitlab/ci/qa.gitlab-ci.yml`<br>`.gitlab/ci/rebase.gitlab-ci.yml`<br>`.gitlab/ci/release.gitlab-ci.yml`<br>`.gitlab/ci/test-kubernetes-integration.gitlab-ci.yml`<br>`.gitlab/ci/test.gitlab-ci.yml` |
| 21 | gitlab-shell | GitLab | <https://gitlab.com/gitlab-org/gitlab-shell> | `a50f5a6158092f5eab82ef203979dc0e16558bd3` | MIT | `.gitlab-ci.yml` |
| 22 | gitlab-vscode-extension | GitLab | <https://gitlab.com/gitlab-org/gitlab-vscode-extension> | `af6563f0ea60d45e2eabb189dde5a759d03f5a10` | MIT | `.gitlab-ci.yml` |
| 24 | release-cli | GitLab | <https://gitlab.com/gitlab-org/release-cli> | `07d5e21c6610f781b65d7196c5d0e86e73bcc6be` | MIT | `.gitlab-ci.yml`<br>`.gitlab/ci/analyze-container.yml`<br>`.gitlab/ci/analyze.yml`<br>`.gitlab/ci/build.yml`<br>`.gitlab/ci/publish.yml`<br>`.gitlab/ci/release.yml`<br>`.gitlab/ci/test.yml` |
| 25 | semgrep | GitLab | <https://gitlab.com/gitlab-org/security-products/analyzers/semgrep> | `21b369a221298beab14a483fccbb700f545ab37e` | MIT | `.gitlab-ci.yml` |
| 26 | terraform-images | GitLab | <https://gitlab.com/gitlab-org/terraform-images> | `88f4ad0c22470d65bf56aadb10fce1ec7e383d33` | MIT | `.gitlab-ci.yml` |
| 27 | pipewire | GitLab | <https://gitlab.com/pipewire/pipewire> | `d329dac6ba7d32dacb13fd358b5360a59f7aac27` | MIT | `.gitlab-ci.yml` |
| 28 | docs | Gitea | <https://gitea.com/gitea/docs> | `5df4373bfe3d38e368d4475af8aaf2de22c8eccd` | Apache-2.0 | `.gitea/workflows/build-and-publish.yaml`<br>`.gitea/workflows/cleanup-preview.yaml`<br>`.gitea/workflows/test.yaml`<br>`.gitea/workflows/update_runner_reference.yaml`<br>`.gitea/workflows/update_swagger.yaml` |
| 29 | helm-actions | Gitea | <https://gitea.com/gitea/helm-actions> | `d0cf871b74ebad2ba42ba8849cea3a321375a4bb` | MIT | `.gitea/workflows/changelog.yml`<br>`.gitea/workflows/commitlint.yml`<br>`.gitea/workflows/release-version.yml`<br>`.gitea/workflows/shellcheck.yml`<br>`.gitea/workflows/test-pr.yml` |
| 30 | runner-images | Gitea | <https://gitea.com/gitea/runner-images> | `e5e94cda9983fdef6ce541b216d36be73240d8ab` | MIT | `.gitea/workflows/build.yaml`<br>`.gitea/workflows/release.yaml` |
| 31 | builder | Gitea | <https://gitea.com/xorm/builder> | `88db4b7a290e042ae5b3c08c9d094eca0b07584c` | BSD-3-Clause | `.gitea/workflows/test.yml` |
| 32 | terraform-provider-gitea | Gitea | <https://gitea.com/gitea/terraform-provider-gitea> | `760cbc8342fa5fa45202140b7d5f5f8dd33b4e0f` | MIT | `.gitea/workflows/sync-to-github.yml`<br>`.gitea/workflows/test.yml` |
| 33 | reverse | Gitea | <https://gitea.com/xorm/reverse> | `99cd7887d56885e298903983c5f666fb76c69ae1` | BSD-3-Clause | `.gitea/workflows/release-tag.yml`<br>`.gitea/workflows/test.yml` |
| 34 | gitea-vscode | Gitea | <https://gitea.com/gitea/gitea-vscode> | `aa9504ece2e1df93bb053619d89139578a8ca654` | MIT | `.gitea/workflows/ci.yml`<br>`.gitea/workflows/release.yml`<br>`.gitea/workflows/tag-release.yml` |
| 35 | actionslib | Gitea | <https://gitea.com/gitea/actionslib> | `a95558b443576745b1a22d2326722fef3534df9f` | MIT | `.gitea/workflows/pull-request.yml`<br>`.gitea/workflows/push.yml`<br>`.gitea/workflows/tag.yml` |
| 36 | renovate-config | Forgejo | <https://codeberg.org/forgejo/renovate-config> | `4c322366ea37510f4f1012323cfec0cfec247ce1` | MIT | `.forgejo/workflows/build.yml` |
| 37 | helm | Forgejo | <https://codeberg.org/forgejo-contrib/forgejo-helm> | `91fcfc2735b2919506179e4b1aff90bb00768251` | MIT | `.forgejo/workflows/build.yml`<br>`.forgejo/workflows/mirror.yml` |
| 38 | deb | Forgejo | <https://codeberg.org/forgejo-contrib/forgejo-deb> | `0151f06943eaaec1909d637b2b237df1c95d95d5` | MIT | `.forgejo/workflows/forgejo-deb.yml`<br>`.forgejo/workflows/mirror.yml` |
| 39 | renovate | Forgejo | <https://codeberg.org/forgejo-contrib/forgejo-renovate> | `0ef5e88be10c29a764c5203f5c2593f6cd5c2998` | MIT | `.forgejo/workflows/build.yml`<br>`.forgejo/workflows/mirror.yml`<br>`.forgejo/workflows/renovate-cb.yml`<br>`.forgejo/workflows/renovate.yml` |
| 40 | contrib-renovate-config | Forgejo | <https://codeberg.org/forgejo-contrib/renovate-config> | `79cee37e7360ec5cbadb65afc5d83605dc9708e4` | MIT | `.forgejo/workflows/build.yml` |
