package irtest_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/ir/irtest"
)

func TestGenProjectUsesOrigin(t *testing.T) {
	for _, origin := range []string{"gha", "gitlab", "other"} {
		t.Run(origin, func(t *testing.T) {
			rapid.Check(t, func(t *rapid.T) {
				p := irtest.GenProject(t, origin, func(caps.Feature) bool { return true })
				if p.Origin.Provider != origin || p.Origin.Dialect != origin {
					t.Fatalf("origin = %+v", p.Origin)
				}
				for _, pl := range p.Pipelines {
					assertExtKeys(t, pl.Ext, origin)
					for _, j := range pl.Jobs {
						assertExtKeys(t, j.Ext, origin)
					}
				}
			})
		})
	}
}

func assertExtKeys(t *rapid.T, ext ir.Ext, origin string) {
	for k := range ext {
		if k != origin {
			t.Fatalf("Ext key %q, want only %q", k, origin)
		}
	}
}
