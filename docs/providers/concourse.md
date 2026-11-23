# Concourse

The provider ID is `concourse`. `docs/versions.md` pins Concourse v8.3.3, fly v8.3.3 and concourse/time-resource v1.11.6. pipemaster reads `.yml` and `.yaml` files in `.concourse`, one pipeline for each file. It writes `.concourse/<pipeline>.yml`.

## Setup outside the pipeline files

The caps table gives the verdict external for `job.needs.artifacts`. Concourse passes no artifact between jobs by itself. Set up an `s3` or `registry-image` resource between the jobs, with its credentials.

The pipeline file leaves one input open:

- The generated `git` resource has the placeholder `uri: ((git-uri))`. The operator must supply `git-uri` through a credential manager or `fly set-pipeline --var`. The report names this entry.

## Conversion notes

- The emitter writes one file for each pipeline, `.concourse/<pipeline>.yml`, and every task is inline. The parser reads every `.yml` and `.yaml` file in `.concourse/` as one pipeline, and the file name is the pipeline ID. A file is a Concourse pipeline only when it has a top-level `jobs` list whose items have a `plan`. The parser rejects a repeated mapping key, as `fly` does, and it accepts anchors and `<<` merge keys.
- A Concourse pipeline has no `on:` block, so a resource is the trigger. A `get` with `trigger: true` on a `git` resource is a push trigger. A `get` with `trigger: true` on a `time` resource is a schedule. The emitter writes only `git` and `time` resources. A task image is an inline `image_resource` of the task, not a pipeline resource. A trigger on any other resource type is lost, and the resource stays in the Ext data.
- A git resource follows one branch. A push trigger with two exact branches becomes two resources, and the job runs once for each branch event. A branch glob is lost. A `git` resource filters tags with one `tag_filter` pattern, so a tag trigger that also has a branch filter is lost.
- Each job starts with a `get` of the source resource, and each job has its own trigger set. A person starts a job whose `get` steps do not trigger. A `needs` entry becomes `passed:` on a `get` of a resource that both jobs get, and the source resource is the usual one. A `passed:` list is an AND, so a job that needs jobs A and B runs on a version that went through both. If the job has no such resource, the emitter adds a `get` of a resource that the needed job gets and this job does not. If no resource orders the two jobs, the emitter does not write the edge and reports a lost `job.needs` entry.
- Each step becomes one `task` step with an inline `config`. Concourse runs each task in a fresh container, so the tasks of a job pass one artifact, `workspace`, from task to task. The artifact is both an input and an output of each task, and the first task maps the source artifact to it with `input_mapping`. A job with no script step writes no `workspace`. The artifact works only within one job. The scripts run as `sh -ec`, and a `((var))` never goes into a script text: values reach a script through task `params`.
- A step with no image gets `ubuntu:latest`, and the report names this default. A PowerShell step runs with `run.path: powershell` or `pwsh` and `platform: windows`, only when the IR names that shell or a Windows runner. A script that may be PowerShell stays `sh`, and the report names the step.
- A cron schedule becomes a `time` resource, and the conversion is approximate. The resource is built to run "at least once every N minutes", not at a wall-clock time. A step of N minutes or N hours that divides the hour or the day becomes `interval`. An interval counts from the last version, so it has no clock alignment. A daily time becomes a window of 5 minutes (`start` and `stop`), so the job starts within 5 minutes after the cron time. A list of days becomes `days` on a daily window, and a day-list window that passes midnight is lost. A list of days combined with an interval step is lost. Any other cron form is lost, and the cron text stays in the Ext data. The pinned `time` resource is v1.11.6.
- A `time` resource with an `interval` emits a version at its first check. A `time` resource with a window has no version until its first window passes, so a manual start before then has no input. The emitter does not write `initial_version`, because it starts the job at once. For a window resource, the operator can run `fly check-resource -r <pipeline>/<resource> --from time:2000-01-01T00:00:00Z` after `fly set-pipeline`. The report names this command for a window resource only.
- Conditions use the shared shell guard on a script task. A status function becomes a job hook (`on_failure` or `ensure`), and several `finally` steps wrap in `do`. A condition with no guard form runs as a task whose first script line is `exit 0`, with a lost entry. A job condition with no guard form leaves the job with no triggering `get`.
- Concourse has no expression language, so every opaque expression is lost. An action with no task form leaves a placeholder task that runs `true`. A pipeline with no jobs gets a placeholder job. A marker comment in the file restores each of these on the way back. If you edit or remove the placeholder, the converter keeps your file and reports that it ignored the marker.
- The parser keeps the YAML of a Concourse file that the IR cannot hold in the Ext data. The emitter writes it back unchanged when the file returns to Concourse. The emitter writes no unknown top-level key of a Concourse file, because Concourse rejects it. It reports each such key as lost, with a different reason for a key that only holds YAML anchors. The metadata restores these keys on a return to Concourse.
- The validator checks the emitted files against a schema that is written by hand from the v8.3.3 structs of Concourse. The tests also run `fly validate-pipeline --strict`. The pinned versions are in `docs/versions.md`. `make linters` runs `scripts/install-linters.sh`, which installs the pinned `fly` into `.tools/bin` beside the other linters.

