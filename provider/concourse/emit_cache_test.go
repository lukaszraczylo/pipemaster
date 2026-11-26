package concourse_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestJobCacheVerdictIsNativeUnlessKeyOrPolicy(t *testing.T) {
	cases := map[string]struct {
		cache ir.Cache
		want  caps.Level
	}{
		"paths only": {ir.Cache{Paths: []string{".cache/go"}}, caps.Native},
		"key":        {ir.Cache{Key: "k", Paths: []string{".cache/go"}}, caps.Emulated},
		"policy":     {ir.Cache{Policy: "pull", Paths: []string{".cache/go"}}, caps.Emulated},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p, _ := parseFiles(t, map[string]string{"ci.yml": cachedFile})
			p.Pipelines[0].Jobs[0].Cache = []ir.Cache{c.cache}
			files, rep := emit(t, p)
			if !strings.Contains(files[".concourse/ci.yml"], "caches:") {
				t.Fatalf("no task caches:\n%s", files[".concourse/ci.yml"])
			}
			got := caps.Native
			for _, e := range rep.Entries {
				if e.Feature == caps.JobCache {
					got = e.Level
				}
			}
			if got != c.want {
				t.Errorf("job.cache level = %v, want %v", got, c.want)
			}
		})
	}
}
