package tekton

import "github.com/lukaszraczylo/pipemaster/caps"

// Caps declares what Tekton does for each feature; the emitter implements every non-native verdict.
func (adapter) Caps() caps.Matrix {
	m := caps.Matrix{}
	for _, f := range caps.AllFeatures() {
		m[f] = caps.Support{Level: caps.Native}
	}
	const ghOnly = "the EventListener serves GitHub webhooks only"
	for f, s := range map[caps.Feature]caps.Support{
		caps.ExprOpaque:             {Level: caps.Native, Caveat: "only opaque expressions of the tekton dialect; a cel entry from a Tekton file is written back and needs enable-cel-in-whenexpression; others are lost"},
		caps.JobIf:                  {Level: caps.Native, Strategy: "PipelineTask when", Caveat: "only the input/operator/values subset; any other condition is emulated or lost"},
		caps.StepIf:                 {Level: caps.Native, Strategy: "step when", Caveat: "only the input/operator/values subset; any other condition is emulated or lost"},
		caps.JobContainer:           {Level: caps.Native, Strategy: "stepTemplate image"},
		caps.JobMatrix:              {Level: caps.Native, Strategy: "PipelineTask matrix", Caveat: "fail-fast and max-parallel are lost; axis values become strings"},
		caps.JobOutputs:             {Level: caps.Native, Strategy: "Task results"},
		caps.PipelineInputs:         {Level: caps.Native, Strategy: "Pipeline params"},
		caps.PipelineFinally:        {Level: caps.Native, Strategy: "Pipeline finally", Caveat: "finally tasks cannot be ordered among themselves, so a needs edge between them is lost"},
		caps.StepShell:              {Level: caps.Native, Strategy: "shebang line of the script"},
		caps.TriggerPush:            {Level: caps.Emulated, Strategy: "EventListener trigger with the github interceptor", Caveat: ghOnly},
		caps.TriggerPullRequest:     {Level: caps.Emulated, Strategy: "EventListener trigger with the github interceptor", Caveat: ghOnly},
		caps.TriggerTag:             {Level: caps.Emulated, Strategy: "EventListener trigger with the github interceptor", Caveat: ghOnly},
		caps.TriggerFilterBranch:    {Level: caps.Emulated, Strategy: "CEL interceptor filter on the ref", Caveat: ghOnly},
		caps.TriggerFilterTags:      {Level: caps.Emulated, Strategy: "CEL interceptor filter on the ref", Caveat: ghOnly},
		caps.TriggerFilterPaths:     {Level: caps.Emulated, Strategy: "CEL interceptor filter on the changed files", Caveat: "the github interceptor needs addChangedFiles; " + ghOnly},
		caps.TriggerSchedule:        {Level: caps.Emulated, Strategy: "CronJob that posts to the EventListener", Caveat: "the CronJob and the listener must run in the same cluster"},
		caps.TriggerManual:          {Level: caps.External, Strategy: "tkn pipeline start <name>", Caveat: "a person starts the run; the report names the command"},
		caps.JobServices:            {Level: caps.Emulated, Strategy: "Task sidecars", Caveat: "the service is at localhost, not at its name"},
		caps.JobCache:               {Level: caps.Emulated, Strategy: "workspace on a PVC from volumeClaimTemplate", Caveat: "tasks on different nodes need ReadWriteMany or the affinity assistant"},
		caps.JobArtifacts:           {Level: caps.Emulated, Strategy: "workspace on a PVC from volumeClaimTemplate", Caveat: "tasks on different nodes need ReadWriteMany or the affinity assistant"},
		caps.JobNeedsArtifacts:      {Level: caps.Emulated, Strategy: "workspace on a PVC from volumeClaimTemplate", Caveat: "tasks on different nodes need ReadWriteMany or the affinity assistant"},
		caps.PipelineEnv:            {Level: caps.Emulated, Strategy: "copied into the stepTemplate env of each Task", Caveat: "a step variable of the same name wins"},
		caps.StepAction:             {Level: caps.Emulated, Strategy: "StepAction ref per actions.Lookup verdict", Caveat: "unknown actions are lost"},
		caps.ProjectMultiPipeline:   {Level: caps.Emulated, Strategy: "one Pipeline document per IR pipeline", Caveat: "each pipeline gets its own trigger"},
		caps.ConfigName:             {Level: caps.Emulated, Strategy: "secrets through secretKeyRef on Secret ci-secrets, vars through configMapKeyRef on ConfigMap ci-vars", Caveat: "the Secret values and ConfigMap values are not written"},
		caps.JobRunner:              {Level: caps.Lost, Caveat: "Tekton schedules pods, not runner labels"},
		caps.JobEnvironment:         {Level: caps.Lost, Caveat: "Tekton has no environment name or URL"},
		caps.JobEnvironmentApproval: {Level: caps.Lost, Caveat: "Tekton has no approval gate"},
		caps.JobManual:              {Level: caps.Lost, Caveat: "Tekton has no manual job gate"},
		caps.PipelineConcurrency:    {Level: caps.Lost, Caveat: "Tekton has no concurrency group or cancel-in-progress"},
		caps.PipelinePermissions:    {Level: caps.Lost, Caveat: "permissions come from the ServiceAccount of the run"},
		caps.TriggerAPI:             {Level: caps.Emulated, Strategy: "EventListener entry of the Tekton file", Caveat: "only an entry that a Tekton file held is written; any other API trigger is lost"},
		caps.TriggerPipelineDone:    {Level: caps.Lost, Caveat: "Tekton has no pipeline-completion trigger"},
		caps.TriggerResource:        {Level: caps.Lost, Caveat: "Tekton has no resource trigger"},
	} {
		m[f] = s
	}
	return m
}
