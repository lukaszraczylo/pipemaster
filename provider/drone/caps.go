package drone

import "github.com/lukaszraczylo/pipemaster/caps"

// Caps declares what Drone CI does for each feature; the emitter implements every non-native verdict.
// A native entry can still be downgraded per use: the emitter notes inexact globs, unmatched labels and the like.
func (adapter) Caps() caps.Matrix {
	m := caps.Matrix{}
	for _, f := range caps.AllFeatures() {
		m[f] = caps.Support{Level: caps.Native}
	}
	for f, s := range map[caps.Feature]caps.Support{
		caps.TriggerFilterBranch:    {Level: caps.Native, Strategy: "trigger branch", Caveat: "only exact globs are native; for a pull request Drone matches the target branch"},
		caps.JobIf:                  {Level: caps.Native, Strategy: "pipeline trigger conditions", Caveat: "only the translatable expression subset; any other condition is emulated or lost"},
		caps.StepIf:                 {Level: caps.Native, Strategy: "step when", Caveat: "only the translatable expression subset; any other condition is emulated or lost"},
		caps.JobEnv:                 {Level: caps.Native, Strategy: "pipeline environment"},
		caps.DisplayName:            {Level: caps.Native, Strategy: "the pipeline and step names", Caveat: "a name that differs from the id is lost"},
		caps.ExprOpaque:             {Level: caps.Native, Caveat: "only opaque expressions of the drone dialect; others are lost"},
		caps.TriggerFilterPaths:     {Level: caps.Lost, Caveat: "the Drone OSS server does not evaluate trigger.paths, so the emitter never writes it"},
		caps.TriggerFilterTags:      {Level: caps.Emulated, Strategy: "ref filter refs/tags/<pattern> on the tag event", Caveat: "the glob dialect is doublestar; the parser reads it back as the tag trigger filter, and branch is not available on tag events"},
		caps.TriggerAPI:             {Level: caps.Lost, Caveat: "Drone starts API builds as event custom"},
		caps.TriggerPipelineDone:    {Level: caps.Lost, Caveat: "Drone has no pipeline-completion trigger"},
		caps.TriggerResource:        {Level: caps.Lost, Caveat: "Drone CI has no resource triggers"},
		caps.TriggerSchedule:        {Level: caps.External, Strategy: "trigger event cron with the cron job name", Caveat: "cron jobs are set in the UI or with drone cron add; the report names the command"},
		caps.TriggerManual:          {Level: caps.Emulated, Strategy: "trigger event custom", Caveat: "a person starts the build through the API or drone build create"},
		caps.JobManual:              {Level: caps.Emulated, Strategy: "trigger event custom", Caveat: "a person starts the build through the API or drone build create; the pipeline no longer runs on its normal events"},
		caps.PipelineEnv:            {Level: caps.Emulated, Strategy: "copied into the environment of every pipeline document", Caveat: "a step variable of the same name wins"},
		caps.PipelineInputs:         {Level: caps.Lost, Caveat: "Drone has no typed pipeline inputs"},
		caps.PipelineConcurrency:    {Level: caps.Lost, Caveat: "Drone concurrency.limit is a count and the IR models a group and cancel-in-progress, not a limit"},
		caps.PipelinePermissions:    {Level: caps.Lost, Caveat: "token scopes are server and repo settings"},
		caps.PipelineFinally:        {Level: caps.Emulated, Strategy: "a document that depends on every other document and runs on trigger status success and failure", Caveat: "the server skips a stage whose status filter does not match the build result"},
		caps.JobNeedsArtifacts:      {Level: caps.Lost, Caveat: "documents share no workspace"},
		caps.JobRunner:              {Level: caps.Lost, Caveat: "only the platform has a Drone form; other runner labels are lost"},
		caps.JobMatrix:              {Level: caps.Emulated, Strategy: "one pipeline document per cell", Caveat: "fail-fast and max-parallel are lost; a failed sibling stops the others without order"},
		caps.JobTimeout:             {Level: caps.External, Strategy: "repo setting timeout", Caveat: "the setting has no per-pipeline or per-step form"},
		caps.JobRetry:               {Level: caps.Lost, Caveat: "Drone has no retry key; only clone.retries exists"},
		caps.JobAllowFailure:        {Level: caps.Emulated, Strategy: "failure ignore on every step", Caveat: "later steps still run after a failed step"},
		caps.JobEnvironment:         {Level: caps.Lost, Caveat: "Drone has no environment name or URL in the pipeline file"},
		caps.JobEnvironmentApproval: {Level: caps.Emulated, Strategy: "trigger event promote with target set to the environment", Caveat: "a person starts the build with drone build promote"},
		caps.JobCache:               {Level: caps.Emulated, Strategy: "shared workspace of the steps in one document", Caveat: "nothing persists between runs"},
		caps.JobArtifacts:           {Level: caps.Emulated, Strategy: "shared workspace of the steps in one document", Caveat: "other documents cannot read the files"},
		caps.JobOutputs:             {Level: caps.Lost, Caveat: "Drone steps cannot pass values to other pipelines"},
		caps.StepAction:             {Level: caps.Emulated, Strategy: "per actions.Lookup verdict; a plugin step with settings", Caveat: "unknown actions are lost; the reference of a mapped checkout action is kept only in round-trip metadata, and without it a return to GHA writes no checkout step and drops its inputs such as fetch-depth"},
		caps.StepTimeout:            {Level: caps.Lost, Caveat: "the docker runner ignores a step timeout"},
		caps.StepShell:              {Level: caps.Lost, Caveat: "Drone compiles commands into its own set -e script that runs under /bin/sh; a step with no shell from a GHA or GitLab source runs under bash there, so bash-only syntax fails when the image sh is dash"},
		caps.ProjectMultiPipeline:   {Level: caps.Emulated, Strategy: "each pipeline is a document named <pipeline>-<job>", Caveat: "each document carries its pipeline's trigger condition"},
	} {
		m[f] = s
	}
	return m
}
