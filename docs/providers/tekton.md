# Tekton Pipelines and Triggers

The provider ID is `tekton`. `docs/versions.md` pins Tekton Pipelines v1.18.0 and Tekton Triggers v0.38.0. pipemaster reads `.yaml` and `.yml` files in `.tekton`, including its subfolders. It writes the files under `.tekton/`.

## Setup outside the pipeline files

The caps table gives the verdict external for `trigger.manual`. A person starts the run with `tkn pipeline start <name>`, and the report names the command. The cluster needs more work:

- A secret becomes a `secretKeyRef` on the Secret `ci-secrets`. A variable becomes a `configMapKeyRef` on the ConfigMap `ci-vars`. The converter does not write the values. The operator must create both objects.
- The cluster setup is external. The operator must install Tekton Pipelines and Tekton Triggers, apply the RBAC objects in `.tekton/rbac/`, and create the Secret and the ConfigMap. The converter reports no feature for this work.

## Conversion notes

- The emitter writes the files under `.tekton/`. Each pipeline becomes one Pipeline in `pipeline.yaml`, and each job becomes a Task in `tasks/`. The triggers go to `triggers/`, the schedules to `cron/`, and the RBAC objects to `rbac/`. A `kustomization.yaml` lists the other files. The parser reads every YAML file under `.tekton/`, including subfolders.
- The context of a run (branch, tag, event, commit, repository and pull request number) travels in reserved Pipeline params that start with `ctx-`. The twelve reserved names are `ctx-branch`, `ctx-tag`, `ctx-event`, `ctx-sha`, `ctx-repo`, `ctx-actor`, `ctx-pr-number`, `ctx-ref-name`, `ctx-ref-type`, `ctx-ref`, `ctx-pr-head-ref` and `ctx-pr-base-ref`. An input that has one of these names is written as `input-<name>`, and the marker restores it on the way back. The pipeline declares only the params that its conditions or scripts use. The TriggerBinding fills them from the webhook payload.
- The trigger support is GitHub only. The EventListener uses the `github` interceptor and a `cel` interceptor, so it serves GitHub webhooks and no other forge. The generated interceptor reads the webhook secret from the Secret `pipemaster-github-webhook`, key `token`. The operator must create this Secret.
- A cron schedule becomes a CronJob. The job posts to a second EventListener, `pipemaster-schedule`, with the header `X-Pipemaster-Schedule: <pipeline>/<n>`, and the listener entry matches only that header. The webhook listener `pipemaster` does not hold schedule entries, because GitHub must reach it from outside the cluster. Do not expose `pipemaster-schedule` with an Ingress. Any pod that reaches its service can start a run, so limit it with a NetworkPolicy that admits only the CronJob pods. The post has an empty body, so every `ctx-*` param is empty, and a checkout step in that run cannot clone.
- The pull request filter admits only pull requests from the same repository (`head.repo.full_name == repository.full_name`). The code of a fork would otherwise run with every secret of the Task. Remove that term from the filter to accept forks.
- The generated RBAC objects and the CronJob URL use the namespace `default`. Deploy the listener and the CronJob in that namespace, or edit the host and the subjects.
- A matrix passes only string values. The `finally` tasks run without an order among themselves, so a `needs` edge between them is lost. A step action becomes a StepAction reference.
- The validator checks the emitted files against embedded Tekton and CronJob schemas, and the tests also run `kubeconform`. The pinned versions are in `docs/versions.md`. `make linters` runs `scripts/install-linters.sh`, which installs the pinned `kubeconform` into `.tools/bin` beside the other linters.

## Feature support

The following table comes from `pipemaster caps --to tekton --format md`.

