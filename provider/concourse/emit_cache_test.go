package concourse_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

func TestJobCacheIsEmulatedWithTheWorkerScope(t *testing.T) {
	cases := map[string]ir.Cache{
		"paths only": {Paths: []string{".cache/go"}},
		"key":        {Key: "k", Paths: []string{".cache/go"}},
		"policy":     {Policy: "pull", Paths: []string{".cache/go"}},
		"fallback":   {Key: "k", FallbackKeys: []string{"main"}, Paths: []string{".cache/go"}},
	}
	for name, cache := range cases {
		t.Run(name, func(t *testing.T) {
			p, _ := parseFiles(t, map[string]string{"ci.yml": cachedFile})
			p.Pipelines[0].Jobs[0].Cache = []ir.Cache{cache}
			files, rep := emit(t, p)
			if !strings.Contains(files[".concourse/ci.yml"], "caches:") {
				t.Fatalf("no task caches:\n%s", files[".concourse/ci.yml"])
			}
			var got *report.Entry
			for i, e := range rep.Entries {
				if e.Feature == caps.JobCache {
					got = &rep.Entries[i]
				}
			}
			if got == nil || got.Level != caps.Emulated || !strings.Contains(got.Detail, "per worker") {
				t.Fatalf("job.cache entry = %+v, want an emulated entry naming the worker scope", got)
			}
			if want := len(cache.FallbackKeys) > 0; strings.Contains(got.Detail, "fallback keys") != want {
				t.Errorf("fallback note present = %v, want %v: %q", !want, want, got.Detail)
			}
		})
	}
}
