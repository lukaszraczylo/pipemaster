# pipemaster

pipemaster is a Go command-line tool that converts CI/CD pipelines between providers through a shared intermediate representation. It reports for every feature whether the conversion is native, emulated, external or lost, so nothing is dropped silently.

Status: pre-alpha.

## Supported providers

The provider ID is the value that you pass to `--from`, `--to`, `--via` and `--as`.

| ID | Provider | Pipeline files |
| --- | --- | --- |
| `gha` | GitHub Actions | `.yml` and `.yaml` files in `.github/workflows` |
| `gitea` | Gitea Actions | `.yml` and `.yaml` files in `.gitea/workflows`, then `.github/workflows` |
| `forgejo` | Forgejo Actions | `.yml` and `.yaml` files in `.forgejo/workflows`, then `.gitea/workflows`, then `.github/workflows` |
| `gitlab` | GitLab CI | `.gitlab-ci.yml` and its local includes |
| `woodpecker` | Woodpecker CI | `.yml` and `.yaml` files in `.woodpecker`, then `.woodpecker.yaml`, then `.woodpecker.yml` |
| `drone` | Drone CI | `.drone.yml` |
| `tekton` | Tekton Pipelines and Triggers | `.yaml` and `.yml` files in `.tekton`, including its subfolders |
| `argo` | Argo Workflows and Argo Events | `.yaml` and `.yml` files in `.argo`, including its subfolders |

The parser reads the first directory of the list that exists and does not merge directories. The emitter writes only the first directory. If a repository holds both `.github/workflows` and `.gitea/workflows`, `detect` lists `gha` and `gitea`. Then `convert` needs `--from`. The pinned versions and the default action hosts are in [docs/versions.md](docs/versions.md).

## Installing

Build the command from a checkout. The module needs Go 1.27.

```sh
go build -o pipemaster ./cmd/pipemaster
```

## Using the command line

The commands `detect`, `inspect`, `convert`, `roundtrip` and `validate` take a directory that holds the pipeline files. The commands `version` and `caps` take no directory. If you omit `--from`, the commands `inspect`, `convert` and `roundtrip` detect the source provider from the files in that directory.

| Command | Purpose |
| --- | --- |
| `pipemaster version` | Print the version. |
| `pipemaster detect <dir>` | List the providers whose config exists in the directory. |
| `pipemaster inspect <dir> [--from <provider>]` | Print the canonical IR of the directory. |
| `pipemaster convert <dir> --to <provider> [--from <provider>] [-o <dir>] [--report text\|md\|json] [--strict] [--embed-meta] [--force]` | Convert the pipelines to another provider. |
| `pipemaster roundtrip <dir> --via <provider> [--from <provider>]` | Convert there and back, then print any differences. |
| `pipemaster caps --from <provider> --to <provider> [--format text\|md]` | Compare the feature support of two providers. |
| `pipemaster validate <dir> --as <provider>` | Check the pipeline files against the provider schema. |

### Converting a pipeline

This example converts a GitHub Actions repository to GitLab CI.

```sh
pipemaster convert ./repo --to gitlab -o ./out --report md
```

The command writes the converted files and a metadata file to the output directory. The default output directory is the current directory. The command prints the loss report to standard error.

The metadata file is `.pipemaster/meta.yaml`. It holds the data that the target format cannot hold. If you use `--embed-meta`, the command writes the metadata into the converted file as comments instead.

### Checking a round trip

```sh
pipemaster roundtrip ./repo --via gitlab
```

The command prints `no differences` when the pipeline survives the conversion to GitLab CI and back. Otherwise, it prints the differences.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | The command succeeded. |
| 1 | The command failed with an error. |
| 2 | The command line is wrong. |
| 3 | You set `--strict` and at least one feature is not native. |
| 4 | The round-trip metadata is stale. Use `--force` to convert anyway. |

## Chained conversions

You can convert the output of one conversion again. The metadata file holds one layer for each conversion, oldest first, so a chain through several providers can return to the first provider. Metadata `version: 2` holds the layers. A `version: 1` file from an earlier release reads as one layer.

This example converts GHA to GitLab CI to Gitea Actions and back to GHA. Each step reads the previous output.

```sh
pipemaster convert ./repo --from gha --to gitlab -o ./b
pipemaster convert ./b --from gitlab --to gitea -o ./c
pipemaster convert ./c --from gitea --to gha -o ./d
```

The last step returns to the first provider. It restores the first pipeline and writes no sidecar file. If the first provider cannot write a key, the return writes a layer that holds only that key. Gitea cannot write `id-token`, for example.

If you edit a converted file and convert it again, the command exits with code 4. With `--force`, the command drops the older layers and reports each one. It never restores a stale layer.

To check a chain, pass several providers to `roundtrip`:

```sh
pipemaster roundtrip ./repo --from gha --via gitlab,gitea
```

The command prints `no differences` when the pipeline survives the whole chain.

## Reading the loss report

The report gives one verdict for each feature that the pipeline uses. The verdicts, from best to worst, are:

