package gitlab_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
)

func stepEnvFile(exports ...string) string {
	return `build:
  script:
    # pipemaster:step build-step-1
    # pipemaster:emulated step.env pipeline/gitlab-ci/job/build/step/build-step-1
    - |
      (
` + strings.Join(exports, "\n") + `
      echo run
      )
`
}

func TestStepEnvMarkerWithoutMetaKeepsReferencesAsExpressions(t *testing.T) {
	tests := []struct {
		name string
		line string
		want ir.Value
	}{
		{"literal", `      export MODE="fast"`, ir.Value{Lit: "fast"}},
		{"text around reference", `      export GREETING="hello $GITLAB_USER_LOGIN"`, ir.Value{Kind: ir.ValueExpr, Expr: mustCanon(t, "format('hello {0}', actor)")}},
		{"lone reference", `      export WHO="$GITLAB_USER_LOGIN"`, ir.Value{Kind: ir.ValueExpr, Expr: mustCanon(t, "actor")}},
		{"other shell variable", `      export KEY="$KEY"`, ir.Value{Lit: "$KEY"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, _, err := gitlab.New().Parse(fstest.MapFS{".gitlab-ci.yml": {Data: []byte(stepEnvFile(tc.line))}})
			if err != nil {
				t.Fatal(err)
			}
			st := p.Pipelines[0].Jobs[0].Steps[0]
			if len(st.Env) != 1 {
				t.Fatalf("env = %+v, want one variable", st.Env)
			}
			got := st.Env[0].Value
			if got.Kind != tc.want.Kind || got.Lit != tc.want.Lit || (tc.want.Expr != nil && expr.String(got.Expr) != expr.String(tc.want.Expr)) {
				t.Errorf("value = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestStepEnvMarkerReferenceEmitsToGHA(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		want     string
		wantLost bool
	}{
		{"mapped reference", `      export REV="$CI_COMMIT_SHA"`, "REV: ${{ github.sha }}", false},
		{"mapped reference inside text", `      export GREETING="hello $GITLAB_USER_LOGIN"`, "GREETING: hello ${{ github.actor }}", false},
		{"reference with no exact GHA value is reported", `      export GREETING="hello $CI_PIPELINE_SOURCE"`, "# pipemaster:lost expr.opaque", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, _, err := gitlab.New().Parse(fstest.MapFS{".gitlab-ci.yml": {Data: []byte(stepEnvFile(tc.line))}})
			if err != nil {
				t.Fatal(err)
			}
			files, rep, err := gha.New(gha.GHA).Emit(p)
			if err != nil {
				t.Fatal(err)
			}
			out := string(files[".github/workflows/gitlab-ci.yml"])
			if strings.Contains(out, "'${{'") {
				t.Errorf("reference escaped as text:\n%s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("output lacks %q:\n%s", tc.want, out)
			}
			lost := false
			for _, e := range rep.Entries {
				lost = lost || (e.Level >= caps.Lost && e.Feature == caps.ExprOpaque)
			}
			if lost != tc.wantLost {
				t.Errorf("lost expr.opaque entry = %v, want %v: %+v", lost, tc.wantLost, rep.Entries)
			}
		})
	}
}

func mustCanon(t *testing.T, s string) expr.Expr {
	t.Helper()
	x, err := expr.ParseCanonical(s)
	if err != nil {
		t.Fatal(err)
	}
	return x
}
