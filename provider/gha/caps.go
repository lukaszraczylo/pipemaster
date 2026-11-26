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
		caps.JobCache:               {Level: caps.Native, Strategy: "actions/cache step; policy pull is actions/cache/restore, policy push is actions/cache/save at job end"},
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
	for _, c := range a.d.Caveats {
		m[c.Feature] = caps.Support{Level: caps.Native, Caveat: c.Caveat}
	}
	for _, ig := range a.d.Ignored {
		m[ig.Feature] = caps.Support{Level: caps.Lost, Caveat: ig.Caveat}
	}
	if len(a.d.SecretPrefixes)+len(a.d.VarPrefixes)+len(a.d.VarNames) > 0 {
		m[caps.ConfigName] = caps.Support{Level: caps.Native, Caveat: "a name the target rejects is external; create it under another name"}
	}
	return m
}
