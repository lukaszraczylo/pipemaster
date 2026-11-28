package roundtrip_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

const dockerStepWorkflow = /* #nosec G101 -- an image digest, not a credential */ `name: d
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: docker://alpine:3.20
        with:
          entrypoint: /bin/sh
          args: -c 'echo MARK dockerstep.run'
      - uses: docker://alpine@sha256:1e42bbe2508154c9126d48c2b8a75420c3544343bf86fd041fb7527e017a4b4a
        with:
          entrypoint: /bin/true
`

func TestDockerStepRunsOnEveryTargetAndReturnsExact(t *testing.T) {
	src := fstest.MapFS{".github/workflows/ci.yml": {Data: []byte(dockerStepWorkflow)}}
	for _, to := range targets(provider.GHA) {
		t.Run(to, func(t *testing.T) {
			r := hops(t, src, prov(t, provider.GHA), prov(t, to), false, true)
			if same, diff := ir.Equal(r.src, r.back); !same {
				t.Fatalf("round trip changed the pipeline:\n%s", diff)
			}
			if provider.Family(to) == provider.GHA {
				return
			}
			emulated := 0
			for _, e := range r.toRep.Entries {
				if e.Feature == caps.StepAction && e.Level == caps.Emulated && strings.HasPrefix(e.Strategy, "docker step") {
					emulated++
				}
				if e.Feature == caps.StepAction && e.Level >= caps.External {
					t.Errorf("a docker step is reported as not run: %+v", e)
				}
			}
			if emulated != 2 {
				t.Errorf("want 2 emulated docker step entries, got %d", emulated)
			}
			all := ""
			for _, b := range r.toFiles {
				all += string(b)
			}
			if !strings.Contains(all, "echo MARK dockerstep.run") || !strings.Contains(all, "/bin/true") {
				t.Errorf("the command of the docker step is not in the output:\n%s", all)
			}
		})
	}
}
