package emitkit

import (
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// GatedSteps returns the action steps whose result another step, condition or output reads (steps.<id>.outputs,
// .outcome or .conclusion). A target that drops such a step would also disable its readers, so the step is not
// droppable: its emitter writes a failing step instead.
func GatedSteps(p *ir.Project) map[*ir.Step]bool {
	out := map[*ir.Step]bool{}
	if p == nil {
		return out
	}
	for _, pl := range p.Pipelines {
		for _, j := range pl.Jobs {
			var texts []string
			for _, o := range j.Outputs {
				texts = append(texts, o.Value)
			}
			for _, st := range j.Steps {
				if st != nil {
					texts = append(texts, stepTexts(st)...)
				}
			}
			all := strings.Join(texts, "\n")
			for _, st := range j.Steps {
				if st == nil || st.ID == "" || (st.Action == nil && st.Kind != ir.StepAction) {
					continue
				}
				if regexp.MustCompile(`\bsteps\.` + regexp.QuoteMeta(st.ID) + `\.(outputs|outcome|conclusion)\b`).MatchString(all) {
					out[st] = true
				}
			}
		}
	}
	return out
}

func stepTexts(st *ir.Step) []string {
	var t []string
	if st.If != nil {
		t = append(t, expr.String(st.If))
	}
	val := func(v ir.Value) {
		if v.Expr != nil {
			t = append(t, expr.String(v.Expr))
		}
		t = append(t, v.Lit)
	}
	for _, v := range st.With {
		val(v)
	}
	for _, v := range st.Env {
		val(v.Value)
	}
	t = append(t, st.Script...)
	t = append(t, st.WorkDir)
	return t
}
