# Gitea Actions

The provider ID is `gitea`. `docs/versions.md` pins Gitea v28.4.0 and Gitea Runner v6.0.0 (formerly act_runner). pipemaster reads `.yml` and `.yaml` files in `.gitea/workflows`, then `.github/workflows`. It writes only to `.gitea/workflows`.

## Setup outside the pipeline files

The only external verdict is `job.environment.approval`. Configure required reviewers on the environment in the repository settings. No pipeline file holds this setting.

## Conversion notes

- The parser reads the first directory that exists and does not merge directories. The default action host is `https://github.com`, the Gitea default `DEFAULT_ACTIONS_URL=github`.
- Gitea ignores `jobs.<id>.environment`, so the `job.environment` feature is lost.
- The `pull_request` trigger is native. The ref of the run is `refs/pull/N/head`, not the merge ref.
- Gitea rejects secret names that start with `GITHUB_` or `GITEA_`. It rejects variable names that start with `GITHUB_`, `GITEA_` or `CI`.
- Gitea v28 does not support `id-token`, so the emitter cannot write it. A chain that returns to the first provider restores it from the metadata.
- The expression context `gitea` is available beside the GitHub contexts.

## Feature support

The following table comes from `pipemaster caps --to gitea --format md`.

| Feature | Gitea | Strategy |
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
| job.environment | lost | (Gitea ignores jobs.<id>.environment) |
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
| trigger.pull_request | native | (the ref is refs/pull/N/head, not the merge ref) |
| trigger.push | native |  |
| trigger.resource | lost | (GitHub Actions has no resource triggers) |
| trigger.schedule | native |  |
| trigger.tag | native |  |
