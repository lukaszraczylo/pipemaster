package gitlab

import "github.com/lukaszraczylo/pipemaster/caps"

// Caps declares what GitLab CI does for each feature; the emitter implements every non-native verdict listed here.
func (adapter) Caps() caps.Matrix {
	m := caps.Matrix{}
	for _, f := range caps.AllFeatures() {
		m[f] = caps.Support{Level: caps.Native}
	}
	for f, s := range map[caps.Feature]caps.Support{
		caps.TriggerSchedule:        {Level: caps.External, Strategy: "pipeline schedule in project settings", Caveat: "GitLab keeps the cron string outside the CI file"},
		caps.TriggerAPI:             {Level: caps.External, Strategy: "pipeline trigger token or API call"},
		caps.TriggerPipelineDone:    {Level: caps.Lost, Caveat: "GitLab CI has no workflow_run equivalent"},
		caps.TriggerResource:        {Level: caps.Lost, Caveat: "GitLab CI has no resource triggers"},
		caps.TriggerFilterPaths:     {Level: caps.Emulated, Strategy: "rules:changes"},
		caps.PipelineInputs:         {Level: caps.Emulated, Strategy: "variables with description"},
		caps.PipelineConcurrency:    {Level: caps.Emulated, Strategy: "resource_group on every job"},
		caps.PipelinePermissions:    {Level: caps.External, Strategy: "job token scope in project settings"},
		caps.PipelineFinally:        {Level: caps.Emulated, Strategy: "when: always job in the .post stage"},
		caps.JobEnvironmentApproval: {Level: caps.External, Strategy: "protected environment in project settings"},
		caps.JobOutputs:             {Level: caps.Emulated, Strategy: "dotenv report artifact"},
		caps.StepAction:             {Level: caps.Emulated, Strategy: "mapped to script lines where the actions table has a mapping"},
		caps.StepImage:              {Level: caps.Emulated, Strategy: "docker run in the script"},
		caps.StepIf:                 {Level: caps.Emulated, Strategy: "shell guard around the step's lines"},
		caps.StepEnv:                {Level: caps.Emulated, Strategy: "export before the step's lines"},
		caps.StepContinueOnError:    {Level: caps.Emulated, Strategy: "|| true after the step's lines"},
		caps.StepTimeout:            {Level: caps.Emulated, Strategy: "timeout command around the step's lines"},
		caps.StepWorkdir:            {Level: caps.Emulated, Strategy: "cd in a subshell"},
		caps.StepShell:              {Level: caps.Lost, Caveat: "GitLab runs scripts in the runner's configured shell"},
		caps.ExprOpaque:             {Level: caps.Native, Caveat: "only opaque expressions of the gitlab dialect; others are lost"},
	} {
		m[f] = s
	}
	return m
}
