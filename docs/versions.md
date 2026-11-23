# Pinned versions

These versions were verified on 2026-11-19. The Tekton Pipelines, Tekton Triggers and kubeconform rows were verified on 2026-11-21. The Argo rows were verified on 2026-11-22. The Concourse rows were verified on 2026-11-23. The Gitea, Forgejo, Woodpecker, Drone, Tekton, Argo and Concourse dialects follow them.

| Product | Version | Date |
| --- | --- | --- |
| Gitea | v28.4.0 | 2026-11-15 |
| Gitea Runner (formerly act_runner) | v6.0.0 | 2026-11-11 |
| Forgejo | v17.0.2 | 2026-11-18 |
| Forgejo LTS | v15.0.12 | |
| forgejo-runner | v13.2.1 | 2026-10-29 |
| Woodpecker CI | v3.20.1 | 2026-10-29 |
| Drone server | v2.28.2 | 2026-04-20 |
| Drone docker runner | v1.8.5 | |
| drone-cli | v1.9.0 | |
| Tekton Pipelines | v1.18.0 | 2026-10-29 |
| Tekton Triggers | v0.38.0 | 2026-11-09 |
| Argo Workflows | v4.1.6 | 2026-10-22 |
| Argo Events | v1.9.11 | 2026-07-13 |
| argo CLI | v4.1.6 | 2026-10-22 |
| kubeconform | v0.8.0 | 2026-06-04 |
| Concourse | v8.3.3 | 2026-11-03 |
| fly | v8.3.3 | 2026-11-03 |
| concourse/time-resource | v1.11.6 | 2026-11-02 |

## Sources

- <https://docs.gitea.com/usage/actions/comparison>
- <https://docs.gitea.com/usage/actions/faq>
- <https://docs.gitea.com/usage/actions/secrets>
- <https://forgejo.org/docs/latest/user/actions/reference/>
- <https://forgejo.org/docs/latest/user/actions/basic-concepts/>
- <https://github.com/woodpecker-ci/woodpecker/releases/tag/v3.20.1>
- <https://woodpecker-ci.org/docs/usage/workflow-syntax>
- <https://github.com/harness/harness/releases/tag/v2.28.2>
- <https://docs.drone.io/pipeline/docker/syntax/>
- <https://github.com/tektoncd/pipeline/releases/tag/v1.18.0>
- <https://github.com/tektoncd/triggers/releases/tag/v0.38.0>
- <https://github.com/argoproj/argo-workflows/releases/tag/v4.1.6>
- <https://github.com/argoproj/argo-events/releases/tag/v1.9.11>
- <https://github.com/datreeio/CRDs-catalog/tree/784ec895547a36e548e6810b41f27baade3a0890/argoproj.io>
- <https://github.com/yannh/kubeconform/releases/tag/v0.8.0>
- <https://github.com/concourse/concourse/releases/tag/v8.3.3>
- <https://concourse-ci.org/docs/pipelines/>
- <https://github.com/concourse/time-resource/tree/v1.11.6>

Drone is in maintenance mode. Harness ships occasional server releases from the `drone` branch, and no release has followed v2.28.2.

## Workflow directories

The emitter writes only the first directory of each list. The parser reads the first directory of the list that exists. It does not merge directories. File extensions are `.yml` and `.yaml`.

| Provider | Directories |
| --- | --- |
| `gha` | `.github/workflows` |
| `gitea` | `.gitea/workflows`, then `.github/workflows` (the Gitea `WORKFLOW_DIRS` default) |
| `forgejo` | `.forgejo/workflows`, then `.gitea/workflows`, then `.github/workflows` (Forgejo `ListWorkflows`) |
| `woodpecker` | `.woodpecker` (no subfolders), then `.woodpecker.yaml`, then `.woodpecker.yml` |
| `drone` | `.drone.yml` |
| `tekton` | `.tekton` (all YAML files, including subfolders) |
| `argo` | `.argo` (all YAML files, including subfolders) |
| `concourse` | `.concourse` (all YAML files, one pipeline for each file) |

## Default action hosts

A bare `owner/repo@ref` resolves to this host.

| Provider | Host |
| --- | --- |
| `gha` | `https://github.com` |
| `gitea` | `https://github.com` (`DEFAULT_ACTIONS_URL=github`, the default) |
| `forgejo` | `https://data.forgejo.org` |
