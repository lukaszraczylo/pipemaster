# Drone CI

The provider ID is `drone`. `docs/versions.md` pins Drone server v2.28.2, Drone docker runner v1.8.5 and drone-cli v1.9.0. Drone is in maintenance mode. pipemaster reads and writes `.drone.yml`.

## Setup outside the pipeline files

The caps table gives the verdict external for two features:

- `job.timeout`: set the timeout in the repository settings. The setting has no per-pipeline or per-step form.
- `trigger.schedule`: create the cron job in the UI or with `drone cron add`. The trigger uses the event `cron` with the cron job name. The report names the command.

## Conversion notes

- Drone replaces `${NAME}` in the raw YAML before it parses the file. The literal `$` escape is `$$`.
- A matrix becomes one pipeline document per cell. Each document carries a marker, so a return conversion can fold the cells into one matrix job.
- The Drone OSS server does not evaluate `paths` filters. The converter reports them as lost and never emits them.
- A script from a bash source (GHA, GitLab) becomes one command that runs the script under bash when the image has bash. The image `sh` can be dash, which fails on `[[ ]]` and arrays. The report marks the wrapper as emulated, and the return conversion removes it.
- A script that reads a predefined variable of another provider, such as `$CI_PROJECT_DIR`, starts with a prelude that defines the name from the Drone value. A variable with no Drone source stops the step with a message. The table is `vars/vars.yaml`.

## Feature support

The following table comes from `pipemaster caps --to drone --format md`.

| Feature | Drone | Strategy |
| --- | --- | --- |
| config.name | native |  |
| display.name | native | the pipeline and step names (a name that differs from the id is lost) |
| expr.opaque | native | (only opaque expressions of the drone dialect; others are lost) |
| ext.passthrough | native |  |
| job.allow_failure | emulated | failure ignore on every step (later steps still run after a failed step) |
| job.artifacts | emulated | shared workspace of the steps in one document (other documents cannot read the files) |
| job.cache | emulated | shared workspace of the steps in one document (nothing persists between runs; for persistence add a cache plugin step (for example drone-cache) with an S3-compatible bucket or a host volume, set up by the operator) |
| job.container | native |  |
| job.env | native | pipeline environment |
| job.environment | lost | (Drone has no environment name or URL in the pipeline file) |
| job.environment.approval | emulated | trigger event promote with target set to the environment (a person starts the build with drone build promote) |
| job.if | native | pipeline trigger conditions (only the translatable expression subset; any other condition is emulated or lost) |
| job.manual | emulated | trigger event custom (a person starts the build through the API or drone build create; the pipeline no longer runs on its normal events) |
| job.matrix | emulated | one pipeline document per cell (fail-fast and max-parallel are lost; a failed sibling stops the others without order) |
| job.needs | native |  |
| job.needs.artifacts | lost | (documents share no workspace) |
| job.outputs | lost | (Drone steps cannot pass values to other pipelines) |
| job.retry | lost | (Drone has no retry key; only clone.retries exists) |
| job.runner | external | workers the operator provides (only the platform has a Drone form; other runner labels are lost) |
| job.services | native |  |
| job.timeout | external | repo setting timeout (the setting has no per-pipeline or per-step form) |
| pipeline.concurrency | lost | (Drone concurrency.limit is a count and the IR models a group and cancel-in-progress, not a limit) |
| pipeline.env | emulated | copied into the environment of every pipeline document (a step variable of the same name wins) |
| pipeline.finally | emulated | a document that depends on every other document and runs on trigger status success and failure (the server skips a stage whose status filter does not match the build result) |
| pipeline.inputs | lost | (Drone has no typed pipeline inputs) |
| pipeline.permissions | lost | (token scopes are server and repo settings) |
| project.multi_pipeline | emulated | each pipeline is a document named <pipeline>-<job> (each document carries its pipeline's trigger condition) |
| script.interpolation | native |  |
| step.action | emulated | per actions.Lookup verdict; a plugin step with settings (unknown actions are lost; the reference of a mapped checkout action is kept only in round-trip metadata, and without it a return to GHA writes no checkout step and drops its inputs such as fetch-depth) |
| step.continue_on_error | native |  |
| step.env | native |  |
| step.if | native | step when (only the translatable expression subset; any other condition is emulated or lost) |
| step.image | native |  |
| step.script | native |  |
| step.shell | emulated | bash-if-present wrapper command (Drone pipes commands to /bin/sh; a step that runs under bash at the source becomes one command that re-runs the script under bash -e when the image has bash, else under sh -e, so bash-only syntax fails under sh; any other shell is lost) |
| step.timeout | lost | (the docker runner ignores a step timeout) |
| step.workdir | native |  |
| trigger.api | lost | (Drone starts API builds as event custom) |
| trigger.filter.branches | native | trigger branch (only exact globs are native; for a pull request Drone matches the target branch) |
| trigger.filter.paths | lost | (the Drone OSS server does not evaluate trigger.paths, so the emitter never writes it) |
| trigger.filter.tags | emulated | ref filter refs/tags/<pattern> on the tag event (the glob dialect is doublestar; the parser reads it back as the tag trigger filter, and branch is not available on tag events) |
| trigger.manual | emulated | trigger event custom (a person starts the build through the API or drone build create) |
| trigger.pipeline_done | lost | (Drone has no pipeline-completion trigger) |
| trigger.pull_request | native |  |
| trigger.push | native |  |
| trigger.resource | lost | (Drone CI has no resource triggers) |
| trigger.schedule | external | trigger event cron with the cron job name (cron jobs are set in the UI or with drone cron add; the report names the command) |
| trigger.tag | native |  |
