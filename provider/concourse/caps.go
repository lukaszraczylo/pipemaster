package concourse

import "github.com/lukaszraczylo/pipemaster/caps"

// Caps declares what Concourse does for each feature; the emitter implements every non-native verdict.
func (adapter) Caps() caps.Matrix {
	m := caps.Matrix{}
	for _, f := range caps.AllFeatures() {
		m[f] = caps.Support{Level: caps.Native}
	}
	const (
		triggerSet = "a job whose get steps carry different trigger sets keeps the set in Ext[concourse][triggers], and a job whose get steps do not trigger is started by a person"
		taskImage  = "image_resource on every task"
		noExpr     = "Concourse has no expression language"
	)
	for f, s := range map[caps.Feature]caps.Support{
		caps.TriggerPush:            {Level: caps.Native, Strategy: "git resource get with trigger: true", Caveat: "a git resource follows one branch, so a push trigger with two exact branches is two resources and the job runs once per branch event"},
		caps.TriggerFilterBranch:    {Level: caps.Native, Strategy: "source.branch of the git resource", Caveat: "one exact branch per resource; a glob is lost"},
		caps.TriggerFilterPaths:     {Level: caps.Native, Strategy: "source.paths and source.ignore_paths of the git resource"},
		caps.TriggerManual:          {Level: caps.Native, Strategy: "job with no triggering get"},
		caps.JobNeeds:               {Level: caps.Native, Strategy: "passed on the get of the shared source resource", Caveat: "passed lists are an AND"},
		caps.JobContainer:           {Level: caps.Native, Strategy: taskImage},
		caps.StepScript:             {Level: caps.Native, Strategy: "task config run.path and run.args"},
		caps.StepImage:              {Level: caps.Native, Strategy: "task config image_resource"},
		caps.StepEnv:                {Level: caps.Native, Strategy: "task params"},
		caps.StepContinueOnError:    {Level: caps.Native, Strategy: "try around the task"},
		caps.StepTimeout:            {Level: caps.Native, Strategy: "task timeout"},
		caps.StepWorkdir:            {Level: caps.Native, Strategy: "task config run.dir"},
		caps.ConfigName:             {Level: caps.Native, Strategy: "((name)) in task params", Caveat: "the credential manager that resolves ((name)) is external, and a ((var)) never goes into a script text"},
		caps.DisplayName:            {Level: caps.Native},
		caps.TriggerTag:             {Level: caps.Emulated, Strategy: "git resource with source.tag_filter", Caveat: "a tag trigger that also has a branch filter is lost, because one git resource cannot hold both sets"},
		caps.TriggerFilterTags:      {Level: caps.Emulated, Strategy: "source.tag_filter of the git resource", Caveat: "one glob(7) pattern per resource; an exclude pattern is lost"},
		caps.TriggerSchedule:        {Level: caps.Emulated, Strategy: "time resource get with trigger: true; the source is interval for a step that divides the hour or the day, start and stop for a daily window, and days for a day list", Caveat: "the time resource is built for at least once every N minutes, not for a wall-clock time. An interval counts from the last version, so it has no clock alignment, the first check emits a version at once, and an every-minute cron cannot fire faster than the check cadence. A daily window is 5 minutes wide, so the job starts within 5 minutes after the cron time; a window that passes midnight is lost when it has a day list. Any other cron form is lost with the cron text kept"},
		caps.JobMatrix:              {Level: caps.Emulated, Strategy: "across over the axes", Caveat: "outputs of the substep are not visible outside the across step"},
		caps.JobIf:                  {Level: caps.Emulated, Strategy: "shared shell guard on script tasks; a status function becomes a job hook (on_failure or ensure)", Caveat: "a condition with no guard form leaves the job with no triggering get, so a person starts it, and adds a lost entry"},
		caps.StepIf:                 {Level: caps.Emulated, Strategy: "shared shell guard in the script of the task", Caveat: "a condition with no guard form runs as a task whose first script line is exit 0, with a lost entry"},
		caps.JobTimeout:             {Level: caps.Emulated, Strategy: "timeout on a do wrapper of the plan"},
		caps.JobRetry:               {Level: caps.Emulated, Strategy: "attempts on a do wrapper of the plan"},
		caps.JobAllowFailure:        {Level: caps.Emulated, Strategy: "try around the plan", Caveat: "the job shows success, not failure"},
		caps.JobEnvironmentApproval: {Level: caps.Emulated, Strategy: "job with no triggering get, started by a person", Caveat: "Concourse has no approver identity: anyone who may trigger the job approves it, and the reviewer list is lost; " + triggerSet},
		caps.JobManual:              {Level: caps.Emulated, Strategy: "job with no triggering get, started by a person", Caveat: "no approver identity; " + triggerSet},
		caps.JobCache:               {Level: caps.Emulated, Strategy: "task config caches", Caveat: "a task cache is kept per worker, job and task step, so a new worker starts cold; it has no key, restore keys or policy"},
		caps.JobArtifacts:           {Level: caps.Emulated, Strategy: "workspace artifact handed from task to task (inputs and outputs of the same name)", Caveat: "only within one job; a job with no script step writes no workspace"},
		caps.PipelineFinally:        {Level: caps.Emulated, Strategy: "job hook on the sink job of a chain, and on_failure hook on its other jobs", Caveat: "several finally steps wrap in do; the other jobs of the chain run only the failure and always finally steps, in on_failure; a pipeline whose jobs are not one chain loses the finally steps"},
		caps.PipelineConcurrency:    {Level: caps.Emulated, Strategy: "serial_groups on the jobs", Caveat: "cancel-in-progress is lost; a second build waits"},
		caps.JobEnv:                 {Level: caps.Emulated, Strategy: "copied into the params of every task", Caveat: "a step variable of the same name wins"},
		caps.PipelineEnv:            {Level: caps.Emulated, Strategy: "copied into the params of every task", Caveat: "a step variable of the same name wins"},
		caps.StepShell:              {Level: caps.Emulated, Strategy: "run.path names the shell", Caveat: "the image must provide the shell; a step with no shell from a GHA or GitLab source runs bash -e when the image has bash, else sh -e, so bash-only syntax fails under sh"},
		caps.StepAction:             {Level: caps.Emulated, Strategy: "task per actions.Lookup verdict", Caveat: "unknown actions are lost"},
		caps.JobRunner:              {Level: caps.Emulated, Strategy: "worker tags of the task", Caveat: "tags name workers, not hosted runner labels"},
		caps.ProjectMultiPipeline:   {Level: caps.Emulated, Strategy: "one pipeline file per IR pipeline under .concourse/", Caveat: "each pipeline needs its own fly set-pipeline and gets its own triggers"},
		caps.JobNeedsArtifacts:      {Level: caps.External, Strategy: "s3 or registry-image resource between the jobs", Caveat: "the operator sets up the s3 or registry resource and its credentials; Concourse passes no artifact between jobs by itself"},
		caps.TriggerPullRequest:     {Level: caps.Lost, Caveat: "Concourse has no pull request resource type in its core; a pull request resource type must be configured by the operator"},
		caps.TriggerAPI:             {Level: caps.Lost, Caveat: "Concourse has no resource, listener or Ext entry that the emitter can restore for an API trigger, so it is lost; a webhook_token on a resource is kept in Ext only"},
		caps.TriggerPipelineDone:    {Level: caps.Lost, Caveat: "Concourse has no pipeline-completion trigger"},
		caps.TriggerResource:        {Level: caps.Lost, Caveat: "the emitter writes git and time resources only; a trigger on any other resource is lost"},
		caps.PipelineInputs:         {Level: caps.Lost, Caveat: "a Concourse pipeline has no run-time inputs; fly set-pipeline vars apply at set time"},
		caps.PipelinePermissions:    {Level: caps.Lost, Caveat: "permissions come from the team and the credential manager"},
		caps.JobOutputs:             {Level: caps.Lost, Caveat: "a Concourse job has no named outputs for other jobs"},
		caps.JobServices:            {Level: caps.Lost, Caveat: "Concourse tasks have no service containers"},
		caps.JobEnvironment:         {Level: caps.Lost, Caveat: "Concourse has no environment name or URL"},
		caps.ExprOpaque:             {Level: caps.Lost, Caveat: noExpr + ", so every opaque expression is lost"},
	} {
		m[f] = s
	}
	return m
}