| Feature | Tekton | Strategy |
| --- | --- | --- |
| config.name | emulated | secrets through secretKeyRef on Secret ci-secrets, vars through configMapKeyRef on ConfigMap ci-vars (the Secret values and ConfigMap values are not written) |
| display.name | native |  |
| expr.opaque | native | (only opaque expressions of the tekton dialect; a cel entry from a Tekton file is written back and needs enable-cel-in-whenexpression; others are lost) |
| ext.passthrough | native |  |
| job.allow_failure | native |  |
| job.artifacts | emulated | workspace on a PVC from volumeClaimTemplate (tasks on different nodes need ReadWriteMany or the affinity assistant) |
| job.cache | emulated | workspace on a PVC from volumeClaimTemplate (tasks on different nodes need ReadWriteMany or the affinity assistant) |
| job.container | native | stepTemplate image |
| job.env | native |  |
| job.environment | lost | (Tekton has no environment name or URL) |
| job.environment.approval | lost | (Tekton has no approval gate) |
| job.if | native | PipelineTask when (only the input/operator/values subset; any other condition is emulated or lost) |
| job.manual | lost | (Tekton has no manual job gate) |
| job.matrix | native | PipelineTask matrix (fail-fast and max-parallel are lost; axis values become strings) |
| job.needs | native |  |
| job.needs.artifacts | emulated | workspace on a PVC from volumeClaimTemplate (tasks on different nodes need ReadWriteMany or the affinity assistant) |
| job.outputs | native | Task results |
| job.retry | native |  |
| job.runner | external | workers the operator provides (each runner label becomes a taskRunSpecs podTemplate nodeSelector key runner.pipemaster.io/<label>=true in the generated TriggerTemplate; label the nodes to match) |
| job.services | emulated | Task sidecars (the service is at localhost, not at its name) |
| job.timeout | native |  |
| pipeline.concurrency | lost | (Tekton has no concurrency group or cancel-in-progress) |
| pipeline.env | emulated | copied into the stepTemplate env of each Task (a step variable of the same name wins) |
| pipeline.finally | native | Pipeline finally (finally tasks cannot be ordered among themselves, so a needs edge between them is lost) |
| pipeline.inputs | native | Pipeline params |
| pipeline.permissions | lost | (permissions come from the ServiceAccount of the run) |
| project.multi_pipeline | emulated | one Pipeline document per IR pipeline (each pipeline gets its own trigger) |
| script.interpolation | native |  |
| step.action | emulated | StepAction ref per actions.Lookup verdict (unknown actions are lost) |
| step.continue_on_error | native |  |
| step.env | native |  |
| step.if | native | step when (only the input/operator/values subset; any other condition is emulated or lost) |
| step.image | native |  |
| step.script | native |  |
| step.shell | native | shebang line of the script (a step with no shell from a GHA or GitLab source gets a preamble that re-runs the script under bash -e when the image has bash, else it runs under sh -e, so bash-only syntax fails under sh) |
| step.timeout | native |  |
| step.workdir | native |  |
| trigger.api | emulated | EventListener entry of the Tekton file (only an entry that a Tekton file held is written; any other API trigger is lost) |
| trigger.filter.branches | emulated | CEL interceptor filter on the ref (the EventListener serves GitHub webhooks only) |
| trigger.filter.paths | emulated | CEL interceptor filter on the changed files (the github interceptor needs addChangedFiles; the EventListener serves GitHub webhooks only) |
| trigger.filter.tags | emulated | CEL interceptor filter on the ref (the EventListener serves GitHub webhooks only) |
| trigger.manual | external | tkn pipeline start <name> (a person starts the run; the report names the command) |
| trigger.pipeline_done | lost | (Tekton has no pipeline-completion trigger) |
| trigger.pull_request | emulated | EventListener trigger with the github interceptor (the EventListener serves GitHub webhooks only; only pull requests from this repository start a run, because a fork would run its code with every secret) |
| trigger.push | emulated | EventListener trigger with the github interceptor (the EventListener serves GitHub webhooks only) |
| trigger.resource | lost | (Tekton has no resource trigger) |
| trigger.schedule | emulated | CronJob that posts to the EventListener (the CronJob and the listener must run in the same cluster; the schedule listener has no signature check, so do not expose it) |
| trigger.tag | emulated | EventListener trigger with the github interceptor (the EventListener serves GitHub webhooks only) |
