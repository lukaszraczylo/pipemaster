package roundtrip_test

import (
	"os"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
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
				roundtrip.Restore(back, roundtrip.NewMeta(a.ID(), src, a, rep))
			}
			if same, diff := ir.Equal(src, back); !same {
				t.Errorf("A->A differs:\n%s", diff)
			}
		})
	}
}
