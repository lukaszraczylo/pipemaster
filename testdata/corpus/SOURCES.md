# Corpus sources

Real-world CI pipelines stored verbatim for `TestCorpus` (`internal/roundtrip/corpus_test.go`).
The directory of an entry is `testdata/corpus/<#>-<gha, gitea, forgejo, gitlab, woodpecker, drone, tekton, argo or concourse>-<slug>`.
Every file is byte-identical to the file at the pinned commit; the test run does not edit them.

- Fetch date: 2026-11-18 for entries 01 to 27, 2026-11-19 for entries 28 to 40 and 2026-11-20 for entries 41 to 50 and 2026-11-21 for entries 51 to 71 and 2026-11-22 for entries 72 to 79 and 2026-11-23 for entries 81 to 86 and 2026-11-24 for entry 87.
- Each repository is public, and its licence was read before use: from the host API (GitHub `license.spdx_id`, GitLab `license.key`), or, for entries 28 to 40,
  from the `LICENSE` file of the repository at the pinned commit.
  Every licence is MIT, Apache-2.0, BSD-3-Clause or, for entry 60 only, MPL-2.0. Entries 63 to 71 are Apache-2.0. Entry 72 is BSD-3-Clause, entry 73, 74 and 75 are Apache-2.0, entries 76 and 78 are MIT, entry 77 is Apache-2.0 and entry 79 is BSD-3-Clause.
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
| 76 | gitops-template | Argo | <https://github.com/konstructio/gitops-template> | `2418943af0047e6bc9ad1f99e5beb4e42e0a84d9` | MIT | `akamai-github/templates/mgmt/components/argo-workflows/cwfts/cwft-kaniko.yaml`<br>`akamai-github/templates/mgmt/components/argo-workflows/cwfts/cwft-git.yaml` |
| 77 | argocd-handson | Argo | <https://github.com/yoonjk/argocd-handson> | `13be905bf5b102d4f0d9d6e0a9e8894cee7411af` | Apache-2.0 | `lab07/01/ci-workflowtemplate-buildah-commit.yaml`<br>`lab07/01/ci-sensor.yaml`<br>`lab05/02/sensor.yaml`<br>`lab05/02/eventsource.yaml` |
| 78 | kind-argo-lab | Argo | <https://github.com/hortonew/kind-argo-lab> | `01b11d9c8a79c10f2e57ee78005fb8442608a9d1` | MIT | `charts/argo-workflows/templates/kaniko-build-workflowtemplate.yaml`<br>`charts/argo-events/templates/sensor-workflow-trigger.yaml`<br>`charts/argo-events/templates/eventsource-gitea.yaml` |
| 79 | smiggiddy-argo | Argo | <https://github.com/smiggiddy/argo> | `86f4924023551cab7571943e74477873af7fd43c` | BSD-3-Clause | `argo-workflows/templates/buildkit.yaml`<br>`argo-workflows/build-excalidraw.yaml` |
| 80 | examples | Concourse | <https://github.com/concourse/examples> | `a3d23a6a73977baa2d4a8dbd24a3048f4f98b131` | Apache-2.0 | `pipelines/multi-branch/parent.yml` (stored as `.concourse/parent.yml`) |
| 81 | bosh | Concourse | <https://github.com/cloudfoundry/bosh> | `404e4581039fe17c25123e11f0da99cd07769908` | Apache-2.0 | `ci/pipeline.yml` (stored as `.concourse/pipeline.yml`) |
| 82 | bosh-backup-and-restore | Concourse | <https://github.com/cloudfoundry/bosh-backup-and-restore> | `09ea1752b351175b08bf86ccff9e436d1af92e91` | Apache-2.0 | `ci/pipeline.yml` (stored as `.concourse/pipeline.yml`) |
| 83 | 7f-ephemeral-pipelines | Concourse | <https://github.com/7Factor/7f-ephemeral-pipelines> | `0cd32f821c464bb10651fe5c0d8f74b2a1d3f4ba` | MIT | `ci/pipelines/pipeline.yml` (stored as `.concourse/pipeline.yml`) |
| 84 | setup-runtime-resource | Concourse | <https://github.com/SoluReal/setup-runtime-resource> | `30302fcc420a45abbd1488489087b116527ecd73` | MIT | `ci/pipeline.yml` (stored as `.concourse/pipeline.yml`) |
| 85 | cli-ci | Concourse | <https://github.com/cloudfoundry/cli-ci> | `7f253f00b18907571ab18f16476d5431f8ddea3a` | Apache-2.0 | `ci/cli-release/pipeline.yml` (stored as `.concourse/cli-release.yml`)<br>`ci/cli-v8-toolsmiths/pipeline.yml` (stored as `.concourse/cli-v8-toolsmiths.yml`) |
| 86 | capi-ci | Concourse | <https://github.com/cloudfoundry/capi-ci> | `889f7ae99544ba51a1b2a60f758a51b01c5a377b` | Apache-2.0 | `ci/cve-pipeline.yml` (stored as `.concourse/cve-pipeline.yml`) |
| 87 | examples (with task files) | Concourse | <https://github.com/concourse/examples> | `a3d23a6a73977baa2d4a8dbd24a3048f4f98b131` | Apache-2.0 | `pipelines/multi-branch/parent.yml` (stored as `.concourse/parent.yml`)<br>`tasks/go-test.yml`<br>`tasks/go-build.yml`<br>`apps/golang/binary-test.yml` |

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
- Entries 76 to 79 are rows 2, 4, 5 and 6 of section 8 of `docs/superpowers/research/argo.md`, copied byte for byte from the pinned commit into `.argo/` with the file name of the source path. Entry 76 holds two ClusterWorkflowTemplates and no workflow, entry 77 holds a template with no entrypoint, a Sensor and a webhook EventSource, and entry 79 holds a Workflow that only names a WorkflowTemplate. Those shapes parse into a pipeline with no job, with the documents kept in Ext.
- Not stored, because the pinned file does not parse or does not round trip: `ci-workflow-s3.yml` of pipekit (a `parameters` key under `spec`, which the Argo schema rejects), `git-checkout-pr.yml` of pipekit (its tags read workflow parameters that no file declares), and `cron-workflow.yaml` of argo-workflows (the schedule is every minute, which the GHA linter rejects). The Helm-templated files of the research list are not stored. The entry numbers are not reused.

