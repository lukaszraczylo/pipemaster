# Woodpecker CI

The provider ID is `woodpecker`. `docs/versions.md` pins Woodpecker CI v3.20.1. pipemaster reads `.yml` and `.yaml` files in `.woodpecker` (no subfolders), then `.woodpecker.yaml`, then `.woodpecker.yml`. It writes one workflow file for each job in `.woodpecker/`.

## Setup outside the pipeline files

The caps table gives the verdict external for three features. Set each one in the repository settings of Woodpecker:

- `job.timeout`: set Timeout. Woodpecker limits the whole pipeline, not a workflow.
- `trigger.schedule`: create the cron job in the repository settings. Woodpecker keeps the cron string there, not in the workflow file.
- `job.environment.approval`: set Require approval for in the repository settings.

## Conversion notes

- The emitter writes one workflow file for each job in `.woodpecker/`. The parser reads the first form that exists and does not merge forms.
- Woodpecker keeps only an `os/arch` runner label (`labels.platform`). The round-trip metadata restores the other labels when the chain returns to the origin. If a later hop emits GHA, Gitea or Forgejo, the emitter writes the default label `ubuntu-latest` and reports an emulated `job.runner` entry.
- Woodpecker substitutes `${NAME}` in the raw YAML and turns `$$` into a literal `$`. Thus the emitter writes `$` in script and environment text as `$$`. The parser reverses this.
- Woodpecker filters use doublestar globs. If a GHA filter uses `+` or `?`, doublestar cannot express it. The emitter then writes an anchored regular expression in `evaluate`.
- The runner is lost. Only a platform label becomes `labels.platform`.

## Feature support

The following table comes from `pipemaster caps --to woodpecker --format md`.

| Feature | Woodpecker | Strategy |
| --- | --- | --- |
| config.name | native |  |
| display.name | native | the step name (pipeline and job names that differ from the id are lost; the workflow name is the file name) |
| expr.opaque | native | (only opaque expressions of the woodpecker dialect; others are lost) |
| ext.passthrough | native |  |
| job.allow_failure | emulated | failure ignore on every step (later steps still run after a failed step) |
| job.artifacts | emulated | shared workspace of the steps in one workflow (other workflows cannot read the files) |
| job.cache | emulated | shared workspace of the steps in one workflow (nothing persists between runs) |
| job.container | native |  |
| job.env | emulated | copied into the environment of every step (a step variable of the same name wins) |
| job.environment | lost | (Woodpecker has no environment name or URL in the workflow) |
| job.environment.approval | external | repo setting Require approval for |
| job.if | native | workflow when (only the translatable expression subset; any other condition disables the workflow) |
| job.manual | emulated | when event manual; a person starts the workflow from the UI or API (the workflow no longer runs on its normal events) |
| job.matrix | native |  |
| job.needs | native |  |
| job.needs.artifacts | lost | (workflows do not share a workspace) |
| job.outputs | lost | (Woodpecker steps cannot pass values to other workflows) |
| job.retry | lost | (Woodpecker has no retry key; restarts are manual) |
| job.runner | lost | (only a platform label has a Woodpecker form (labels.platform); other labels are lost) |
| job.services | native |  |
| job.timeout | external | repo setting Timeout (Woodpecker limits the whole pipeline, not a workflow) |
| pipeline.concurrency | native | workflow concurrency limit 1 with group (cancel-in-progress has no Woodpecker form) |
| pipeline.env | emulated | copied into the environment of every step (a step variable of the same name wins) |
| pipeline.finally | emulated | a workflow that depends on every other workflow and runs on status success or failure (a skipped dependency is optional, so the workflow still runs) |
| pipeline.inputs | lost | (Woodpecker has no typed pipeline inputs) |
| pipeline.permissions | lost | (token scopes are forge and repo settings) |
| project.multi_pipeline | emulated | each job of each pipeline is a workflow named <pipeline>-<job> (each workflow carries its pipeline's trigger condition) |
| script.interpolation | native |  |
| step.action | native | per actions.Lookup verdict; a plugin step is native (unknown actions are lost; the reference of a mapped checkout action is kept only in round-trip metadata, and without it a return to GHA writes no checkout step and drops its inputs such as fetch-depth) |
| step.continue_on_error | native |  |
| step.env | native |  |
| step.if | native |  |
| step.image | native |  |
| step.script | native |  |
| step.shell | lost | (Woodpecker runs commands through /bin/sh -e of the image; a step with no shell from a GHA or GitLab source runs under bash there, so bash-only syntax fails when the image sh is dash) |
| step.timeout | lost | (Woodpecker has only a pipeline timeout in the repo settings) |
| step.workdir | native |  |
| trigger.api | lost | (Woodpecker starts API pipelines as manual events) |
| trigger.filter.branches | native | branch filter (only exact globs are native; other patterns keep doublestar meaning) |
| trigger.filter.paths | native |  |
| trigger.filter.tags | emulated | ref filter refs/tags/<pattern> on the tag event (the glob dialect is doublestar; the parser reads it back as the tag trigger filter) |
| trigger.manual | native |  |
| trigger.pipeline_done | lost | (Woodpecker has no pipeline-completion trigger) |
| trigger.pull_request | native |  |
| trigger.push | native |  |
| trigger.resource | lost | (Woodpecker CI has no resource triggers) |
| trigger.schedule | external | when event cron; the schedule is a repo cron job (Woodpecker keeps the cron string in the repo settings (Cron), not in the workflow file) |
| trigger.tag | native |  |
