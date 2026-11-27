# GitHub Actions

The provider ID is `gha`. GitHub Actions is the source dialect of pipemaster, and the Gitea and Forgejo providers extend it. `docs/versions.md` pins no GitHub Actions version. It pins actionlint v1.7.12, which checks the emitted workflows in the tests. pipemaster reads and writes `.yml` and `.yaml` files in `.github/workflows`.

## Setup outside the pipeline files

The only external verdict is `job.environment.approval`. Configure required reviewers on the environment in the repository settings. No pipeline file holds this setting.

## Conversion notes

- The parser reads the first directory that exists in its list and does not merge directories. For `gha` the list holds only `.github/workflows`.
- A bare `owner/repo@ref` action resolves to `https://github.com`. The default action hosts of all providers are in [versions.md](../versions.md).
- The action registry in [mappings.yaml](../../actions/mappings.yaml) maps each `uses:` reference to the construct of every other provider. An action with no mapping for the target is lost.
- A source that clones the repository by itself (GitLab, Drone, Woodpecker, Concourse) gets an `actions/checkout` step, because GitHub Actions does not clone. The report marks the step as emulated.
- A cache key with no `hashFiles` call becomes a rolling key. GitHub restores only the caches of the current branch, the base branch of a pull request and the default branch. A GitLab key with no branch part is shared across branches. The caps table row for `job.cache` states this.
- Gitea and Forgejo reuse the GitHub Actions parser and emitter with a table of differences. See [Gitea](gitea.md) and [Forgejo](forgejo.md).

## Feature support

The following table comes from `pipemaster caps --to gha --format md`.

| Feature | Gha | Strategy |
| --- | --- | --- |
| config.name | native |  |
| display.name | native |  |
| expr.opaque | native | (only opaque expressions of the gha dialect; others are lost) |
| ext.passthrough | native |  |
| job.allow_failure | native |  |
| job.artifacts | emulated | upload-artifact step at job end |
| job.cache | emulated | actions/cache step; policy pull is actions/cache/restore, policy push is actions/cache/save at job end (a key with no hashFiles call becomes a rolling key (<key>-run-<run_id>-<run_attempt>, restored by prefix) so each run saves a cache as GitLab does; GitHub restores only caches of the current branch, the base branch of a pull request and the default branch, while a GitLab key with no branch part is shared across branches) |
| job.container | native |  |
| job.env | native |  |
| job.environment | native |  |
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
| pipeline.permissions | native |  |
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
