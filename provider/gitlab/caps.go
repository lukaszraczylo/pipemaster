package gitlab

import "github.com/lukaszraczylo/pipemaster/caps"

// Caps declares what GitLab CI does for each feature; the emitter implements every non-native verdict listed here.
// A native entry can still be downgraded per use: the emitter notes lost parts (unknown actions, retry caps, other shells).
func (adapter) Caps() caps.Matrix {
	m := caps.Matrix{}
	for _, f := range caps.AllFeatures() {
		m[f] = caps.Support{Level: caps.Native}
	}
	for f, s := range map[caps.Feature]caps.Support{
		caps.TriggerSchedule:        {Level: caps.External, Strategy: "pipeline schedule in project settings", Caveat: "GitLab keeps the cron string outside the CI file"},
		caps.TriggerAPI:             {Level: caps.External, Strategy: "pipeline trigger token or API call"},
		caps.TriggerPipelineDone:    {Level: caps.Lost, Caveat: "a GitLab downstream pipeline starts from a trigger job in the upstream project"},
		caps.TriggerResource:        {Level: caps.Lost, Caveat: "GitLab CI has no resource triggers"},
		caps.ProjectMultiPipeline:   {Level: caps.Emulated, Strategy: "jobs of every pipeline in one file under <pipeline>:<job> keys", Caveat: "each job carries its pipeline's trigger condition; stage names and inputs are not restored"},
		caps.PipelineInputs:         {Level: caps.Emulated, Strategy: "variables with description", Caveat: "input types and required flags are not enforced"},
		caps.PipelineConcurrency:    {Level: caps.Emulated, Strategy: "resource_group on every job", Caveat: "queues, does not cancel"},
		caps.PipelinePermissions:    {Level: caps.Lost, Caveat: "job token scope is a project setting"},
		caps.JobEnvironmentApproval: {Level: caps.External, Strategy: "protected environment approval rules in project settings"},
		caps.JobMatrix:              {Level: caps.Native, Strategy: "parallel:matrix", Caveat: "include and exclude are expanded into explicit entries"},
		caps.JobOutputs:             {Level: caps.Emulated, Strategy: "dotenv artifact report"},
		caps.StepAction:             {Level: caps.Native, Strategy: "per actions.Lookup verdict", Caveat: "unknown actions are lost; the reference of a mapped checkout action is kept only in round-trip metadata, and without it a return to GHA writes the pinned default actions/checkout (not the source ref) with fetch-depth and submodules from GIT_DEPTH and GIT_SUBMODULE_STRATEGY"},
		caps.StepImage:              {Level: caps.Emulated, Strategy: "one job per run of steps that share an image, chained by needs", Caveat: "GitLab sets the image per job, so the workspace passes between the jobs as artifacts; a job that cannot split keeps the step image lost"},
		caps.StepIf:                 {Level: caps.Emulated, Strategy: "shell guard around the step's lines", Caveat: "step reports success instead of skipped"},
		caps.StepEnv:                {Level: caps.Emulated, Strategy: "export lines inside a subshell around the step"},
		caps.StepContinueOnError:    {Level: caps.Emulated, Strategy: "|| true after each line of the step"},
		caps.StepTimeout:            {Level: caps.Lost, Caveat: "GitLab has only a job timeout"},
		caps.StepWorkdir:            {Level: caps.Emulated, Strategy: "cd in a subshell around the step"},
		caps.StepShell:              {Level: caps.Lost, Caveat: "GitLab runs scripts in the runner's configured shell"},
		caps.DisplayName:            {Level: caps.Native, Strategy: "the pipeline name (workflow:name)", Caveat: "step names and job display names have no GitLab field and are lost; a job name survives only as its key; a pipeline name with a dollar sign or over 255 characters is lost"},
		caps.ExprOpaque:             {Level: caps.Native, Caveat: "only opaque expressions of the gitlab dialect; others are lost"},
	} {
		m[f] = s
	}
	return m
}
