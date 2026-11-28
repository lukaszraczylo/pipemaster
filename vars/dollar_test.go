package vars_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/vars"
)

func TestPrepareReExportKeepsDollarPairByOrigin(t *testing.T) {
	for _, c := range []struct {
		name, origin, lit, want string
	}{
		{"gitlab escape", provider.GitLab, `$CI_PROJECT_DIR/a$$b`, `a\$b"`},
		{"gha literal pair", provider.GHA, `$GITHUB_WORKSPACE/a$$b`, `a\$\$b"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := shellStep(`echo $V`)
			in.Origin.Provider = c.origin
			in.Pipelines[0].Env = []ir.Var{{Name: "V", Value: ir.Value{Lit: c.lit}}}
			p, _ := vars.Prepare(in, provider.Woodpecker)
			got := p.Pipelines[0].Jobs[0].Steps[0].Script[0]
			t.Logf("%s", got)
			if !strings.Contains(got, c.want) {
				t.Errorf("missing %q in %q", c.want, got)
			}
		})
	}
}
