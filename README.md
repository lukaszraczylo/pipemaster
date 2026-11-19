# pipemaster

pipemaster is a Go command-line tool that converts CI/CD pipelines between providers through a shared intermediate representation. It reports for every feature whether the conversion is native, emulated, external or lost, so nothing is dropped silently.

Status: pre-alpha.

## Supported providers

The tool supports two providers. The provider ID is the value that you pass to `--from`, `--to`, `--via` and `--as`.

| ID | Provider | Pipeline files |
| --- | --- | --- |
| `gha` | GitHub Actions | `.yml` and `.yaml` files in `.github/workflows` |
| `gitlab` | GitLab CI | `.gitlab-ci.yml` and its local includes |

No other provider is available.

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

## Reading the loss report

The report gives one verdict for each feature that the pipeline uses. The verdicts, from best to worst, are:

- `native`: the target has the same feature.
- `emulated`: the target gets an equivalent built from other features. The converted file marks it with a `# pipemaster:emulated` comment.
- `external`: the target must configure the feature outside the pipeline file.
- `lost`: the target cannot express the feature.

## Feature support from GHA to GitLab

The following table comes from `pipemaster caps --from gha --to gitlab --format md`. The first verdict column is the source provider. The second is the target provider. The last column is the target strategy.

| Feature | Gha | Gitlab | Strategy |
| --- | --- | --- | --- |
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