- `native`: the target has the same feature.
- `emulated`: the target gets an equivalent built from other features. The converted file marks it with a `# pipemaster:emulated` comment.
- `external`: the target must configure the feature outside the pipeline file.
- `lost`: the target cannot express the feature.

## Feature support from GHA to Gitea and Forgejo

The following tables come from `pipemaster caps --from gha --to gitea --format md` and `pipemaster caps --from gha --to forgejo --format md`.

### Gitea

| Feature | Gha | Gitea | Strategy |
| --- | --- | --- | --- |
| config.name | native | native | (a name the target rejects is external; create it under another name) |
| display.name | native | native |  |
| expr.opaque | native | native | (only opaque expressions of the gha dialect; others are lost) |
| ext.passthrough | native | native |  |
| job.allow_failure | native | native |  |
| job.artifacts | emulated | emulated | upload-artifact step at job end |
| job.cache | emulated | emulated | actions/cache step |
| job.container | native | native |  |
| job.env | native | native |  |
| job.environment | native | lost | (Gitea ignores jobs.<id>.environment) |
| job.environment.approval | external | external | configure required reviewers on the environment in repository settings |
| job.if | native | native |  |
| job.manual | emulated | emulated | workflow_dispatch trigger |
| job.matrix | native | native |  |
| job.needs | native | native |  |
| job.needs.artifacts | emulated | emulated | download-artifact step at job start |
| job.outputs | native | native |  |
| job.retry | emulated | emulated | step-level retry loop via shell (Retry.Attempts is the total number of runs, so GitLab retry: N is Attempts N+1) |
| job.runner | native | native |  |
| job.services | native | native |  |
| job.timeout | native | native |  |
| pipeline.concurrency | native | native |  |
| pipeline.env | native | native |  |
| pipeline.finally | emulated | emulated | job with if: always() that needs every other job |
| pipeline.inputs | native | native |  |
| pipeline.permissions | native | native |  |
| project.multi_pipeline | native | native |  |
| script.interpolation | native | native |  |
| step.action | native | native |  |
| step.continue_on_error | native | native |  |
| step.env | native | native |  |
| step.if | native | native |  |
| step.image | emulated | emulated | uses: docker://<image> with entrypoint and args |
| step.script | native | native |  |
| step.shell | native | native |  |
| step.timeout | native | native |  |
| step.workdir | native | native |  |
| trigger.api | native | native |  |
| trigger.filter.branches | native | native |  |
| trigger.filter.paths | native | native |  |
| trigger.filter.tags | native | native |  |
| trigger.manual | native | native |  |
| trigger.pipeline_done | native | native | workflow_run |
| trigger.pull_request | native | native | (the ref is refs/pull/N/head, not the merge ref) |
| trigger.push | native | native |  |
| trigger.resource | lost | lost | (GitHub Actions has no resource triggers) |
| trigger.schedule | native | native |  |
| trigger.tag | native | native |  |

### Forgejo

| Feature | Gha | Forgejo | Strategy |
| --- | --- | --- | --- |
| config.name | native | native | (a name the target rejects is external; create it under another name) |
| display.name | native | native |  |
| expr.opaque | native | native | (only opaque expressions of the gha dialect; others are lost) |
| ext.passthrough | native | native |  |
| job.allow_failure | native | native |  |
| job.artifacts | emulated | emulated | upload-artifact step at job end |
| job.cache | emulated | emulated | actions/cache step |
| job.container | native | native |  |
| job.env | native | native |  |
| job.environment | native | lost | (Forgejo does not document environment) |
| job.environment.approval | external | external | configure required reviewers on the environment in repository settings |
| job.if | native | native |  |
| job.manual | emulated | emulated | workflow_dispatch trigger |
| job.matrix | native | native |  |
| job.needs | native | native |  |
| job.needs.artifacts | emulated | emulated | download-artifact step at job start |
| job.outputs | native | native |  |
| job.retry | emulated | emulated | step-level retry loop via shell (Retry.Attempts is the total number of runs, so GitLab retry: N is Attempts N+1) |
| job.runner | native | native |  |
| job.services | native | native |  |
| job.timeout | native | native |  |
| pipeline.concurrency | native | native |  |
| pipeline.env | native | native |  |
| pipeline.finally | emulated | emulated | job with if: always() that needs every other job |
| pipeline.inputs | native | native |  |
| pipeline.permissions | native | lost | (Forgejo does not document permissions; the token has write access except on fork pull requests) |
| project.multi_pipeline | native | native |  |
| script.interpolation | native | native |  |
| step.action | native | native |  |
| step.continue_on_error | native | native |  |
| step.env | native | native |  |
| step.if | native | native |  |
| step.image | emulated | emulated | uses: docker://<image> with entrypoint and args |
| step.script | native | native |  |
| step.shell | native | native |  |
| step.timeout | native | native |  |
| step.workdir | native | native |  |
| trigger.api | native | native |  |
| trigger.filter.branches | native | native |  |
| trigger.filter.paths | native | native |  |
| trigger.filter.tags | native | native |  |
| trigger.manual | native | native |  |
| trigger.pipeline_done | native | native | workflow_run |
| trigger.pull_request | native | native |  |
| trigger.push | native | native |  |
| trigger.resource | lost | lost | (GitHub Actions has no resource triggers) |
| trigger.schedule | native | native |  |
| trigger.tag | native | native |  |

