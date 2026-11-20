package woodpecker

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	wpexpr "github.com/lukaszraczylo/pipemaster/expr/woodpecker"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

func TestMergeCondFailsSafe(t *testing.T) {
	jp := "pipeline/woodpecker/job/ci"
	base := []whenEntry{{Item: wpexpr.Item{Event: wpexpr.Filter{Include: []string{"push"}}}}}
	bad := wpexpr.Item{Evaluate: "(("}
	if _, err := wpexpr.WhenExpr([]wpexpr.Item{bad}); err == nil {
		t.Fatal("precondition: WhenExpr must reject the item")
	}
	uses := []ir.FeatureUse{{IRPath: jp, Feature: caps.JobIf}}
	e := &emitter{led: emitkit.NewLedger("ir", dialect, adapter{}.Caps())}
	e.led.Decide(uses[0])
	got := e.mergeCond(jp, expr.Lit{Kind: expr.LitStr, Str: "x"}, base, []wpexpr.Item{bad})
	if len(got) != 1 || got[0].Evaluate != "false" {
		t.Fatalf("a condition that cannot merge must disable the workflow, got %+v", got)
	}
	if !strings.Contains(strings.Join(e.head, "\n"), "pipemaster:lost job.if") {
		t.Errorf("want a lost comment, head = %v", e.head)
	}
	rep, err := e.led.Finish(uses)
	if err != nil {
		t.Fatal(err)
	}
	lost := false
	for _, en := range rep.Entries {
		lost = lost || (en.Feature == caps.JobIf && en.Level == caps.Lost)
	}
	if !lost {
		t.Errorf("want a lost job.if entry: %+v", rep.Entries)
	}
}