Concourse entries (80 to 86):

- Rows 1, 2, 3, 5, 6, 7 and 8 of section 8 of `docs/superpowers/research/concourse.md`. The raw URL is `https://raw.githubusercontent.com/<repository>/<commit>/<path>`. Every file is byte-identical to the file at the pinned commit, and `fly validate-pipeline --strict` (fly 8.3.3) accepts each one except entries 80 and 82, which the next bullet explains.
- The licence text was read at the pinned commit before use: Apache License 2.0 (`LICENSE`, and `LICENCE` for entry 82) for the Apache-2.0 entries, and the MIT text for entries 83 and 84. Each file holds only `((var))` references.
- SHA-256: entry 80 `952697266151e6c550fd13fd1cc5bbb45f01a4095811c54e2f586949f4ad6f1f`, 81 `5939f6ff52fd71282ca78d0ec09c57f35a028dba2e56ed717c5c91bdaaf6bc4a`, 82 `e2a593e1bb24b4434b78cf11ef4c1fa057ff7c47f4e277da810d32fb13440169`, 83 `8bcf00edf536b106492e63ef7658cb833cee470d11efdcef22dae94b699f3e1d`, 84 `2466e172c8a4b508a702b78a72f6464a6cc6b6365ae1010b4688ab78633bcc79`, 85 `cli-release.yml` `0aab42bcaf4bee6e5974a96e68874d1cb03310df6be428416166e32bf2162324`, 85 `cli-v8-toolsmiths.yml` `d696bcf16dd394d92e36c7ba59b874768d8218bd08179fa32cb92cd98a992e2c`, 86 `5fdd02d49aee46e1e45ff8c96ce36edfbb481556d3a88968a46586c172f5cb9d`.
- The two files of entry 85 take distinct names because a pipeline ID is the file base name and both sources are called `pipeline.yml`.
- Entries 80 (row 1) and 82 (row 3) fail `fly validate-pipeline --strict` as stored, and the plain command accepts both. Entry 80 names the set_pipeline identifier `((instance_pipeline_name))`, which fly 8.3.3 reports as a deprecation, and strict mode turns that into an error. The converted Concourse file keeps the identifier and fails for the same reason. Entry 82 holds top-level keys that only carry YAML anchors, and strict mode reports them as unknown fields. The converted Concourse file omits those keys, reports each one as lost, passes the schema and passes strict mode. Row 4 and the flagged rows are not used.
- Entry 86 is also the file `testdata/concourse/capi-ci-cve-pipeline.yml`, which the parser tests read.
- Entry 87 is entry 80 with the three task files that its jobs reference, at the paths of the upstream repository (`tasks/go-test.yml`, `tasks/go-build.yml` and `apps/golang/binary-test.yml`), so the parser reads the task files that `file: repo/...` names. The files were fetched with `gh api repos/concourse/examples/contents/<path>?ref=<commit>` and stored byte-identical. SHA-256: `.concourse/parent.yml` `952697266151e6c550fd13fd1cc5bbb45f01a4095811c54e2f586949f4ad6f1f`, `tasks/go-test.yml` `b1505d07371ebefb767a70b91c61472a63e7bd8a1a26bf2541b53335bc072ba4`, `tasks/go-build.yml` `4d9a8b3f3f7527e7f0cb965ff914bab486492f921272aeb4781dd2079eda3c87`, `apps/golang/binary-test.yml` `cbc5f3c2f1288104a47d222bb61c784b663331fd79622fba44ff4fd431a16b58`. The licence is Apache-2.0 (host API `license.spdx_id`), as for entry 80. Entry 87 fails `fly validate-pipeline --strict` for the same reason as entry 80.
