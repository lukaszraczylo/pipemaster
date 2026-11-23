# Argo Workflows and Argo Events

The provider ID is `argo`. `docs/versions.md` pins Argo Workflows v4.1.6, Argo Events v1.9.11 and the argo CLI v4.1.6. pipemaster reads `.yaml` and `.yml` files in `.argo`, including its subfolders. It writes the files under `.argo/`.

## Setup outside the pipeline files

The caps table gives the verdict external for `trigger.manual`. The same table row also lists cluster work. The operator completes these items:

- `trigger.manual`: start the run with `argo submit --from workflowtemplate/<name>`. The report names the command.
- Repository: edit the placeholder repository `OWNER/REPO` in the EventSource entries before the first run.
- Secret and variable: create the Secret `ci-secrets` and the ConfigMap `ci-vars`. A secret becomes a `secretKeyRef` on the Secret. A variable becomes a `configMapKeyRef` on the ConfigMap. The converter does not write the values.
- Cluster: install Argo Workflows and Argo Events, create the EventBus, and expose the EventSource Service with an Ingress. Configure the artifact repository and apply the RBAC objects in `.argo/rbac/`.

## Conversion notes

- The emitter writes the files under `.argo/`. Each pipeline becomes one WorkflowTemplate in `workflows/`. The schedules go to `cron/` as CronWorkflows. The EventSource and the Sensor go to `events/eventsource.yaml` and `events/sensor.yaml`. The RBAC objects go to `rbac/`. The parser reads every YAML file under `.argo/`, including subfolders, and it skips a file that holds Helm template text.
- The entrypoint of a WorkflowTemplate is the template `main`, a `dag` with one task for each job. A plain `needs` entry becomes `dependencies`. A `needs` entry with a status test becomes `depends`. The `finally` jobs run in an exit handler (`hooks.exit`) that holds a second `dag`.
- A job becomes one `containerSet` template, and each step becomes one container. The containers share an `emptyDir` volume `workspace` that is mounted at `/workspace`, so the steps of a job see the same files. The last container is named `main` when the job has outputs, artifacts, a checkout, an upload or a download. Argo reads these only from the container `main`.
- An action that has no container form leaves a placeholder container that runs `true`. The marker in the file restores the action on the way back. If you edit or remove the placeholder, the converter keeps your file and reports that it ignored the marker.
- The `containerSet` has no `when` and no `timeout` for a single container. A step condition becomes a shell guard that reads the context from container `env`. A step timeout is lost. The job timeout is the template timeout.
- The context of a run travels in reserved workflow parameters that start with `ctx-`. They use the same twelve names as the Tekton provider. The containers read them from `env`, and the converter never writes them into script text, because Argo substitutes `{{...}}` without escaping. A run from a CronWorkflow or from `argo submit` has an empty `ctx-repo` and an empty `ctx-sha`, so a checkout in that run cannot clone.
- The trigger support is GitHub only. A push, a pull request or a tag becomes an EventSource of type `github` and a Sensor. The Sensor holds a data filter for each trigger and a `k8s` trigger that creates a Workflow from the WorkflowTemplate. The Sensor fills the `ctx-*` parameters from the webhook payload. The EventSource reads the webhook secret from the Secret `pipemaster-github-webhook`, key `token`. The operator must create this Secret.
- The Sensor accepts a pull request only when the head repository equals the base repository. The code of a fork would otherwise run with every secret. Remove that term from the Sensor filter to accept forks.
- The validator checks the emitted files against embedded schemas from the datree CRDs-catalog. The tests also run `argo lint --offline` on the workflow kinds. The Sensor and the EventSource schemas accept any content in `spec`, so the schema check is weak for them. The pinned versions are in `docs/versions.md`. `make linters` runs `scripts/install-linters.sh`, which installs the pinned `argo` CLI into `.tools/bin` beside the other linters.

## Feature support

The following table comes from `pipemaster caps --to argo --format md`.

