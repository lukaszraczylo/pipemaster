# Corpus sources

Real-world CI pipelines stored verbatim for `TestCorpus` (`internal/roundtrip/corpus_test.go`).
The directory of an entry is `testdata/corpus/<#>-<gha, gitea, forgejo, gitlab, woodpecker, drone, tekton or argo>-<slug>`.
Every file is byte-identical to the file at the pinned commit; the test run does not edit them.

- Fetch date: 2026-11-18 for entries 01 to 27, 2026-11-19 for entries 28 to 40 and 2026-11-20 for entries 41 to 50 and 2026-11-21 for entries 51 to 71 and 2026-11-22 for entries 72 to 75.
- Each repository is public, and its licence was read before use: from the host API (GitHub `license.spdx_id`, GitLab `license.key`), or, for entries 28 to 40,
  from the `LICENSE` file of the repository at the pinned commit.
  Every licence is MIT, Apache-2.0, BSD-3-Clause or, for entry 60 only, MPL-2.0. Entries 63 to 71 are Apache-2.0. Entry 72 is BSD-3-Clause, and entries 73 to 75 are Apache-2.0.
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
| 41 | woodpecker | Woodpecker | <https://github.com/woodpecker-ci/woodpecker> | `f1f38e319804d5b96369e7c772a45d7d70dc5c55` | Apache-2.0 | `.woodpecker/test.yaml`<br>`.woodpecker/docker.yaml`<br>`.woodpecker/securityscan.yaml` |
| 42 | plugin-git | Woodpecker | <https://github.com/woodpecker-ci/plugin-git> | `1097dc26ddd07ad42f3b345596da693ef44bfa4f` | Apache-2.0 | `.woodpecker/test-release.yml` |
| 43 | catala | Woodpecker | <https://github.com/CatalaLang/catala> | `7a0c93beda25b903ebcdfe64d672caf53dd41370` | Apache-2.0 | `.woodpecker/main.yaml` |
| 44 | monitor | Woodpecker | <https://github.com/ntppool/monitor> | `e01e6f0461f818b66e376790bd8c2882e1c78668` | MIT | `.woodpecker/publish-production.yaml` |
| 46 | go-starter | Woodpecker | <https://github.com/allaboutapps/go-starter> | `39baa1b429b67096847200324fce542243e1a6e9` | MIT | `.woodpecker/ci.yaml` |
| 47 | core | Woodpecker | <https://github.com/azugo/core> | `46034f8b4bbe51512a3fb27abf3939974c1a868c` | MIT | `.woodpecker/test.yaml`<br>`.woodpecker/integration.yaml` |
| 48 | aiocoap | Woodpecker | <https://github.com/chrysn/aiocoap> | `f867dc482ec4bfd7246924a7f4babf30b0193e79` | MIT | `.woodpecker/all.yaml` |
| 49 | contributions-web | Woodpecker | <https://github.com/jordanopensource/contributions-web> | `5c4d44955d335d309777234d10097f31db583f5c` | Apache-2.0 | `.woodpecker/build-latest-image.yaml` |
| 50 | rust-confirm-email | Woodpecker | <https://github.com/FrostWalk/rust-confirm-email> | `8dccde4410b4339c47ca92c590f5a2019d2fce96` | MIT | `.woodpecker/pr.yaml` |
| 51 | Parsr | Drone | <https://github.com/axa-group/Parsr> | `4462467727409c4f2f4d186edc8ae4844c354ee8` | Apache-2.0 | `.drone.yml` |
| 52 | distribution | Drone | <https://github.com/sighupio/distribution> | `a147b22ce87b2cbff469d199039b774001cf93ef` | BSD-3-Clause | `.drone.yml` |
| 53 | dictutil | Drone | <https://github.com/pgaskin/dictutil> | `84f28bca4bdb452b86f01d5adf753d583d9dcd70` | MIT | `.drone.yml` |
| 54 | go-away | Drone | <https://github.com/WeebDataHoarder/go-away> | `bddbe80d62ce12d099a800a344a27cdb04bbffdb` | MIT | `.drone.yml` |
| 55 | geodns | Drone | <https://github.com/abh/geodns> | `ed37e52b329a37fc5f7fa1ff3a28adf32b3b42be` | Apache-2.0 | `.drone.yml` |
| 56 | mysql-operator | Drone | <https://github.com/bitpoke/mysql-operator> | `37c64109855691ab227130cf2d011effdd48690b` | Apache-2.0 | `.drone.yml` |
| 58 | go-admin | Drone | <https://github.com/GoAdminGroup/go-admin> | `c47763c7bb630a742edf33c01aa7010f7a37a33d` | Apache-2.0 | `.drone.yml` |
| 59 | casper-node | Drone | <https://github.com/casper-network/casper-node> | `02925627bc2ac5ab477c3e5c31d7b20906198adc` | Apache-2.0 | `.drone.yml` |
| 60 | go-markdown2confluence | Drone | <https://github.com/justmiles/go-markdown2confluence> | `812f9ef8812f5552f31d5fb46b71cf7f7e582219` | MPL-2.0 | `.drone.yml` |
| 61 | pinmonl | Drone | <https://github.com/pinmonl/pinmonl> | `2ca880f1079a8d554df0c9762c378e0f3ea947f1` | MIT | `.drone.yml` |
| 62 | elasticsearch-test-data | Drone | <https://github.com/oliver006/elasticsearch-test-data> | `e37c5d5b26b98d4000d89da38a1cb118014cd098` | MIT | `.drone.yml` |
| 63 | pipeline | Tekton | <https://github.com/tektoncd/pipeline> | `3fe337ff1878f5037e2a4d5be288080306d617a7` | Apache-2.0 | `examples/v1/pipelineruns/pipelinerun-with-final-tasks.yaml`<br>`examples/v1/pipelineruns/pipelinerun-with-when-expressions.yaml`<br>`examples/v1/pipelineruns/beta/pipelinerun-with-matrix-include.yaml`<br>`examples/v1/pipelineruns/alpha/pipelinerun-with-cel-when-expressions.yaml`<br>`examples/v1/pipelineruns/optional-workspaces.yaml`<br>`examples/v1/pipelineruns/no-ci/git-resolver.yaml` |
| 64 | triggers | Tekton | <https://github.com/tektoncd/triggers> | `9e21fc4bdea9e6defe7027242d1618cb6abd7f6d` | Apache-2.0 | `examples/v1beta1/cron/cronjob.yaml`<br>`examples/v1beta1/cron/eventlistener.yaml`<br>`examples/v1beta1/github-add-changed-files-push-cel/02-github-eventlistener-interceptor.yaml` |
| 65 | plumbing | Tekton | <https://github.com/tektoncd/plumbing> | `b24502d4f511ddeb6eb390689a21d0813bb17610` | Apache-2.0 | `tekton/catalog/pipelines/release.yaml`<br>`tekton/catalog/tasks/publish.yaml` |
| 66 | results | Tekton | <https://github.com/tektoncd/results> | `9d8626118d5582ae95b28107afbdd5b6ba551c9c` | Apache-2.0 | `tekton/ci.yaml`<br>`tekton/results-release-pipeline.yaml` |
| 67 | chains | Tekton | <https://github.com/tektoncd/chains> | `afaaeac09fa09245a2f77bab556f0f6370db60f7` | Apache-2.0 | `release/release-pipeline.yaml` |
| 68 | build-definitions | Tekton | <https://github.com/konflux-ci/build-definitions> | `8fed8ffff0ff01a8bd8d5ca2049e538c642f2d05` | Apache-2.0 | `.tekton/tasks/buildah.yaml`<br>`pipelines/enterprise-contract.yaml` |
| 69 | pipelines-tutorial | Tekton | <https://github.com/openshift/pipelines-tutorial> | `4ebfb3d6f4e195de106f0f59a2506f4edffdfcf7` | Apache-2.0 | `01_pipeline/04_pipeline.yaml`<br>`01_pipeline/01_apply_manifest_task.yaml`<br>`.tekton/pipelinerun.yaml` |
| 70 | git-clone | Tekton | <https://github.com/tektoncd-catalog/git-clone> | `e1b1fafc3a0c1704a69beb894d65a8b3e090d5d8` | Apache-2.0 | `task/git-clone/git-clone.yaml`<br>`stepaction/git-clone/git-clone.yaml` |
| 71 | catalog | Tekton | <https://github.com/tektoncd/catalog> | `112374ce1262b133a77300bd8133445722605e04` | Apache-2.0 | `task/git-clone/0.10/git-clone.yaml` |
| 72 | ci-example | Argo | <https://github.com/pipekit/argo-workflows-ci-example> | `4f0e92c4a90d788ba22ca617dae52a547d869b4b` | BSD-3-Clause | `bootstrap/workflow-templates/nfs/ci-workflow.yml` |
| 73 | go-gin-rest-api | Argo | <https://github.com/linclin/go-gin-rest-api> | `8f326d8d6789fa8d5b486e7586ba8911b9da63a6` | Apache-2.0 | `wiki/ci/buildkit-argo-workflow-template.yaml` |
| 74 | workflows-examples | Argo | <https://github.com/argoproj/argo-workflows> | `43021573c5a35cbafae009043c132dfcef43a112` | Apache-2.0 | `examples/ci.yaml`<br>`examples/conditionals.yaml`<br>`examples/dag-diamond.yaml`<br>`examples/dag-enhanced-depends.yaml`<br>`examples/exit-handler-dag-level.yaml` |
| 75 | events-examples | Argo | <https://github.com/argoproj/argo-events> | `d13fde07d944c081c25574305911908c80faa98e` | Apache-2.0 | `examples/event-sources/github.yaml`<br>`examples/sensors/github.yaml`<br>`examples/dag-diamond.yaml` (from argo-workflows at the commit of entry 74) |