## Feature support

The following table comes from `pipemaster caps --to concourse --format md`.

| Feature | Concourse | Strategy |
| --- | --- | --- |
| config.name | native | ((name)) in task params (the credential manager that resolves ((name)) is external, and a ((var)) never goes into a script text) |
| display.name | native |  |
| expr.opaque | lost | (Concourse has no expression language, so every opaque expression is lost) |
| ext.passthrough | native |  |
| job.allow_failure | emulated | try around the plan (the job shows success, not failure) |
| job.artifacts | emulated | workspace artifact handed from task to task (inputs and outputs of the same name) (only within one job; a job with no script step writes no workspace) |
| job.cache | emulated | task config caches |
| job.container | native | image_resource on every task |
| job.env | emulated | copied into the params of every task (a step variable of the same name wins) |
| job.environment | lost | (Concourse has no environment name or URL) |
| job.environment.approval | emulated | job with no triggering get, started by a person (Concourse has no approver identity: anyone who may trigger the job approves it, and the reviewer list is lost; a job whose get steps carry different trigger sets keeps the set in Ext[concourse][triggers], and a job whose get steps do not trigger is started by a person) |
| job.if | emulated | shared shell guard on script tasks; a status function becomes a job hook (on_failure or ensure) (a condition with no guard form leaves the job with no triggering get, so a person starts it, and adds a lost entry) |
| job.manual | emulated | job with no triggering get, started by a person (no approver identity; a job whose get steps carry different trigger sets keeps the set in Ext[concourse][triggers], and a job whose get steps do not trigger is started by a person) |
| job.matrix | emulated | across over the axes (outputs of the substep are not visible outside the across step) |
| job.needs | native | passed on the get of the shared source resource (passed lists are an AND) |
| job.needs.artifacts | external | s3 or registry-image resource between the jobs (the operator sets up the s3 or registry resource and its credentials; Concourse passes no artifact between jobs by itself) |
| job.outputs | lost | (a Concourse job has no named outputs for other jobs) |
| job.retry | emulated | attempts on a do wrapper of the plan |
| job.runner | emulated | worker tags of the task (tags name workers, not hosted runner labels) |
| job.services | lost | (Concourse tasks have no service containers) |
| job.timeout | emulated | timeout on a do wrapper of the plan |
| pipeline.concurrency | emulated | serial_groups on the jobs (cancel-in-progress is lost; a second build waits) |
| pipeline.env | emulated | copied into the params of every task (a step variable of the same name wins) |
| pipeline.finally | emulated | job hook (ensure) on the single sink job (several finally steps wrap in do; a pipeline with no single sink job loses the finally steps) |
| pipeline.inputs | lost | (a Concourse pipeline has no run-time inputs; fly set-pipeline vars apply at set time) |
| pipeline.permissions | lost | (permissions come from the team and the credential manager) |
| project.multi_pipeline | emulated | one pipeline file per IR pipeline under .concourse/ (each pipeline needs its own fly set-pipeline and gets its own triggers) |
| script.interpolation | native |  |
| step.action | emulated | task per actions.Lookup verdict (unknown actions are lost) |
| step.continue_on_error | native | try around the task |
| step.env | native | task params |
| step.if | emulated | shared shell guard in the script of the task (a condition with no guard form runs as a task whose first script line is exit 0, with a lost entry) |
| step.image | native | task config image_resource |
| step.script | native | task config run.path and run.args |
| step.shell | emulated | run.path names the shell (the image must provide the shell) |
| step.timeout | native | task timeout |
| step.workdir | native | task config run.dir |
| trigger.api | lost | (Concourse has no resource, listener or Ext entry that the emitter can restore for an API trigger, so it is lost; a webhook_token on a resource is kept in Ext only) |
| trigger.filter.branches | native | source.branch of the git resource (one exact branch per resource; a glob is lost) |
| trigger.filter.paths | native | source.paths and source.ignore_paths of the git resource |
| trigger.filter.tags | emulated | source.tag_filter of the git resource (one glob(7) pattern per resource; an exclude pattern is lost) |
| trigger.manual | native | job with no triggering get |
| trigger.pipeline_done | lost | (Concourse has no pipeline-completion trigger) |
| trigger.pull_request | lost | (Concourse has no pull request resource type in its core; a pull request resource type must be configured by the operator) |
| trigger.push | native | git resource get with trigger: true (a git resource follows one branch, so a push trigger with two exact branches is two resources and the job runs once per branch event) |
| trigger.resource | lost | (the emitter writes git and time resources only; a trigger on any other resource is lost) |
| trigger.schedule | emulated | time resource get with trigger: true; the source is interval for a step that divides the hour or the day, start and stop for a daily window, and days for a day list (the time resource is built for at least once every N minutes, not for a wall-clock time. An interval counts from the last version, so it has no clock alignment, the first check emits a version at once, and an every-minute cron cannot fire faster than the check cadence. A daily window is 5 minutes wide, so the job starts within 5 minutes after the cron time; a window that passes midnight is lost when it has a day list. Any other cron form is lost with the cron text kept) |
| trigger.tag | emulated | git resource with source.tag_filter (a tag trigger that also has a branch filter is lost, because one git resource cannot hold both sets) |