| Feature | Argo | Strategy |
| --- | --- | --- |
| config.name | emulated | secrets through secretKeyRef on Secret ci-secrets, vars through configMapKeyRef on ConfigMap ci-vars (the Secret values and ConfigMap values are not written) |
| display.name | native |  |
| expr.opaque | native | (only opaque expressions of the argo dialect; a {{= expr }} text from an Argo file is written back, others are lost) |
| ext.passthrough | native |  |
| job.allow_failure | native | DAG task continueOn failed (continueOn does not combine with depends, so the emitter writes dependencies for such a task) |
| job.artifacts | native | template outputs artifacts under /workspace (the artifact repository ConfigMap is external; artifacts save only from the container named main, one path per artifact (a second path gets a numbered name), a glob is not expanded and an expiry is lost) |
| job.cache | emulated | PVC from volumeClaimTemplates (the cache lives for one Workflow unless the PVC is shared; tasks on different nodes need ReadWriteMany) |
| job.container | native | image on every container of the containerSet |
| job.env | emulated | copied into the env of every container of the set (a step variable of the same name wins) |
| job.environment | lost | (Argo has no environment name or URL) |
| job.environment.approval | native | suspend template as a gate task (Argo has no approver identity: anyone with RBAC to resume the Workflow can approve; the reviewer list is lost) |
| job.if | native | DAG task when (the subset that the {{= expr }} printer writes becomes when; a status or result test becomes depends, and a task with continueOn or a need that may fail waits through depends; any other condition becomes when {{= false }} with a lost entry) |
| job.manual | native | suspend template as a gate task (anyone with RBAC to resume the Workflow can start the job) |
| job.matrix | emulated | DAG task withItems over the expanded cells (failFast and parallelism carry fail-fast and max-parallel only where they match; axis values become strings) |
| job.needs | native |  |
| job.needs.artifacts | native | DAG task arguments artifacts from the needed task (the artifact repository ConfigMap is external; artifacts load only into a container named main) |
| job.outputs | native | template outputs parameters read from files under /workspace/.pipemaster/outputs (outputs load only from the container named main; a value is written by a last script line of that container, and a parameter name that is not a valid name is lost) |
| job.retry | native | template retryStrategy |
| job.runner | lost | (Argo schedules pods; only a platform label can carry a node selector) |
| job.services | emulated | template sidecars (the service is at localhost, not at its name) |
| job.timeout | native | template timeout (the timeout counts Pending time too) |
| pipeline.concurrency | emulated | synchronization mutex (cancel-in-progress is lost; a second run waits for the mutex) |
| pipeline.env | emulated | copied into the env of every container (a step variable of the same name wins) |
| pipeline.finally | native | exit handler (hooks.exit) that runs a second dag |
| pipeline.inputs | native | spec arguments parameters (every input is a string parameter) |
| pipeline.permissions | lost | (permissions come from the ServiceAccount of the Workflow) |
| project.multi_pipeline | emulated | one WorkflowTemplate per IR pipeline (each pipeline gets its own trigger) |
| script.interpolation | native |  |
| step.action | emulated | container image per actions.Lookup verdict (unknown actions are lost) |
| step.continue_on_error | emulated | script line trap 'exit 0' EXIT with a marker comment (containerSet has no continueOn and no depends; a container that exits non-zero stops its dependents, so the trap makes the exit code zero) |
| step.env | native | container env |
| step.if | emulated | guard in the script that reads context values from env (containerSet has no when, so every step condition runs as a shell guard that reads context values from env; a condition with no guard form disables the script and the container still starts; a containerSet starts every container at once and each waits for its dependencies, and a container that exits non-zero stops its dependents) |
| step.image | native | container image |
| step.script | native | container args |
| step.shell | emulated | container command <shell> -ec (only bash, dash, zsh, ash and sh run as the container command, and the image must provide the shell; any other shell is lost and the script runs under sh) |
| step.timeout | lost | (containerSet has no timeout per container; the template timeout covers the whole job) |
| step.workdir | native | container workingDir |
| trigger.api | emulated | Sensor trigger kept in the Ext data, or a suspended CronWorkflow (only a trigger that an Argo file held is written back; any other API trigger is lost, and the schedule of a suspended CronWorkflow is not in the IR) |
| trigger.filter.branches | emulated | Sensor data filter on body.ref (the EventSource is github and serves GitHub webhooks only; a glob becomes a regex and a glob with no exact regex is lost) |
| trigger.filter.paths | lost | (a Sensor data filter cannot test the changed files of a push) |
| trigger.filter.tags | emulated | Sensor data filter on body.ref (the EventSource is github and serves GitHub webhooks only; a glob becomes a regex and a glob with no exact regex is lost) |
| trigger.manual | external | argo submit --from workflowtemplate/<name> (a person starts the run; the report names the command. The EventBus, the EventSource Service and Ingress, the artifact repository ConfigMap, the RBAC under .argo/rbac/, the ci-secrets Secret and the ci-vars ConfigMap values are external as well) |
| trigger.pipeline_done | lost | (Argo has no pipeline-completion trigger) |
| trigger.pull_request | emulated | EventSource and Sensor with a k8s trigger that creates a Workflow (the EventSource is github and serves GitHub webhooks only; the Sensor accepts pull requests from the same repository only, because a fork would run its code with every secret; the EventSource webhook needs Secret pipemaster-github-webhook with key token) |
| trigger.push | emulated | EventSource and Sensor with a k8s trigger that creates a Workflow (the EventSource is github and serves GitHub webhooks only; the Argo Events controller needs the EventBus and a ServiceAccount, and the EventSource Service needs an Ingress) |
| trigger.resource | lost | (Argo has no resource trigger) |
| trigger.schedule | native | CronWorkflow with workflowTemplateRef |
| trigger.tag | emulated | EventSource and Sensor with a k8s trigger that creates a Workflow (the EventSource is github and serves GitHub webhooks only) |
