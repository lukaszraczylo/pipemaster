package gha

import "github.com/lukaszraczylo/pipemaster/caps"

// Caps declares what the emitter does for each feature; the emitter implements every non-native verdict listed here.
func (a *adapter) Caps() caps.Matrix {
	m := caps.Matrix{}
	for _, f := range caps.AllFeatures() {
		m[f] = caps.Support{Level: caps.Native}
	}
	for f, s := range map[caps.Feature]caps.Support{
		caps.JobRetry:               {Level: caps.Emulated, Strategy: "step-level retry loop via shell", Caveat: "Retry.Attempts is the total number of runs, so GitLab retry: N is Attempts N+1"},
		caps.JobManual:              {Level: caps.Emulated, Strategy: "workflow_dispatch trigger"},
		caps.TriggerPipelineDone:    {Level: caps.Native, Strategy: "workflow_run"},
		caps.JobCache:               {Level: caps.Emulated, Strategy: "actions/cache step"},
		caps.JobArtifacts:           {Level: caps.Emulated, Strategy: "upload-artifact step at job end"},
		caps.JobNeedsArtifacts:      {Level: caps.Emulated, Strategy: "download-artifact step at job start"},
		caps.PipelineFinally:        {Level: caps.Emulated, Strategy: "job with if: always() that needs every other job"},
		caps.JobEnvironmentApproval: {Level: caps.External, Strategy: "configure required reviewers on the environment in repository settings"},
		caps.StepImage:              {Level: caps.Emulated, Strategy: "uses: docker://<image> with entrypoint and args"},
		caps.TriggerResource:        {Level: caps.Lost, Caveat: "GitHub Actions has no resource triggers"},
		caps.ExprOpaque:             {Level: caps.Native, Caveat: "only opaque expressions of the gha dialect; others are lost"},
	} {
		m[f] = s
	}
	return m
}
