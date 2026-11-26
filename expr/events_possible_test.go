package expr_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
)

func TestFoldEvent(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"parent source holds", `$CI_PIPELINE_SOURCE == "parent_pipeline"`, "true"},
		{"push source fails", `$CI_PIPELINE_SOURCE == "push"`, "false"},
		{"negated push holds", `$CI_PIPELINE_SOURCE != "push"`, "true"},
		{"other test stays", `$CI_PIPELINE_SOURCE == "parent_pipeline" && $FLAG == "1"`, "gitlab.FLAG == '1'"},
		{"or with push", `$CI_PIPELINE_SOURCE == "push" || $CI_PIPELINE_SOURCE == "parent_pipeline"`, "true"},
		{"and with push", `$CI_PIPELINE_SOURCE == "push" && $FLAG == "1"`, "false"},
		{"no event test", `$FLAG == "1"`, "gitlab.FLAG == '1'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := exprgitlab.Parse(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			got := expr.String(expr.FoldEvent(e, "parent_pipeline"))
			if got != tc.want {
				t.Fatalf("FoldEvent = %q, want %q", got, tc.want)
			}
		})
	}
}