## Feature support from GHA to GitLab

The following table comes from `pipemaster caps --from gha --to gitlab --format md`. The first verdict column is the source provider. The second is the target provider. The last column is the target strategy.

| Feature | Gha | Gitlab | Strategy |
| --- | --- | --- | --- |
| config.name | native | native |  |
| display.name | native | lost | (step and pipeline names have no GitLab field; a job name survives only as its key) |
| expr.opaque | native | native | (only opaque expressions of the gitlab dialect; others are lost) |
| ext.passthrough | native | native |  |
| job.allow_failure | native | native |  |
| job.artifacts | emulated | native |  |
| job.cache | emulated | native |  |
| job.container | native | native |  |
| job.env | native | native |  |
| job.environment | native | native |  |
| job.environment.approval | external | external | protected environment approval rules in project settings |
| job.if | native | native |  |
| job.manual | emulated | native |  |
| job.matrix | native | native | parallel:matrix (include and exclude are expanded into explicit entries) |
| job.needs | native | native |  |
| job.needs.artifacts | emulated | native |  |
| job.outputs | native | emulated | dotenv artifact report |
| job.retry | emulated | native |  |
| job.runner | native | native |  |
| job.services | native | native |  |
| job.timeout | native | native |  |
| pipeline.concurrency | native | emulated | resource_group on every job (queues, does not cancel) |
| pipeline.env | native | native |  |
| pipeline.finally | emulated | native |  |
| pipeline.inputs | native | emulated | variables with description (input types and required flags are not enforced) |
| pipeline.permissions | native | lost | (job token scope is a project setting) |
| project.multi_pipeline | native | emulated | jobs of every pipeline in one file under <pipeline>:<job> keys (each job carries its pipeline's trigger condition; stage names and inputs are not restored) |
| script.interpolation | native | native |  |
| step.action | native | native | per actions.Lookup verdict (unknown actions are lost) |
| step.continue_on_error | native | emulated | \|\| true after each line of the step |
| step.env | native | emulated | export lines inside a subshell around the step |
| step.if | native | emulated | shell guard around the step's lines (step reports success instead of skipped) |
| step.image | emulated | lost | (GitLab sets the image per job, not per step) |
| step.script | native | native |  |
| step.shell | native | lost | (GitLab runs scripts in the runner's configured shell) |
| step.timeout | native | lost | (GitLab has only a job timeout) |
| step.workdir | native | emulated | cd in a subshell around the step |
| trigger.api | native | external | pipeline trigger token or API call |
| trigger.filter.branches | native | native |  |
| trigger.filter.paths | native | native |  |
| trigger.filter.tags | native | native |  |
| trigger.manual | native | native |  |
| trigger.pipeline_done | native | lost | (a GitLab downstream pipeline starts from a trigger job in the upstream project) |
| trigger.pull_request | native | native |  |
| trigger.push | native | native |  |
| trigger.resource | lost | lost | (GitLab CI has no resource triggers) |
| trigger.schedule | native | external | pipeline schedule in project settings (GitLab keeps the cron string outside the CI file) |
| trigger.tag | native | native |  |

## Feature support from GHA to Woodpecker

The following table comes from `pipemaster caps --from gha --to woodpecker --format md`. The columns follow the GitLab table.

| Feature | Gha | Woodpecker | Strategy |
| --- | --- | --- | --- |
| config.name | native | native |  |
| display.name | native | native | the step name (pipeline and job names that differ from the id are lost; the workflow name is the file name) |
| expr.opaque | native | native | (only opaque expressions of the woodpecker dialect; others are lost) |
| ext.passthrough | native | native |  |
| job.allow_failure | native | emulated | failure ignore on every step (later steps still run after a failed step) |
| job.artifacts | emulated | emulated | shared workspace of the steps in one workflow (other workflows cannot read the files) |
| job.cache | emulated | emulated | shared workspace of the steps in one workflow (nothing persists between runs) |
| job.container | native | native |  |
| job.env | native | emulated | copied into the environment of every step (a step variable of the same name wins) |
| job.environment | native | lost | (Woodpecker has no environment name or URL in the workflow) |
| job.environment.approval | external | external | repo setting Require approval for |
| job.if | native | native | workflow when (only the translatable expression subset; any other condition disables the workflow) |
| job.manual | emulated | emulated | when event manual; a person starts the workflow from the UI or API (the workflow no longer runs on its normal events) |
| job.matrix | native | native |  |
| job.needs | native | native |  |
| job.needs.artifacts | emulated | lost | (workflows do not share a workspace) |
| job.outputs | native | lost | (Woodpecker steps cannot pass values to other workflows) |
| job.retry | emulated | lost | (Woodpecker has no retry key; restarts are manual) |
| job.runner | native | lost | (only a platform label has a Woodpecker form (labels.platform); other labels are lost) |
| job.services | native | native |  |
| job.timeout | native | external | repo setting Timeout (Woodpecker limits the whole pipeline, not a workflow) |
| pipeline.concurrency | native | native | workflow concurrency limit 1 with group (cancel-in-progress has no Woodpecker form) |
| pipeline.env | native | emulated | copied into the environment of every step (a step variable of the same name wins) |
| pipeline.finally | emulated | emulated | a workflow that depends on every other workflow and runs on status success or failure (a skipped dependency is optional, so the workflow still runs) |
| pipeline.inputs | native | lost | (Woodpecker has no typed pipeline inputs) |
| pipeline.permissions | native | lost | (token scopes are forge and repo settings) |
| project.multi_pipeline | native | emulated | each job of each pipeline is a workflow named <pipeline>-<job> (each workflow carries its pipeline's trigger condition) |
| script.interpolation | native | native |  |
| step.action | native | native | per actions.Lookup verdict; a plugin step is native (unknown actions are lost) |
| step.continue_on_error | native | native |  |
| step.env | native | native |  |
| step.if | native | native |  |
| step.image | emulated | native |  |
| step.script | native | native |  |
| step.shell | native | lost | (Woodpecker runs commands in the image entrypoint shell) |
| step.timeout | native | lost | (Woodpecker has only a pipeline timeout in the repo settings) |
| step.workdir | native | native |  |
| trigger.api | native | lost | (Woodpecker starts API pipelines as manual events) |
| trigger.filter.branches | native | native | branch filter (only exact globs are native; other patterns keep doublestar meaning) |
| trigger.filter.paths | native | native |  |
| trigger.filter.tags | native | emulated | ref filter refs/tags/<pattern> on the tag event (the glob dialect is doublestar; the parser reads it back as the tag trigger filter) |
| trigger.manual | native | native |  |
| trigger.pipeline_done | native | lost | (Woodpecker has no pipeline-completion trigger) |
| trigger.pull_request | native | native |  |
| trigger.push | native | native |  |
| trigger.resource | lost | lost | (Woodpecker CI has no resource triggers) |
| trigger.schedule | native | external | when event cron; the schedule is a repo cron job (Woodpecker keeps the cron string in the repo settings (Cron), not in the workflow file) |
| trigger.tag | native | native |  |

### Woodpecker notes

- The emitter writes one workflow file for each job in `.woodpecker/`. The parser reads the first form that exists and does not merge forms.
- Woodpecker keeps only an `os/arch` runner label (`labels.platform`). The round-trip metadata restores the other labels when the chain returns to the origin. If a later hop emits GHA, Gitea or Forgejo, the emitter writes the default label `ubuntu-latest` and reports an emulated `job.runner` entry.
- Woodpecker substitutes `${NAME}` in the raw YAML and turns `$$` into a literal `$`. Thus the emitter writes `$` in script and environment text as `$$`. The parser reverses this.
- Woodpecker filters use doublestar globs. If a GHA filter uses `+` or `?`, doublestar cannot express it. The emitter then writes an anchored regular expression in `evaluate`.
- A cron schedule is external. Woodpecker keeps the cron string in the repository settings, not in the workflow file.
- Environment approval is external. Set it with the repository setting Require approval for.
- The runner is lost. Only a platform label becomes `labels.platform`.

## Feature support from GHA to Drone

The following table comes from `pipemaster caps --from gha --to drone --format md`.

| Feature | Gha | Drone | Strategy |
| --- | --- | --- | --- |
| config.name | native | native |  |
| display.name | native | native | the pipeline and step names (a name that differs from the id is lost) |
| expr.opaque | native | native | (only opaque expressions of the drone dialect; others are lost) |
| ext.passthrough | native | native |  |
| job.allow_failure | native | emulated | failure ignore on every step (later steps still run after a failed step) |
| job.artifacts | emulated | emulated | shared workspace of the steps in one document (other documents cannot read the files) |
| job.cache | emulated | emulated | shared workspace of the steps in one document (nothing persists between runs) |
| job.container | native | native |  |
| job.env | native | native | pipeline environment |
| job.environment | native | lost | (Drone has no environment name or URL in the pipeline file) |
| job.environment.approval | external | emulated | trigger event promote with target set to the environment (a person starts the build with drone build promote) |
| job.if | native | native | pipeline trigger conditions (only the translatable expression subset; any other condition is emulated or lost) |
| job.manual | emulated | emulated | trigger event custom (a person starts the build through the API or drone build create; the pipeline no longer runs on its normal events) |
| job.matrix | native | emulated | one pipeline document per cell (fail-fast and max-parallel are lost; a failed sibling stops the others without order) |
| job.needs | native | native |  |
| job.needs.artifacts | emulated | lost | (documents share no workspace) |
| job.outputs | native | lost | (Drone steps cannot pass values to other pipelines) |
| job.retry | emulated | lost | (Drone has no retry key; only clone.retries exists) |
| job.runner | native | lost | (only the platform has a Drone form; other runner labels are lost) |
| job.services | native | native |  |
| job.timeout | native | external | repo setting timeout (the setting has no per-pipeline or per-step form) |
| pipeline.concurrency | native | lost | (Drone concurrency.limit is a count and the IR models a group and cancel-in-progress, not a limit) |
| pipeline.env | native | emulated | copied into the environment of every pipeline document (a step variable of the same name wins) |
| pipeline.finally | emulated | emulated | a document that depends on every other document and runs on trigger status success and failure (the server skips a stage whose status filter does not match the build result) |
| pipeline.inputs | native | lost | (Drone has no typed pipeline inputs) |
| pipeline.permissions | native | lost | (token scopes are server and repo settings) |
| project.multi_pipeline | native | emulated | each pipeline is a document named <pipeline>-<job> (each document carries its pipeline's trigger condition) |
| script.interpolation | native | native |  |
| step.action | native | emulated | per actions.Lookup verdict; a plugin step with settings (unknown actions are lost) |
| step.continue_on_error | native | native |  |
| step.env | native | native |  |
| step.if | native | native | step when (only the translatable expression subset; any other condition is emulated or lost) |
| step.image | emulated | native |  |
| step.script | native | native |  |
| step.shell | native | lost | (Drone compiles commands into its own set -e script) |
| step.timeout | native | lost | (the docker runner ignores a step timeout) |
| step.workdir | native | native |  |
| trigger.api | native | lost | (Drone starts API builds as event custom) |
| trigger.filter.branches | native | native | trigger branch (only exact globs are native; for a pull request Drone matches the target branch) |
| trigger.filter.paths | native | lost | (the Drone OSS server does not evaluate trigger.paths, so the emitter never writes it) |
| trigger.filter.tags | native | emulated | ref filter refs/tags/<pattern> on the tag event (the glob dialect is doublestar; the parser reads it back as the tag trigger filter, and branch is not available on tag events) |
| trigger.manual | native | emulated | trigger event custom (a person starts the build through the API or drone build create) |
| trigger.pipeline_done | native | lost | (Drone has no pipeline-completion trigger) |
| trigger.pull_request | native | native |  |
| trigger.push | native | native |  |
| trigger.resource | lost | lost | (Drone CI has no resource triggers) |
| trigger.schedule | native | external | trigger event cron with the cron job name (cron jobs are set in the UI or with drone cron add; the report names the command) |
| trigger.tag | native | native |  |

Notes on the Drone provider:

- Drone replaces `${NAME}` in the raw YAML before it parses the file. The literal `$` escape is `$$`.
- A matrix becomes one pipeline document per cell. Each document carries a marker, so a return conversion can fold the cells into one matrix job.
- The Drone OSS server does not evaluate `paths` filters. The converter reports them as lost and never emits them.

## Feature support from GHA to Tekton

The following table comes from `pipemaster caps --from gha --to tekton --format md`.

| Feature | Gha | Tekton | Strategy |
| --- | --- | --- | --- |
| config.name | native | emulated | secrets through secretKeyRef on Secret ci-secrets, vars through configMapKeyRef on ConfigMap ci-vars (the Secret values and ConfigMap values are not written) |
| display.name | native | native |  |
| expr.opaque | native | native | (only opaque expressions of the tekton dialect; a cel entry from a Tekton file is written back and needs enable-cel-in-whenexpression; others are lost) |
| ext.passthrough | native | native |  |
| job.allow_failure | native | native |  |
| job.artifacts | emulated | emulated | workspace on a PVC from volumeClaimTemplate (tasks on different nodes need ReadWriteMany or the affinity assistant) |
| job.cache | emulated | emulated | workspace on a PVC from volumeClaimTemplate (tasks on different nodes need ReadWriteMany or the affinity assistant) |
| job.container | native | native | stepTemplate image |
| job.env | native | native |  |
| job.environment | native | lost | (Tekton has no environment name or URL) |
| job.environment.approval | external | lost | (Tekton has no approval gate) |
| job.if | native | native | PipelineTask when (only the input/operator/values subset; any other condition is emulated or lost) |
| job.manual | emulated | lost | (Tekton has no manual job gate) |
| job.matrix | native | native | PipelineTask matrix (fail-fast and max-parallel are lost; axis values become strings) |
| job.needs | native | native |  |
| job.needs.artifacts | emulated | emulated | workspace on a PVC from volumeClaimTemplate (tasks on different nodes need ReadWriteMany or the affinity assistant) |
| job.outputs | native | native | Task results |
| job.retry | emulated | native |  |
| job.runner | native | lost | (Tekton schedules pods, not runner labels) |
| job.services | native | emulated | Task sidecars (the service is at localhost, not at its name) |
| job.timeout | native | native |  |
| pipeline.concurrency | native | lost | (Tekton has no concurrency group or cancel-in-progress) |
| pipeline.env | native | emulated | copied into the stepTemplate env of each Task (a step variable of the same name wins) |
| pipeline.finally | emulated | native | Pipeline finally (finally tasks cannot be ordered among themselves, so a needs edge between them is lost) |
| pipeline.inputs | native | native | Pipeline params |
| pipeline.permissions | native | lost | (permissions come from the ServiceAccount of the run) |
| project.multi_pipeline | native | emulated | one Pipeline document per IR pipeline (each pipeline gets its own trigger) |
| script.interpolation | native | native |  |
| step.action | native | emulated | StepAction ref per actions.Lookup verdict (unknown actions are lost) |
| step.continue_on_error | native | native |  |
| step.env | native | native |  |
| step.if | native | native | step when (only the input/operator/values subset; any other condition is emulated or lost) |
| step.image | emulated | native |  |
| step.script | native | native |  |
| step.shell | native | native | shebang line of the script |
| step.timeout | native | native |  |
| step.workdir | native | native |  |
| trigger.api | native | emulated | EventListener entry of the Tekton file (only an entry that a Tekton file held is written; any other API trigger is lost) |
| trigger.filter.branches | native | emulated | CEL interceptor filter on the ref (the EventListener serves GitHub webhooks only) |
| trigger.filter.paths | native | emulated | CEL interceptor filter on the changed files (the github interceptor needs addChangedFiles; the EventListener serves GitHub webhooks only) |
| trigger.filter.tags | native | emulated | CEL interceptor filter on the ref (the EventListener serves GitHub webhooks only) |
| trigger.manual | native | external | tkn pipeline start <name> (a person starts the run; the report names the command) |
| trigger.pipeline_done | native | lost | (Tekton has no pipeline-completion trigger) |
| trigger.pull_request | native | emulated | EventListener trigger with the github interceptor (the EventListener serves GitHub webhooks only; only pull requests from this repository start a run, because a fork would run its code with every secret) |
| trigger.push | native | emulated | EventListener trigger with the github interceptor (the EventListener serves GitHub webhooks only) |
| trigger.resource | lost | lost | (Tekton has no resource trigger) |
| trigger.schedule | native | emulated | CronJob that posts to the EventListener (the CronJob and the listener must run in the same cluster; the schedule listener has no signature check, so do not expose it) |
| trigger.tag | native | emulated | EventListener trigger with the github interceptor (the EventListener serves GitHub webhooks only) |

Notes on the Tekton provider:

- The emitter writes the files under `.tekton/`. Each pipeline becomes one Pipeline in `pipeline.yaml`, and each job becomes a Task in `tasks/`. The triggers go to `triggers/`, the schedules to `cron/`, and the RBAC objects to `rbac/`. A `kustomization.yaml` lists the other files. The parser reads every YAML file under `.tekton/`, including subfolders.
- The context of a run (branch, tag, event, commit, repository and pull request number) travels in reserved Pipeline params that start with `ctx-`, The twelve reserved names are `ctx-branch`, `ctx-tag`, `ctx-event`, `ctx-sha`, `ctx-repo`, `ctx-actor`, `ctx-pr-number`, `ctx-ref-name`, `ctx-ref-type`, `ctx-ref`, `ctx-pr-head-ref` and `ctx-pr-base-ref`. An input that has one of these names is written as `input-<name>`, and the marker restores it on the way back. The pipeline declares only the params that its conditions or scripts use. The TriggerBinding fills them from the webhook payload.
- The trigger support is GitHub only. The EventListener uses the `github` interceptor and a `cel` interceptor, so it serves GitHub webhooks and no other forge. The generated interceptor reads the webhook secret from the Secret `pipemaster-github-webhook`, key `token`. The operator must create this Secret.
- A cron schedule becomes a CronJob. The job posts to a second EventListener, `pipemaster-schedule`, with the header `X-Pipemaster-Schedule: <pipeline>/<n>`, and the listener entry matches only that header. The webhook listener `pipemaster` does not hold schedule entries, because GitHub must reach it from outside the cluster. Do not expose `pipemaster-schedule` with an Ingress. Any pod that reaches its service can start a run, so limit it with a NetworkPolicy that admits only the CronJob pods. The post has an empty body, so every `ctx-*` param is empty, and a checkout step in that run cannot clone.
- The pull request filter admits only pull requests from the same repository (`head.repo.full_name == repository.full_name`). The code of a fork would otherwise run with every secret of the Task. Remove that term from the filter to accept forks.
- The generated RBAC objects and the CronJob URL use the namespace `default`. Deploy the listener and the CronJob in that namespace, or edit the host and the subjects.
- A secret becomes a `secretKeyRef` on the Secret `ci-secrets`. A variable becomes a `configMapKeyRef` on the ConfigMap `ci-vars`. The converter does not write the values. The operator must create both objects.
- The cluster setup is external. The operator must install Tekton Pipelines and Tekton Triggers, apply the RBAC objects in `.tekton/rbac/`, and create the Secret and the ConfigMap. The converter reports no feature for this work.
- A matrix passes only string values. The `finally` tasks run without an order among themselves, so a `needs` edge between them is lost. A step action becomes a StepAction reference.
- The validator checks the emitted files against embedded Tekton and CronJob schemas, and the tests also run `kubeconform`. The pinned versions are in `docs/versions.md`. `make linters` runs `scripts/install-linters.sh`, which installs the pinned `kubeconform` into `.tools/bin` beside the other linters.

## Feature support from GHA to Argo

The following table comes from `pipemaster caps --from gha --to argo --format md`.

| Feature | Gha | Argo | Strategy |
| --- | --- | --- | --- |
| config.name | native | emulated | secrets through secretKeyRef on Secret ci-secrets, vars through configMapKeyRef on ConfigMap ci-vars (the Secret values and ConfigMap values are not written) |
| display.name | native | native |  |
| expr.opaque | native | native | (only opaque expressions of the argo dialect; a {{= expr }} text from an Argo file is written back, others are lost) |
| ext.passthrough | native | native |  |
| job.allow_failure | native | native | DAG task continueOn failed (continueOn does not combine with depends, so the emitter writes dependencies for such a task) |
| job.artifacts | emulated | native | template outputs artifacts under /workspace (the artifact repository ConfigMap is external; artifacts save only from the container named main, one path per artifact (a second path gets a numbered name), a glob is not expanded and an expiry is lost) |
| job.cache | emulated | emulated | PVC from volumeClaimTemplates (the cache lives for one Workflow unless the PVC is shared; tasks on different nodes need ReadWriteMany) |
| job.container | native | native | image on every container of the containerSet |
| job.env | native | emulated | copied into the env of every container of the set (a step variable of the same name wins) |
| job.environment | native | lost | (Argo has no environment name or URL) |
| job.environment.approval | external | native | suspend template as a gate task (Argo has no approver identity: anyone with RBAC to resume the Workflow can approve; the reviewer list is lost) |
| job.if | native | native | DAG task when (the subset that the {{= expr }} printer writes becomes when; a status or result test becomes depends, and a task with continueOn or a need that may fail waits through depends; any other condition becomes when {{= false }} with a lost entry) |
| job.manual | emulated | native | suspend template as a gate task (anyone with RBAC to resume the Workflow can start the job) |
| job.matrix | native | emulated | DAG task withItems over the expanded cells (failFast and parallelism carry fail-fast and max-parallel only where they match; axis values become strings) |
| job.needs | native | native |  |
| job.needs.artifacts | emulated | native | DAG task arguments artifacts from the needed task (the artifact repository ConfigMap is external; artifacts load only into a container named main) |
| job.outputs | native | native | template outputs parameters read from files under /workspace/.pipemaster/outputs (outputs load only from the container named main; a value is written by a last script line of that container, and a parameter name that is not a valid name is lost) |
| job.retry | emulated | native | template retryStrategy |
| job.runner | native | lost | (Argo schedules pods; only a platform label can carry a node selector) |
| job.services | native | emulated | template sidecars (the service is at localhost, not at its name) |
| job.timeout | native | native | template timeout (the timeout counts Pending time too) |
| pipeline.concurrency | native | emulated | synchronization mutex (cancel-in-progress is lost; a second run waits for the mutex) |
| pipeline.env | native | emulated | copied into the env of every container (a step variable of the same name wins) |
| pipeline.finally | emulated | native | exit handler (hooks.exit) that runs a second dag |
| pipeline.inputs | native | native | spec arguments parameters (every input is a string parameter) |
| pipeline.permissions | native | lost | (permissions come from the ServiceAccount of the Workflow) |
| project.multi_pipeline | native | emulated | one WorkflowTemplate per IR pipeline (each pipeline gets its own trigger) |
| script.interpolation | native | native |  |
| step.action | native | emulated | container image per actions.Lookup verdict (unknown actions are lost) |
| step.continue_on_error | native | emulated | script line trap 'exit 0' EXIT with a marker comment (containerSet has no continueOn and no depends; a container that exits non-zero stops its dependents, so the trap makes the exit code zero) |
| step.env | native | native | container env |
| step.if | native | emulated | guard in the script that reads context values from env (containerSet has no when, so every step condition runs as a shell guard that reads context values from env; a condition with no guard form disables the script and the container still starts; a containerSet starts every container at once and each waits for its dependencies, and a container that exits non-zero stops its dependents) |
| step.image | emulated | native | container image |
| step.script | native | native | container args |
| step.shell | native | emulated | container command <shell> -ec (only bash, dash, zsh, ash and sh run as the container command, and the image must provide the shell; any other shell is lost and the script runs under sh) |
| step.timeout | native | lost | (containerSet has no timeout per container; the template timeout covers the whole job) |
| step.workdir | native | native | container workingDir |
| trigger.api | native | emulated | Sensor trigger kept in the Ext data, or a suspended CronWorkflow (only a trigger that an Argo file held is written back; any other API trigger is lost, and the schedule of a suspended CronWorkflow is not in the IR) |
| trigger.filter.branches | native | emulated | Sensor data filter on body.ref (the EventSource is github and serves GitHub webhooks only; a glob becomes a regex and a glob with no exact regex is lost) |
| trigger.filter.paths | native | lost | (a Sensor data filter cannot test the changed files of a push) |
| trigger.filter.tags | native | emulated | Sensor data filter on body.ref (the EventSource is github and serves GitHub webhooks only; a glob becomes a regex and a glob with no exact regex is lost) |
| trigger.manual | native | external | argo submit --from workflowtemplate/<name> (a person starts the run; the report names the command. The EventBus, the EventSource Service and Ingress, the artifact repository ConfigMap, the RBAC under .argo/rbac/, the ci-secrets Secret and the ci-vars ConfigMap values are external as well) |
| trigger.pipeline_done | native | lost | (Argo has no pipeline-completion trigger) |
| trigger.pull_request | native | emulated | EventSource and Sensor with a k8s trigger that creates a Workflow (the EventSource is github and serves GitHub webhooks only; the Sensor accepts pull requests from the same repository only, because a fork would run its code with every secret; the EventSource webhook needs Secret pipemaster-github-webhook with key token) |
| trigger.push | native | emulated | EventSource and Sensor with a k8s trigger that creates a Workflow (the EventSource is github and serves GitHub webhooks only; the Argo Events controller needs the EventBus and a ServiceAccount, and the EventSource Service needs an Ingress) |
| trigger.resource | lost | lost | (Argo has no resource trigger) |
| trigger.schedule | native | native | CronWorkflow with workflowTemplateRef |
| trigger.tag | native | emulated | EventSource and Sensor with a k8s trigger that creates a Workflow (the EventSource is github and serves GitHub webhooks only) |

Notes on the Argo provider:

- The emitter writes the files under `.argo/`. Each pipeline becomes one WorkflowTemplate in `workflows/`. The schedules go to `cron/` as CronWorkflows. The EventSource and the Sensor go to `events/eventsource.yaml` and `events/sensor.yaml`. The RBAC objects go to `rbac/`. The parser reads every YAML file under `.argo/`, including subfolders, and it skips a file that holds Helm template text.
- The entrypoint of a WorkflowTemplate is the template `main`, a `dag` with one task for each job. A plain `needs` entry becomes `dependencies`. A `needs` entry with a status test becomes `depends`. The `finally` jobs run in an exit handler (`hooks.exit`) that holds a second `dag`.
- A job becomes one `containerSet` template, and each step becomes one container. The containers share an `emptyDir` volume `workspace` that is mounted at `/workspace`, so the steps of a job see the same files. The last container is named `main` when the job has outputs, artifacts, a checkout, an upload or a download, because Argo reads these only from the container `main`.
- An action that has no container form leaves a placeholder container that runs `true`. The marker in the file restores the action on the way back. If you edit or remove the placeholder, the converter keeps your file and reports that it ignored the marker.
- The `containerSet` has no `when` and no `timeout` for a single container. A step condition becomes a shell guard that reads the context from container `env`. A step timeout is lost. The job timeout is the template timeout.
- The context of a run travels in reserved workflow parameters that start with `ctx-`. They use the same twelve names as the Tekton provider. The containers read them from `env`, and the converter never writes them into script text, because Argo substitutes `{{...}}` without escaping. A run from a CronWorkflow or from `argo submit` has an empty `ctx-repo` and an empty `ctx-sha`, so a checkout in that run cannot clone.
- The trigger support is GitHub only. A push, a pull request or a tag becomes an EventSource of type `github` and a Sensor. The Sensor holds a data filter for each trigger and a `k8s` trigger that creates a Workflow from the WorkflowTemplate. The Sensor fills the `ctx-*` parameters from the webhook payload. The EventSource reads the webhook secret from the Secret `pipemaster-github-webhook`, key `token`. The operator must create this Secret.
- The EventSource entries name the placeholder repository `OWNER/REPO`. The operator must edit it before the first run.
- The Sensor accepts a pull request only when the head repository equals the base repository. The code of a fork would otherwise run with every secret. Remove that term from the Sensor filter to accept forks.
- A secret becomes a `secretKeyRef` on the Secret `ci-secrets`. A variable becomes a `configMapKeyRef` on the ConfigMap `ci-vars`. The converter does not write the values. The operator must create both objects.
- The cluster setup is external. The operator must install Argo Workflows and Argo Events, create the EventBus, expose the EventSource Service with an Ingress, configure the artifact repository, apply the RBAC objects in `.argo/rbac/`, and create the Secret and the ConfigMap. The converter reports no feature for most of this work. The trigger.manual entry lists it.
- The validator checks the emitted files against embedded schemas from the datree CRDs-catalog. The tests also run `argo lint --offline` on the workflow kinds. The Sensor and the EventSource schemas accept any content in `spec`, so the schema check is weak for them. The pinned versions are in `docs/versions.md`. `make linters` runs `scripts/install-linters.sh`, which installs the pinned `argo` CLI into `.tools/bin` beside the other linters.

## Development

`make check` runs the tests, `golangci-lint` and the Lean checks. It runs `go test -short ./...`, and the short form runs a deterministic sample of the chain matrix. The sample keeps one chain for each middle hop of each source provider. Every provider is a source, a middle hop and a target at least once. The full matrix at eight providers takes about 200 seconds and about 1.2 GB of memory. `make chains` runs the full matrix.