Woodpecker entries (41 to 50):

- The `.woodpecker/` files of the research list (section 10.1) are copied byte for byte. The linter runs with
  `--plugins-privileged woodpeckerci/plugin-docker-buildx` and `--plugins-privileged plugins/docker`. Entries 41, 42 and 49 need the first; the converted Drone entries (51 to 62) need the second.
- Gap: `.woodpecker/default.yaml` of ntppool/monitor (entry 44) is not stored. It holds a database connection string
  that trufflehog reports as a Postgres credential at line 43.
- Gap: entry 45 (abh/ntppool, `.woodpecker/e2e.yaml`) is not stored for the same reason, at line 67.
- Entry 48 is MIT per its `SPDX-License-Identifier` header and `LICENSES/MIT.txt`.

Drone entries (51 to 62):

- The `.drone.yml` files of the research list (section 10.1) are copied byte for byte from the pinned commit. The raw URL names the commit, not the branch.
- Gap: entry 57 (webkom/lego-webapp) is not stored. Its `.drone.yml` holds a Sentry URL with a user name and password at line 92, which trufflehog flags.
- Gap: drone/drone-kaniko is not stored. Its pipelines use `type: vm`, which is not a docker-runner shape.
- Entries 54, 55 hold a `kind: signature` document. Its `hmac` value is a hash of the file, not a secret.
- Entry 60 is MPL-2.0, per the GitHub API `license.spdx_id`.

