package roundtrip_test

import (
	"os"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestRoundTripMatrix(t *testing.T) {
	for _, f := range allFixtures(t) {
		for _, mode := range []string{"sidecar", "embed"} {
			t.Run(f.provider+"/"+f.name+"/"+mode, func(t *testing.T) {
				a, b := prov(t, f.provider), prov(t, other(f.provider))
				r := hops(t, os.DirFS(f.dir), a, b, mode == "embed", true)
				if same, diff := ir.Equal(r.src, r.back); !same {
					t.Errorf("A->B->A differs:\n%s", diff)
				}
			})
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
			files, _, err := a.Emit(src)
			if err != nil {
				t.Fatal(err)
			}
			back, _, err := a.Parse(toFS(files))
			if err != nil {
				t.Fatal(err)
			}
			if same, diff := ir.Equal(src, back); !same {
				t.Errorf("A->A differs:\n%s", diff)
			}
		})
	}
}
