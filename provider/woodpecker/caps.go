package woodpecker

import "github.com/lukaszraczylo/pipemaster/caps"

// Caps declares what Woodpecker CI does for each feature; the emitter implements every non-native verdict.
// A native entry can still be downgraded per use: the emitter notes inexact globs, unmatched labels and the like.
func (adapter) Caps() caps.Matrix {
	m := caps.Matrix{}
	for _, f := range caps.AllFeatures() {
		m[f] = caps.Support{Level: caps.Native}
	}
	for f, s := range map[caps.Feature]caps.Support{
		caps.TriggerSchedule:        {Level: caps.External, Strategy: "when event cron; the schedule is a repo cron job", Caveat: "Woodpecker keeps the cron string in the repo settings (Cron), not in the workflow file"},
		caps.TriggerAPI:             {Level: caps.Lost, Caveat: "Woodpecker starts API pipelines as manual events"},
		caps.TriggerPipelineDone:    {Level: caps.Lost, Caveat: "Woodpecker has no pipeline-completion trigger"},
		caps.TriggerResource:        {Level: caps.Lost, Caveat: "Woodpecker CI has no resource triggers"},
		caps.TriggerFilterTags:      {Level: caps.Emulated, Strategy: "ref filter refs/tags/<pattern> on the tag event", Caveat: "the glob dialect is doublestar; the parser reads it back as the tag trigger filter"},
		caps.TriggerFilterBranch:    {Level: caps.Native, Strategy: "branch filter", Caveat: "only exact globs are native; other patterns keep doublestar meaning"},
		caps.PipelineEnv:            {Level: caps.Emulated, Strategy: "copied into the environment of every step", Caveat: "a step variable of the same name wins"},
		caps.JobEnv:                 {Level: caps.Emulated, Strategy: "copied into the environment of every step", Caveat: "a step variable of the same name wins"},
		caps.PipelineInputs:         {Level: caps.Lost, Caveat: "Woodpecker has no typed pipeline inputs"},
		caps.PipelineConcurrency:    {Level: caps.Native, Strategy: "workflow concurrency limit 1 with group", Caveat: "cancel-in-progress has no Woodpecker form"},
		caps.PipelinePermissions:    {Level: caps.Lost, Caveat: "token scopes are forge and repo settings"},
		caps.PipelineFinally:        {Level: caps.Emulated, Strategy: "a workflow that depends on every other workflow and runs on status success or failure", Caveat: "a skipped dependency is optional, so the workflow still runs"},
		caps.JobNeedsArtifacts:      {Level: caps.Lost, Caveat: "workflows do not share a workspace"},
		caps.JobIf:                  {Level: caps.Native, Strategy: "workflow when", Caveat: "only the translatable expression subset; any other condition disables the workflow"},
		caps.JobRunner:              {Level: caps.Lost, Caveat: "only a platform label has a Woodpecker form (labels.platform); other labels are lost"},
		caps.JobTimeout:             {Level: caps.External, Strategy: "repo setting Timeout", Caveat: "Woodpecker limits the whole pipeline, not a workflow"},
		caps.JobRetry:               {Level: caps.Lost, Caveat: "Woodpecker has no retry key; restarts are manual"},
		caps.JobAllowFailure:        {Level: caps.Emulated, Strategy: "failure ignore on every step", Caveat: "later steps still run after a failed step"},
		caps.JobEnvironment:         {Level: caps.Lost, Caveat: "Woodpecker has no environment name or URL in the workflow"},
		caps.JobEnvironmentApproval: {Level: caps.External, Strategy: "repo setting Require approval for"},
		caps.JobManual:              {Level: caps.Emulated, Strategy: "when event manual; a person starts the workflow from the UI or API", Caveat: "the workflow no longer runs on its normal events"},
		caps.JobCache:               {Level: caps.Emulated, Strategy: "shared workspace of the steps in one workflow", Caveat: "nothing persists between runs"},
		caps.JobArtifacts:           {Level: caps.Emulated, Strategy: "shared workspace of the steps in one workflow", Caveat: "other workflows cannot read the files"},
		caps.JobOutputs:             {Level: caps.Lost, Caveat: "Woodpecker steps cannot pass values to other workflows"},
		caps.StepAction:             {Level: caps.Native, Strategy: "per actions.Lookup verdict; a plugin step is native", Caveat: "unknown actions are lost; the reference of a mapped checkout action is kept only in round-trip metadata, and without it a return to GHA writes no checkout step and drops its inputs such as fetch-depth"},
		caps.StepTimeout:            {Level: caps.Lost, Caveat: "Woodpecker has only a pipeline timeout in the repo settings"},
		caps.StepShell:              {Level: caps.Lost, Caveat: "Woodpecker runs commands through /bin/sh -e of the image; a step with no shell from a GHA or GitLab source runs under bash there, so bash-only syntax fails when the image sh is dash"},
		caps.ProjectMultiPipeline:   {Level: caps.Emulated, Strategy: "each job of each pipeline is a workflow named <pipeline>-<job>", Caveat: "each workflow carries its pipeline's trigger condition"},
		caps.ExprOpaque:             {Level: caps.Native, Caveat: "only opaque expressions of the woodpecker dialect; others are lost"},
		caps.DisplayName:            {Level: caps.Native, Strategy: "the step name", Caveat: "pipeline and job names that differ from the id are lost; the workflow name is the file name"},
	} {
		m[f] = s
	}
	return m
}
