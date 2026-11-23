# GitLab CI

The provider ID is `gitlab`. `docs/versions.md` pins no GitLab version. pipemaster reads `.gitlab-ci.yml` and its local includes. It writes `.gitlab-ci.yml`.

## Setup outside the pipeline files

The caps table gives the verdict external for three features. Each one needs a setting that no CI file holds:

- `job.environment.approval`: set protected environment approval rules in the project settings.
- `trigger.api`: create a pipeline trigger token, or call the API.
- `trigger.schedule`: create a pipeline schedule in the project settings. GitLab keeps the cron string outside the CI file.

## Conversion notes

- The caps table holds the details of each emulated and lost feature. For example, a step has no `image`, `shell` or `timeout` field, and a job name survives only as its key.
- A GitLab downstream pipeline starts from a trigger job in the upstream project, so `trigger.pipeline_done` is lost.
- GitLab sets the job token scope in the project settings, so `pipeline.permissions` is lost.

## Feature support

The following table comes from `pipemaster caps --to gitlab --format md`.

| Feature | Gitlab | Strategy |
| --- | --- | --- |
| config.name | native |  |
| display.name | native | the pipeline name (workflow:name) (step names and job display names have no GitLab field and are lost; a job name survives only as its key; a pipeline name with a dollar sign or over 255 characters is lost) |
| expr.opaque | native | (only opaque expressions of the gitlab dialect; others are lost) |
| ext.passthrough | native |  |
| job.allow_failure | native |  |
| job.artifacts | native |  |
| job.cache | native |  |
| job.container | native |  |
| job.env | native |  |
| job.environment | native |  |
| job.environment.approval | external | protected environment approval rules in project settings |
| job.if | native |  |
| job.manual | native |  |
| job.matrix | native | parallel:matrix (include and exclude are expanded into explicit entries) |
| job.needs | native |  |
| job.needs.artifacts | native |  |
| job.outputs | emulated | dotenv artifact report |
| job.retry | native |  |
| job.runner | native |  |
| job.services | native |  |
| job.timeout | native |  |
| pipeline.concurrency | emulated | resource_group on every job (queues, does not cancel) |
| pipeline.env | native |  |
| pipeline.finally | native |  |
| pipeline.inputs | emulated | variables with description (input types and required flags are not enforced) |
| pipeline.permissions | lost | (job token scope is a project setting) |
| project.multi_pipeline | emulated | jobs of every pipeline in one file under <pipeline>:<job> keys (each job carries its pipeline's trigger condition; stage names and inputs are not restored) |
| script.interpolation | native |  |
| step.action | native | per actions.Lookup verdict (unknown actions are lost; the reference of a mapped checkout action is kept only in round-trip metadata, and without it a return to GHA writes the pinned default actions/checkout (not the source ref) with fetch-depth and submodules from GIT_DEPTH and GIT_SUBMODULE_STRATEGY) |
| step.continue_on_error | emulated | \|\| true after each line of the step |
| step.env | emulated | export lines inside a subshell around the step |
| step.if | emulated | shell guard around the step's lines (step reports success instead of skipped) |
| step.image | lost | (GitLab sets the image per job, not per step) |
| step.script | native |  |
| step.shell | lost | (GitLab runs scripts in the runner's configured shell) |
| step.timeout | lost | (GitLab has only a job timeout) |
| step.workdir | emulated | cd in a subshell around the step |
| trigger.api | external | pipeline trigger token or API call |
| trigger.filter.branches | native |  |
| trigger.filter.paths | native |  |
| trigger.filter.tags | native |  |
| trigger.manual | native |  |
| trigger.pipeline_done | lost | (a GitLab downstream pipeline starts from a trigger job in the upstream project) |
| trigger.pull_request | native |  |
| trigger.push | native |  |
| trigger.resource | lost | (GitLab CI has no resource triggers) |
| trigger.schedule | external | pipeline schedule in project settings (GitLab keeps the cron string outside the CI file) |
| trigger.tag | native |  |
