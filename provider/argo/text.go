package argo

import (
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprargo "github.com/lukaszraczylo/pipemaster/expr/argo"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	tagOpen        = "{{"
	tagClose       = "}}"
	inputParamHead = "inputs.parameters."
)

// texts reads Argo text. Argo substitutes {{...}} with no escaping. A workflow parameter, a task output or a
// context parameter becomes a canonical reference; any other tag stays text as written. In a template scope,
// {{inputs.parameters.x}} resolves through the argument that the task binds to x.
type texts struct {
	// bind holds the parts that the task binds to each template input.
	bind map[string][]expr.Part
	// defaults holds the default of each template input that has one.
	defaults map[string]string
	// used names the bound inputs that a text read.
	used map[string]bool
	// needs names the tasks that an output reference points at, by Argo name.
	needs map[string]bool
	// taskID maps an Argo task name to the job ID; ok is false for a name that is no task of the workflow.
	taskID func(name string) (string, bool)
	// kept lists the tags left as written.
	kept []string
	// tmpl is true in the text of a template, where an input name resolves through bind.
	tmpl bool
}

func newTexts(taskID func(string) (string, bool), tmpl bool) *texts {
	return &texts{
		bind: map[string][]expr.Part{}, defaults: map[string]string{}, used: map[string]bool{},
		needs: map[string]bool{}, taskID: taskID, tmpl: tmpl,
	}
}

func (x *texts) keep(tag string) {
	if !slices.Contains(x.kept, tag) {
		x.kept = append(x.kept, tag)
	}
}

func (x *texts) scope() exprargo.Scope {
	if x.tmpl {
		return exprargo.ScopeTemplate
	}
	return exprargo.ScopeTask
}

// tag reads one whole tag as parts; ok is false when the tag stays text.
func (x *texts) tag(tag, inner string) ([]expr.Part, bool) {
	if strings.HasPrefix(inner, "=") {
		return nil, false
	}
	if x.tmpl {
		if name, ok := strings.CutPrefix(inner, inputParamHead); ok {
			return x.input(name)
		}
	}
	r, ok := exprargo.FromTag(tag, x.scope())
	if !ok || len(r.Path) == 0 || r.Path[0] == expr.PrefixMatrix {
		return nil, false
	}
	if len(r.Path) == 4 && r.Path[0] == expr.PrefixNeeds {
		id, known := x.taskID(r.Path[1])
		if !known {
			return nil, false
		}
		x.needs[r.Path[1]] = true
		return []expr.Part{{Expr: expr.Ref{Path: []string{expr.PrefixNeeds, id, r.Path[2], r.Path[3]}}}}, true
	}
	return []expr.Part{{Expr: r}}, true
}

// input resolves a template input: a bound argument, or its default.
func (x *texts) input(name string) ([]expr.Part, bool) {
	if parts, ok := x.bind[name]; ok {
		x.used[name] = true
		return slices.Clone(parts), true
	}
	if d, ok := x.defaults[name]; ok {
		return []expr.Part{{Text: d}}, true
	}
	return nil, false
}

func (x *texts) parts(s string) []expr.Part {
	var parts []expr.Part
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, expr.Part{Text: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(s); {
		if !strings.HasPrefix(s[i:], tagOpen) {
			lit.WriteByte(s[i])
			i++
			continue
		}
		end := strings.Index(s[i:], tagClose)
		if end < 0 {
			lit.WriteString(s[i:])
			break
		}
		whole := s[i : i+end+len(tagClose)]
		if got, ok := x.tag(whole, strings.TrimSpace(whole[len(tagOpen):len(whole)-len(tagClose)])); ok {
			flush()
			parts = append(parts, got...)
		} else {
			lit.WriteString(whole)
			x.keep(whole)
		}
		i += end + len(tagClose)
	}
	flush()
	return expr.MergeText(parts)
}

// template reads s as canonical script text.
func (x *texts) template(s string) (string, error) {
	if !strings.Contains(s, tagOpen) && !strings.Contains(s, "${{") {
		return s, nil
	}
	return expr.PrintScriptTemplate(x.parts(s))
}

// value reads s as a literal, or as an expression when it holds a reference.
func (x *texts) value(s string) ir.Value {
	parts := x.parts(s)
	var lit strings.Builder
	for _, p := range parts {
		if p.Expr != nil {
			return ir.Value{Kind: ir.ValueExpr, Expr: expr.JoinParts(parts)}
		}
		lit.WriteString(p.Text)
	}
	return ir.Value{Lit: lit.String()}
}

// unresolved tells a value that holds a tag with no IR reference.
func unresolved(taskID func(string) (string, bool), val string) bool {
	probe := newTexts(taskID, false)
	probe.parts(val)
	return len(probe.kept) > 0
}
