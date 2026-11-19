# Pinned versions

These versions were verified on 2026-11-19. The Gitea and Forgejo dialects follow them.

| Product | Version | Date |
| --- | --- | --- |
| Gitea | v28.4.0 | 2026-11-15 |
| Gitea Runner (formerly act_runner) | v6.0.0 | 2026-11-11 |
| Forgejo | v17.0.2 | 2026-11-18 |
| Forgejo LTS | v15.0.12 | |
| forgejo-runner | v13.2.1 | 2026-10-29 |

## Sources

- <https://docs.gitea.com/usage/actions/comparison>
- <https://docs.gitea.com/usage/actions/faq>
- <https://docs.gitea.com/usage/actions/secrets>
- <https://forgejo.org/docs/latest/user/actions/reference/>
- <https://forgejo.org/docs/latest/user/actions/basic-concepts/>

## Workflow directories

The emitter writes only the first directory of each list. The parser reads the first directory of the list that exists. It does not merge directories. File extensions are `.yml` and `.yaml`.

| Provider | Directories |
| --- | --- |
| `gha` | `.github/workflows` |
| `gitea` | `.gitea/workflows`, then `.github/workflows` (the Gitea `WORKFLOW_DIRS` default) |
| `forgejo` | `.forgejo/workflows`, then `.gitea/workflows`, then `.github/workflows` (Forgejo `ListWorkflows`) |

## Default action hosts

A bare `owner/repo@ref` resolves to this host.

| Provider | Host |
| --- | --- |
| `gha` | `https://github.com` |
| `gitea` | `https://github.com` (`DEFAULT_ACTIONS_URL=github`, the default) |
| `forgejo` | `https://data.forgejo.org` |
