package roundtrip_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

func TestRoundTripMatrix(t *testing.T) {
	for _, f := range allFixtures(t) {
		for _, to := range targets(f.provider) {
			for _, mode := range []string{"sidecar", "embed"} {
				t.Run(f.provider+"/"+f.name+"/to-"+to+"/"+mode, func(t *testing.T) {
					r := hops(t, os.DirFS(f.dir), prov(t, f.provider), prov(t, to), mode == "embed", true)
					if same, diff := ir.Equal(r.src, r.back); !same {
						t.Errorf("A->B->A differs:\n%s", diff)
					}
				})
			}
		}
	}
}

func TestRoundTripIdentity(t *testing.T) {
	for _, f := range allFixtures(t) {
		t.Run(f.provider+"/"+f.name, func(t *testing.T) {
			a := prov(t, f.provider)
			src, _, err := a.Parse(os.DirFS(f.dir))
			if err != nil {
				t.Fatal(err)
			}
			files, rep, err := a.Emit(src)
			if err != nil {
				t.Fatal(err)
			}
			back, _, err := a.Parse(toFS(files))
			if err != nil {
				t.Fatal(err)
			}
			// A dialect drops a key it cannot hold from the file and keeps it in the metadata.
			if isDialect(a.ID()) && rep.Worst() >= caps.External {
				for _, d := range diffNodes(src, back) {
					if !reportedLost(rep, d.Path) {
						t.Errorf("%s: %s differs and no lost or external entry names it", d.Path, d.Field)
					}
				}
				roundtrip.Restore(back, roundtrip.NewMeta(a.ID(), src, a, rep))
			}
			if same, diff := ir.Equal(src, back); !same {
				t.Errorf("A->A differs:\n%s", diff)
			}
		})
	}
}

// reportedLost is true when a lost or external entry sits at the node or at one of its trigger, scope or event parts.
func reportedLost(rep *report.Report, path string) bool {
	return slices.ContainsFunc(rep.Entries, func(e report.Entry) bool {
		return e.Level >= caps.External && (atOrBelow(e.IRPath, path) || strings.HasPrefix(e.IRPath, path+"/event/"))
	})
}
