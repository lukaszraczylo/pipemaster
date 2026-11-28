package tekton

import (
	"slices"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// refTypeArg marks the job conditions that the emitter wrote as a test of the ref type instead of a prefix test of the
// ref. Back is what the written condition reads back as, so the parser restores If only for an unedited condition.
type refTypeArg struct {
	Jobs map[string]refTypeJob `json:"ref_type_jobs"`
}

type refTypeJob struct {
	If   string `json:"if"`
	Back string `json:"back"`
}

// noteRefType records a job condition that RefTypeForm changed.
func (tc *taskCtx) noteRefType(orig, written expr.Expr) {
	if expr.String(orig) == expr.String(written) {
		return
	}
	if tc.pp.refTypes == nil {
		tc.pp.refTypes = map[string]refTypeJob{}
	}
	tc.pp.refTypes[tc.jp.j.ID] = refTypeJob{If: expr.String(orig), Back: expr.String(written)}
}

// markRefTypes writes the marker of the job conditions that noteRefType recorded.
func (e *emitter) markRefTypes(pp *plan) error {
	if len(pp.refTypes) == 0 {
		return nil
	}
	return e.markArg(caps.JobIf, pp.path, refTypeArg{Jobs: pp.refTypes})
}

// restoreRefTypes puts back the conditions that the marker holds, for each job whose condition is still as written.
func restoreRefTypes(pl *ir.Pipeline, head string) {
	var a refTypeArg
	if !readMarks(head).arg(caps.JobIf, "pipeline/"+pl.ID, &a) {
		return
	}
	for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
		w, ok := a.Jobs[j.ID]
		if !ok || j.If == nil || expr.String(j.If) != w.Back {
			continue
		}
		if orig, err := expr.ParseCanonical(w.If); err == nil {
			j.If = orig
		}
	}
}
