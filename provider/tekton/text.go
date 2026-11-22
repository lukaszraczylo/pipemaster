package tekton

import (
	"regexp"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	exprtekton "github.com/lukaszraczylo/pipemaster/expr/tekton"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	varOpen  = "$("
	varClose = ")"
)

var (
	// tektonVar is the first segment of every variable that Tekton substitutes; `$(date)` and the like are shell.
	tektonVar = regexp.MustCompile(`^(params|tasks|results|workspaces|context|steps|credentials|artifacts)\.`)
	// paramVar reads the three spellings of a param variable.
	paramVar = regexp.MustCompile(`^params(?:\.([A-Za-z_][A-Za-z0-9_-]*)|\['([^']+)'\]|\["([^"]+)"\])$`)
)

// texts reads Tekton text. Tekton substitutes $(...) with no escaping. A param, a result or a context value
// becomes a canonical reference; any other Tekton variable stays shell text. A Task scope resolves $(params.x)
// through what the PipelineTask binds to x.
type texts struct {
	// bind holds the parts that the PipelineTask binds to each Task param.
	bind map[string][]expr.Part
	// defaults holds the default of each Task param that has a string default.
	defaults map[string]string
	// matrix names the Task params that a matrix axis binds.
	matrix map[string]bool
	// used names the bound params that a text read.
	used map[string]bool
	// needs names the tasks that a result reference points at, by Tekton name.
	needs map[string]bool
	// taskID maps a Tekton task name to the job ID; ok is false for a name that is no task of the pipeline.
	taskID func(name string) (string, bool)
	// kept lists the variables left as written.
	kept []string
	// task is true in a Task body, where a param name resolves through bind.
	task bool
}

func newTexts(taskID func(string) (string, bool), task bool) *texts {
	return &texts{
		bind: map[string][]expr.Part{}, defaults: map[string]string{}, matrix: map[string]bool{},
		used: map[string]bool{}, needs: map[string]bool{}, taskID: taskID, task: task,
	}
}

func (x *texts) keep(form string) {
	for _, k := range x.kept {
		if k == form {
			return
		}
	}
	x.kept = append(x.kept, form)
}

// unkeep forgets a variable that a collapsed marker accounts for.
func (x *texts) unkeep(form string) {
	x.kept = slices.DeleteFunc(x.kept, func(k string) bool { return k == form })
}

// ref reads one whole variable as a canonical reference.
func (x *texts) ref(word string) (expr.Ref, bool) {
	r, ok := exprtekton.FromWord(word)
	if !ok {
		return expr.Ref{}, false
	}
	if len(r.Path) == 4 && r.Path[0] == expr.PrefixNeeds {
		id, known := x.taskID(r.Path[1])
		if !known {
			return expr.Ref{}, false
		}
		x.needs[r.Path[1]] = true
		return expr.Ref{Path: []string{expr.PrefixNeeds, id, r.Path[2], r.Path[3]}}, true
	}
	return r, true
}

// variable reads the inside of one $(...) as parts; ok is false when the form stays text.
func (x *texts) variable(inner string) ([]expr.Part, bool) {
	if m := paramVar.FindStringSubmatch(inner); m != nil {
		name := m[1] + m[2] + m[3]
		if !x.task {
			if r, ok := x.ref(varOpen + "params." + name + varClose); ok {
				return []expr.Part{{Expr: r}}, true
			}
			return nil, false
		}
		return x.taskParam(name)
	}
	if rest, ok := strings.CutPrefix(inner, "tasks."); ok && !x.task {
		if r, ok := x.ref(varOpen + "tasks." + rest + varClose); ok {
			return []expr.Part{{Expr: r}}, true
		}
	}
	return nil, false
}

// taskParam resolves a param inside a Task body: a bound value, a matrix axis, a default, or a context param.
func (x *texts) taskParam(name string) ([]expr.Part, bool) {
	if parts, ok := x.bind[name]; ok {
		x.used[name] = true
		return append([]expr.Part(nil), parts...), true
	}
	if x.matrix[name] {
		return []expr.Part{{Expr: expr.Ref{Path: []string{expr.PrefixMatrix, name}}}}, true
	}
	if d, ok := x.defaults[name]; ok {
		return []expr.Part{{Text: d}}, true
	}
	if path, ok := ctxparam.Ref(name); ok {
		return []expr.Part{{Expr: expr.Ref{Path: path}}}, true
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
		if !strings.HasPrefix(s[i:], varOpen) {
			lit.WriteByte(s[i])
			i++
			continue
		}
		end := strings.Index(s[i:], varClose)
		if end < 0 {
			lit.WriteString(s[i:])
			break
		}
		inner := s[i+len(varOpen) : i+end]
		if !tektonVar.MatchString(inner) {
			lit.WriteString(varOpen)
			i += len(varOpen)
			continue
		}
		if got, ok := x.variable(inner); ok {
			flush()
			parts = append(parts, got...)
		} else {
			lit.WriteString(s[i : i+end+1])
			x.keep(s[i : i+end+1])
		}
		i += end + 1
	}
	flush()
	return expr.MergeText(parts)
}

// template reads s as canonical script text.
func (x *texts) template(s string) (string, error) {
	if !strings.Contains(s, "$(") && !strings.Contains(s, "${{") {
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
