package roundtrip_test

import (
	"testing"
	"testing/fstest"

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
