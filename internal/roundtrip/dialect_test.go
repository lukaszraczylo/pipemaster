package roundtrip_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/golden"
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

func TestDialectAliasRoundTrip(t *testing.T) {
	dir := filepath.Join(featuresRoot, provider.Forgejo, "features", "context.alias")
	p := prov(t, provider.Forgejo)
	src, _, err := p.Parse(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ir.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, filepath.Join(dir, "ir.golden.yaml"), got)
	files, _, err := p.Emit(src)
	if err != nil {
		t.Fatal(err)
	}
	text := string(files[".forgejo/workflows/ci.yml"])
	if strings.Contains(text, "forgejo.") || strings.Contains(text, "forge.") || !strings.Contains(text, "github.ref_name") {
		t.Errorf("emitter must print github.*:\n%s", text)
	}
	back, _, err := p.Parse(toFS(files))
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := ir.Equal(src, back); !same {
		t.Errorf("Parse->Emit->Parse differs:\n%s", diff)
	}
}

func TestDialectHostFixtures(t *testing.T) {
	for _, id := range []string{provider.Gitea, provider.Forgejo} {
		t.Run(id, func(t *testing.T) {
			dir := filepath.Join(featuresRoot, id, "features", "step.action--host")
			p := prov(t, id)
			src, _, err := p.Parse(os.DirFS(dir))
			if err != nil {
				t.Fatal(err)
			}
			got, err := ir.Marshal(src)
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, filepath.Join(dir, "ir.golden.yaml"), got)
		})
	}
}
