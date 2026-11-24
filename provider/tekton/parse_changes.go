package tekton

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
)

// withoutChangesTask drops the PipelineTask that pipemaster generated to compute path predicates: the job
// conditions that read its results come back as the source condition.
func withoutChangesTask(tasks []*omap) []*omap {
	var out []*omap
	for _, m := range tasks {
		name, _ := text(m.Vals["name"])
		if ref, ok := asMap(m.Vals["taskRef"]); ok && name == changesTask {
			if rn, _ := text(ref.Vals["name"]); strings.HasSuffix(rn, "-"+changesTask) {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// collapseChanges restores the source condition of a job whose when entries are still the generated ones.
func (b *jobBuilder) collapseChanges() {
	var a changesArg
	if !b.marks.arg(caps.JobIf, b.path, &a) || a.Head == "" {
		return
	}
	src, err := expr.ParseCanonical(a.Src)
	parts := []expr.Expr(nil)
	if b.job.If != nil {
		parts = conjuncts(b.job.If)
	}
	if err != nil || len(parts) == 0 || expr.String(parts[0]) != a.Head {
		b.ignored(b.path, caps.JobIf, "the changed-paths condition")
		return
	}
	var rest expr.Expr
	for _, p := range parts[1:] {
		if rest == nil {
			rest = p
		} else {
			rest = expr.Binary{Op: expr.OpAnd, L: rest, R: p}
		}
	}
	back := ""
	if rest != nil {
		back = expr.String(rest)
	}
	if back != a.Back {
		b.ignored(b.path, caps.JobIf, "the changed-paths condition")
		return
	}
	b.job.If = src
}
