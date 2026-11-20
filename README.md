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