Tekton entries (63 to 71):

- The files are the non-PaC rows of section 10 of `docs/superpowers/research/tekton.md`, copied byte for byte from the pinned commit. The raw URL is `https://raw.githubusercontent.com/<repository>/<commit>/<path>`.
- Rows 14, 16, 17, 18 and 21 of that section are PaC `.tekton/` PipelineRuns, which belong to a later phase. They are not stored.
- Entries 63 to 69 keep the file name of the source path, in `.tekton/`. Entry 70 keeps the subdirectory `task/` or `stepaction/` of its two sources.
- Entry 71 stores the file as `.tekton/git-clone.yaml`.
- Entries 64, 70 and 71 hold a second file, `.tekton/pipelinerun-with-matrix-include.yaml`, from `tektoncd/pipeline` at the commit of entry 63 (path `examples/v1/pipelineruns/beta/pipelinerun-with-matrix-include.yaml`). The entries hold no Pipeline of their own, so a Tekton project needs it to parse.
- Entry 71 is a `tekton.dev/deprecated` catalog Task. Entry 63 holds `cel` when entries, which the parser keeps as opaque text.

Argo entries (72 to 75):

- The raw URL is `https://raw.githubusercontent.com/<repository>/<commit>/<path>`. Every file is byte-identical to the file at the pinned commit. Licences are the GitHub API `license.spdx_id` of each repository.
- Entry 74 pins the tag v4.1.6 of argoproj/argo-workflows, which is the commit in the table. Entry 75 pins the tag v1.9.11 of argoproj/argo-events, an annotated tag that peels to the commit in the table.
- Entries 72 to 74 keep the file name of the source path, in `.argo/`. Entry 75 keeps the subdirectories `event-sources/` and `sensors/`, and holds `.argo/dag-diamond.yaml` from entry 74, so that a Workflow exists for the Sensor to run.
- Not stored, because the pinned file does not parse or does not round trip: `ci-workflow-s3.yml` of pipekit (a `parameters` key under `spec`, which the Argo schema rejects), `git-checkout-pr.yml` of pipekit (its tags read workflow parameters that no file declares), `cron-workflow.yaml` of argo-workflows (the schedule is every minute, which the GHA linter rejects), and the files of konstructio/gitops-template, yoonjk/argocd-handson, hortonew/kind-argo-lab and smiggiddy/argo. Those hold a template library with no entrypoint, a ClusterWorkflowTemplate whose entrypoint names no template, Helm template text, or a command list with an input tag. The entry numbers are not reused.
