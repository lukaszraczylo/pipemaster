package expr_test

// A GitLab source on a target that holds the target branch on a pull request: GitLab leaves CI_COMMIT_BRANCH unset
// in a merge request pipeline (https://docs.gitlab.com/ci/variables/predefined_variables/, CI_COMMIT_BRANCH: "Not
// available in merge request pipelines or tag pipelines"), Woodpecker sets it to the target branch.
// lean4/Pipemaster/TranslateFacts.lean: glWp_sound.

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/expr/woodpecker"
)

// glView is a GitLab run (Mirrors glEnv): a merge request pipeline has no branch and no tag.
func (e canonEnv) glView() view {
	v := e.gitlabView()
	if e.Event != nil && *e.Event == "pull_request" {
		delete(v, "branch")
		delete(v, "tag")
	}
	return v
}

type glPrOut struct {
	Tr      json.RawMessage `json:"tr"`
	Src     []bool          `json:"src"`
	Wp      []bool          `json:"wp"`
	Plain   []bool          `json:"plain"`
	WpClose []bool          `json:"wp_closed"`
	WpMeta  []bool          `json:"wp_metadata"`
	TrPlain json.RawMessage `json:"tr_plain"`
}

func TestGitLabToWoodpeckerVectors(t *testing.T) {
	envs, cases := loadEnvs(t, "expr-glpr")
	count := map[string]int{}
	for i, c := range cases {
		var in struct {
			Expr json.RawMessage `json:"expr"`
		}
		var out glPrOut
		if err := json.Unmarshal(c.In, &in); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		src, err := decodeExpr(in.Expr, gitlab.Lang.ID)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		got, ok := expr.AssumeGitLab(src, gitlab.Lang)
		if string(out.Tr) == "null" {
			count["refused"]++
			if ok {
				t.Errorf("case %d %s: Lean refuses, Go rewrote to %s", i, expr.String(src), expr.String(got))
			}
			if _, tok := expr.TranslateFacts(src, gitlab.Lang, woodpecker.Lang, expr.Facts{}); tok {
				t.Errorf("case %d %s: translated although the rewrite is refused", i, expr.String(src))
			}
			continue
		}
		want, err := decodeExpr(out.TrPlain, gitlab.Lang.ID)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("case %d %s: Go = %v, %v; Lean = %s", i, expr.String(src), got, ok, expr.String(want))
			continue
		}
		count["rewritten"]++
		wp, wok := expr.TranslateFacts(src, gitlab.Lang, woodpecker.Lang, expr.Facts{})
		if tb := expr.TranslatableFacts(src, gitlab.Lang, woodpecker.Lang, expr.Facts{}); tb != wok {
			t.Errorf("case %d %s: TranslatableFacts = %v, TranslateFacts = %v", i, expr.String(src), tb, wok)
		}
		if !wok {
			continue
		}
		count["woodpecker"]++
		plain, pok := expr.Translate(src, gitlab.Lang, woodpecker.Lang)
		for j, e := range envs {
			if s := holdsOf(t, src, e.glView(), gitlab.Lang.Sem); s != out.Src[j] {
				t.Errorf("case %d env %d %s: GitLab = %v, Lean = %v", i, j, expr.String(src), s, out.Src[j])
			}
			for _, v := range []struct {
				sfx  string
				lean []bool
			}{{"", out.Wp}, {"_closed", out.WpClose}, {"_metadata", out.WpMeta}} {
				g := woodpeckerHolds(t, wp, e.factWoodpeckerView(v.sfx))
				if (g != out.Src[j] && !unsetEventNarrower(e, g, out.Src[j])) || (g != v.lean[j] && !unsetEventNarrower(e, g, v.lean[j])) {
					t.Errorf("case %d env %d %s -> %s: Woodpecker%s = %v, GitLab = %v, Lean wp = %v", i, j, expr.String(src), expr.String(wp), v.sfx, g, out.Src[j], v.lean[j])
				}
			}
			if pok {
				p := woodpeckerHolds(t, plain, e.factWoodpeckerView(""))
				if p != out.Plain[j] && !unsetEventNarrower(e, p, out.Plain[j]) {
					t.Errorf("case %d env %d: plain translation = %v, Lean = %v", i, j, p, out.Plain[j])
				}
				if p && !out.Src[j] {
					count["plain runs more"]++
				}
			}
		}
	}
	for _, k := range []string{"refused", "rewritten", "woodpecker", "plain runs more"} {
		if count[k] < 10 {
			t.Errorf("only %d cases for %q; the vectors lost coverage", count[k], k)
		}
	}
	t.Logf("cases: %v", count)
}
