package argo

import "github.com/lukaszraczylo/pipemaster/caps"

// Caps declares what Argo does for each feature; the emitter implements every non-native verdict.
func (adapter) Caps() caps.Matrix {
	m := caps.Matrix{}
	for _, f := range caps.AllFeatures() {
		m[f] = caps.Support{Level: caps.Native}
	}
	const (
		ghOnly   = "the EventSource is github and serves GitHub webhooks only"
		artRepo  = "the artifact repository ConfigMap is external"
		setOrder = "a containerSet starts every container at once and each waits for its dependencies, and a container that exits non-zero stops its dependents"
	)
	for f, s := range map[caps.Feature]caps.Support{
		caps.ExprOpaque:             {Level: caps.Native, Caveat: "only opaque expressions of the argo dialect; a {{= expr }} text from an Argo file is written back, others are lost"},
		caps.TriggerSchedule:        {Level: caps.Native, Strategy: "CronWorkflow with workflowTemplateRef"},
		caps.JobIf:                  {Level: caps.Native, Strategy: "DAG task when", Caveat: "the subset that the {{= expr }} printer writes becomes when; a status or result test becomes depends, and a task with continueOn or a need that may fail waits through depends; any other condition becomes when {{= false }} with a lost entry"},
		caps.JobContainer:           {Level: caps.Native, Strategy: "image on every container of the containerSet"},
		caps.JobTimeout:             {Level: caps.Native, Strategy: "template timeout", Caveat: "the timeout counts Pending time too"},
		caps.JobRetry:               {Level: caps.Native, Strategy: "template retryStrategy"},
		caps.JobAllowFailure:        {Level: caps.Native, Strategy: "DAG task continueOn failed", Caveat: "continueOn does not combine with depends, so the emitter writes dependencies for such a task"},
		caps.JobOutputs:             {Level: caps.Native, Strategy: "template outputs parameters read from files under /workspace/.pipemaster/outputs", Caveat: "outputs load only from the container named main; a value is written by a last script line of that container, and a parameter name that is not a valid name is lost"},
		caps.JobArtifacts:           {Level: caps.Native, Strategy: "template outputs artifacts under /workspace", Caveat: artRepo + "; artifacts save only from the container named main, one path per artifact (a second path gets a numbered name), a glob is not expanded and an expiry is lost"},
		caps.JobNeedsArtifacts:      {Level: caps.Native, Strategy: "DAG task arguments artifacts from the needed task", Caveat: artRepo + "; artifacts load only into a container named main"},
		caps.JobEnvironmentApproval: {Level: caps.Native, Strategy: "suspend template as a gate task", Caveat: "Argo has no approver identity: anyone with RBAC to resume the Workflow can approve; the reviewer list is lost"},
		caps.JobManual:              {Level: caps.Native, Strategy: "suspend template as a gate task", Caveat: "anyone with RBAC to resume the Workflow can start the job"},
		caps.PipelineInputs:         {Level: caps.Native, Strategy: "spec arguments parameters", Caveat: "every input is a string parameter"},
		caps.PipelineFinally:        {Level: caps.Native, Strategy: "exit handler (hooks.exit) that runs a second dag"},
		caps.StepScript:             {Level: caps.Native, Strategy: "container args"},
		caps.StepImage:              {Level: caps.Native, Strategy: "container image"},
		caps.StepEnv:                {Level: caps.Native, Strategy: "container env"},
		caps.StepWorkdir:            {Level: caps.Native, Strategy: "container workingDir"},
		caps.DisplayName:            {Level: caps.Native},
		caps.TriggerPush:            {Level: caps.Emulated, Strategy: "EventSource and Sensor with a k8s trigger that creates a Workflow", Caveat: ghOnly + "; the Argo Events controller needs the EventBus and a ServiceAccount, and the EventSource Service needs an Ingress"},
		caps.TriggerPullRequest:     {Level: caps.Emulated, Strategy: "EventSource and Sensor with a k8s trigger that creates a Workflow", Caveat: ghOnly + "; the Sensor accepts pull requests from the same repository only, because a fork would run its code with every secret; the EventSource webhook needs Secret pipemaster-github-webhook with key token"},
		caps.TriggerTag:             {Level: caps.Emulated, Strategy: "EventSource and Sensor with a k8s trigger that creates a Workflow", Caveat: ghOnly},
		caps.TriggerFilterBranch:    {Level: caps.Emulated, Strategy: "Sensor data filter on body.ref", Caveat: ghOnly + "; a glob becomes a regex and a glob with no exact regex is lost"},
		caps.TriggerFilterTags:      {Level: caps.Emulated, Strategy: "Sensor data filter on body.ref", Caveat: ghOnly + "; a glob becomes a regex and a glob with no exact regex is lost"},
		caps.TriggerAPI:             {Level: caps.Emulated, Strategy: "Sensor trigger kept in the Ext data, or a suspended CronWorkflow", Caveat: "only a trigger that an Argo file held is written back; any other API trigger is lost, and the schedule of a suspended CronWorkflow is not in the IR"},
		caps.JobMatrix:              {Level: caps.Emulated, Strategy: "DAG task withItems over the expanded cells", Caveat: "failFast and parallelism carry fail-fast and max-parallel only where they match; axis values become strings"},
		caps.JobServices:            {Level: caps.Emulated, Strategy: "template sidecars", Caveat: "the service is at localhost, not at its name"},
		caps.JobCache:               {Level: caps.Emulated, Strategy: "PVC from volumeClaimTemplates", Caveat: "the cache lives for one Workflow unless the PVC is shared; tasks on different nodes need ReadWriteMany"},
		caps.JobEnv:                 {Level: caps.Emulated, Strategy: "copied into the env of every container of the set", Caveat: "a step variable of the same name wins"},
		caps.PipelineEnv:            {Level: caps.Emulated, Strategy: "copied into the env of every container", Caveat: "a step variable of the same name wins"},
		caps.PipelineConcurrency:    {Level: caps.Emulated, Strategy: "synchronization mutex", Caveat: "cancel-in-progress is lost; a second run waits for the mutex"},
		caps.StepIf:                 {Level: caps.Emulated, Strategy: "guard in the script that reads context values from env", Caveat: "containerSet has no when, so every step condition runs as a shell guard that reads context values from env; a condition with no guard form disables the script and the container still starts; " + setOrder},
		caps.StepContinueOnError:    {Level: caps.Emulated, Strategy: "script line trap 'exit 0' EXIT with a marker comment", Caveat: "containerSet has no continueOn and no depends; a container that exits non-zero stops its dependents, so the trap makes the exit code zero"},
		caps.StepShell:              {Level: caps.Emulated, Strategy: "container command <shell> -ec", Caveat: "only bash, dash, zsh, ash and sh run as the container command, and the image must provide the shell; any other shell is lost and the script runs under sh; a step with no shell from a GHA or GitLab source runs bash -e when the image has bash, else sh -e, so bash-only syntax fails under sh"},
		caps.StepAction:             {Level: caps.Emulated, Strategy: "container image per actions.Lookup verdict", Caveat: "unknown actions are lost"},
		caps.ConfigName:             {Level: caps.Emulated, Strategy: "secrets through secretKeyRef on Secret ci-secrets, vars through configMapKeyRef on ConfigMap ci-vars", Caveat: "the Secret values and ConfigMap values are not written"},
		caps.ProjectMultiPipeline:   {Level: caps.Emulated, Strategy: "one WorkflowTemplate per IR pipeline", Caveat: "each pipeline gets its own trigger"},
		caps.TriggerManual:          {Level: caps.External, Strategy: "argo submit --from workflowtemplate/<name>", Caveat: "a person starts the run; the report names the command. The EventBus, the EventSource Service and Ingress, the artifact repository ConfigMap, the RBAC under .argo/rbac/, the ci-secrets Secret and the ci-vars ConfigMap values are external as well"},
		caps.TriggerFilterPaths:     {Level: caps.Lost, Caveat: "a Sensor data filter cannot test the changed files of a push"},
		caps.JobRunner:              {Level: caps.Lost, Caveat: "Argo schedules pods; only a platform label can carry a node selector"},
		caps.JobEnvironment:         {Level: caps.Lost, Caveat: "Argo has no environment name or URL"},
		caps.PipelinePermissions:    {Level: caps.Lost, Caveat: "permissions come from the ServiceAccount of the Workflow"},
		caps.StepTimeout:            {Level: caps.Lost, Caveat: "containerSet has no timeout per container; the template timeout covers the whole job"},
		caps.TriggerPipelineDone:    {Level: caps.Lost, Caveat: "Argo has no pipeline-completion trigger"},
		caps.TriggerResource:        {Level: caps.Lost, Caveat: "Argo has no resource trigger"},
	} {
		m[f] = s
	}
	return m
}
