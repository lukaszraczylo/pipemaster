# Forgejo Actions

The provider ID is `forgejo`. `docs/versions.md` pins Forgejo v17.0.2, Forgejo LTS v15.0.12 and forgejo-runner v13.2.1. pipemaster reads `.yml` and `.yaml` files in `.forgejo/workflows`, then `.gitea/workflows`, then `.github/workflows`. It writes only to `.forgejo/workflows`.

## Setup outside the pipeline files

The only external verdict is `job.environment.approval`. Configure required reviewers on the environment in the repository settings. No pipeline file holds this setting.

## Conversion notes

- The parser reads the first directory that exists and does not merge directories. The default action host is `https://data.forgejo.org`, not GitHub.
- Forgejo does not document `environment`, so the `job.environment` feature is lost.
- Forgejo does not document `permissions`, so the `pipeline.permissions` feature is lost. The token has write access, except on fork pull requests.
- Forgejo rejects variable names that start with `FORGEJO_`, `GITHUB_` or `GITEA_`, and the name `CI`.
- Forgejo accepts the workflow keys `enable-email-notifications` and `enable-openid-connect`, and the job key `enable-openid-connect`. The last key enables OIDC where Forgejo has no `id-token` scope.
- Forgejo cancels earlier `push` and `pull_request` runs by default while `concurrency` is unset. If the pipeline needs this behaviour, the emitter writes a run-group `concurrency` block.
- The expression contexts `forgejo` and `forge` are available beside the GitHub contexts.

## Feature support

The following table comes from `pipemaster caps --to forgejo --format md`.

| Feature | Forgejo | Strategy |
| --- | --- | --- |
| config.name | native | (a name the target rejects is external; create it under another name) |
| display.name | native |  |
| expr.opaque | native | (only opaque expressions of the gha dialect; others are lost) |
| ext.passthrough | native |  |
| job.allow_failure | native |  |
| job.artifacts | emulated | upload-artifact step at job end |
| job.cache | emulated | actions/cache step; policy pull is actions/cache/restore, policy push is actions/cache/save at job end (a key with no hashFiles call becomes a rolling key (<key>-run-<run_id>, restored by prefix) so each run saves a cache as GitLab does; GitHub restores only caches of the current branch, the base branch of a pull request and the default branch, while a GitLab key with no branch part is shared across branches) |
| job.container | native |  |
| job.env | native |  |
| job.environment | lost | (Forgejo does not document environment) |
| job.environment.approval | external | configure required reviewers on the environment in repository settings |
| job.if | native |  |
| job.manual | emulated | workflow_dispatch trigger |
| job.matrix | native |  |
| job.needs | native |  |
| job.needs.artifacts | emulated | download-artifact step at job start |
| job.outputs | native |  |
| job.retry | emulated | step-level retry loop via shell (Retry.Attempts is the total number of runs, so GitLab retry: N is Attempts N+1) |
| job.runner | native |  |
| job.services | native |  |
| job.timeout | native |  |
| pipeline.concurrency | native |  |
| pipeline.env | native |  |
| pipeline.finally | emulated | job with if: always() that needs every other job |
| pipeline.inputs | native |  |
| pipeline.permissions | lost | (Forgejo does not document permissions; the token has write access except on fork pull requests) |
| project.multi_pipeline | native |  |
| script.interpolation | native |  |
| step.action | native |  |
| step.continue_on_error | native |  |
| step.env | native |  |
| step.if | native |  |
| step.image | emulated | uses: docker://<image> with entrypoint and args |
| step.script | native |  |
| step.shell | native |  |
| step.timeout | native |  |
| step.workdir | native |  |
| trigger.api | native |  |
| trigger.filter.branches | native |  |
| trigger.filter.paths | native |  |
| trigger.filter.tags | native |  |
| trigger.manual | native |  |
| trigger.pipeline_done | native | workflow_run |
| trigger.pull_request | native |  |
| trigger.push | native |  |
| trigger.resource | lost | (GitHub Actions has no resource triggers) |
| trigger.schedule | native |  |
| trigger.tag | native |  |
