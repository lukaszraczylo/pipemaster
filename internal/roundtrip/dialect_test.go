package roundtrip_test

import (
	"os"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

func TestIdentityDialects(t *testing.T) {
	for _, id := range []string{provider.Gitea, provider.Forgejo} {
		for _, f := range fixtures(t, id) {
			t.Run(id+"/"+f.name, func(t *testing.T) {
				p := prov(t, id)
				src, _, err := p.Parse(os.DirFS(f.dir))
				if err != nil {
					t.Fatal(err)
				}
				files, _, err := p.Emit(src)
				if err != nil {
					t.Fatal(err)
				}
				back, _, err := p.Parse(toFS(files))
				if err != nil {
					t.Fatal(err)
				}
				if same, diff := ir.Equal(src, back); !same {
					t.Errorf("Parse->Emit->Parse differs:\n%s", diff)
				}
			})
		}
	}
}
