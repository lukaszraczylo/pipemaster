package roundtrip_test

import (
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

const foldEnvWorkflow = `on: push
env:
  MODE: ${{ github.ref_name == 'main' && 'full' || '' }}
  KEY: ${{ secrets.GPG }}
  PLAIN: one
jobs:
  j:
    runs-on: docker
    steps:
      - run: echo
`

// Pipeline env that GitLab cannot hold must come back from the metadata when several pipelines share one file.
func TestMultiPipelineEnvReturnsFromMetadata(t *testing.T) {
	fsys := fstest.MapFS{
		".github/workflows/a.yml": {Data: []byte(foldEnvWorkflow)},
		".github/workflows/b.yml": {Data: []byte(foldEnvWorkflow)},
	}
	for _, id := range []string{provider.GHA, provider.Gitea} {
		t.Run(id, func(t *testing.T) {
			p := prov(t, id)
			src := fsys
			if id != provider.GHA {
				src = fstest.MapFS{}
				for n, f := range fsys {
					src[".gitea/workflows/"+n[len(".github/workflows/"):]] = f
				}
			}
			r := hops(t, src, p, prov(t, provider.GitLab), false, true)
			if same, diff := ir.Equal(r.src, r.back); !same {
				t.Errorf("A->GitLab->A differs:\n%s", diff)
			}
		})
	}
}

// Each folded job copy receives the pipeline variables, yet the report holds one entry per pipeline and feature.
func TestFoldedPipelineEnvLossIsReportedOnce(t *testing.T) {
	const twoJobs = foldEnvWorkflow + `  k:
    runs-on: docker
    steps:
      - run: echo
`
	fsys := fstest.MapFS{
		".github/workflows/a.yml": {Data: []byte(twoJobs)},
		".github/workflows/b.yml": {Data: []byte(foldEnvWorkflow)},
	}
	r := hops(t, fsys, prov(t, provider.GHA), prov(t, provider.GitLab), false, true)
	count := map[string]int{}
	for _, e := range r.toRep.Entries {
		if e.Feature == caps.PipelineEnv || e.Feature == caps.JobEnv {
			count[e.IRPath+" "+string(e.Feature)]++
		}
	}
	if count["pipeline/a pipeline.env"] != 1 || count["pipeline/b pipeline.env"] != 1 || len(count) != 2 {
		t.Errorf("env loss entries = %v, want one pipeline.env entry per pipeline and no job.env entry", count)
	}
}
