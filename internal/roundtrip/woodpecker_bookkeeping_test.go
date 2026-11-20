package roundtrip_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// A derived step ID and a map-form step key are parser bookkeeping. No target may report them as foreign data.
func TestWoodpeckerBookkeepingIsNotReportedAsPassthrough(t *testing.T) {
	src := fstest.MapFS{".woodpecker.yaml": {Data: []byte("steps:\n  - image: alpine\n    commands: [echo one]\n  - name: second\n    image: alpine\n    commands: [echo two]\n")}}
	mapForm := fstest.MapFS{".woodpecker.yaml": {Data: []byte("steps:\n  build:\n    name: compile\n    image: golang\n    commands: [go build]\n")}}
	for name, fsys := range map[string]fstest.MapFS{"derived id": src, "map key": mapForm} {
		for _, target := range []string{provider.GHA, provider.GitLab} {
			t.Run(name+"/"+target, func(t *testing.T) {
				p, _, err := prov(t, provider.Woodpecker).Parse(fsys)
				if err != nil {
					t.Fatal(err)
				}
				if k := "_derived_id"; name == "map key" {
					k = "_map_key"
					if _, ok := p.Pipelines[0].Jobs[0].Steps[0].Ext[provider.Woodpecker].(map[string]any)[k]; !ok {
						t.Fatalf("fixture lacks %s: %v", k, p.Pipelines[0].Jobs[0].Steps[0].Ext)
					}
				}
				_, rep, err := prov(t, target).Emit(p)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range rep.Entries {
					if e.Feature == caps.ExtPassthrough && (strings.Contains(e.Detail, "_derived_id") || strings.Contains(e.Detail, "_map_key")) {
						t.Errorf("bookkeeping reported as passthrough: %+v", e)
					}
				}
			})
		}
	}
}
