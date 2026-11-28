package inline

import (
	"path"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

// hiding is what a GitLab child must not see of its parent: a variable that is not in seen reads as unset.
type hiding struct{ seen map[string]bool }

// hidden says what the child jobs of a GitLab trigger see. GitLab forwards the variables of the trigger job and
// the pipeline file only with forward:yaml_variables, and a variable that a run sets by hand only with
// forward:pipeline_variables. The result is nil when nothing is hidden.
func (f *flattener) hidden(j *ir.Job, child *ir.Pipeline, cj *ir.Job, fwd bool) *hiding {
	if callKind(j) == kindGitLab && forwardsPipelineVars(j) {
		return nil
	}
	h := &hiding{seen: map[string]bool{}}
	for _, vars := range [][]ir.Var{child.Env, cj.Env} {
		for _, v := range vars {
			h.seen[v.Name] = true
		}
	}
	if fwd && callKind(j) == kindGitLab {
		for _, vars := range [][]ir.Var{j.Env, f.pl.Env} {
			for _, v := range vars {
				h.seen[v.Name] = true
			}
		}
	}
	return h
}

func forwardsPipelineVars(j *ir.Job) bool {
	f, _ := trigger(j)[extForward].(map[string]any)
	v, _ := f[extPipelineVars].(bool)
	return v
}

// hiddenEnv sets each parent variable that the child must not see to the empty value, so the child reads it as unset.
func hiddenEnv(h *hiding, parent, own []ir.Var) []ir.Var {
	if h == nil {
		return nil
	}
	var out []ir.Var
	for _, v := range parent {
		if !h.seen[v.Name] && !slices.ContainsFunc(own, func(o ir.Var) bool { return o.Name == v.Name }) {
			out = append(out, ir.Var{Name: v.Name})
		}
	}
	return out
}

var posixShells = []string{"sh", "bash", "dash", "ash", "zsh", "ksh"}

// shellBase names the interpreter of a shell setting or of a shebang line, or "" for a template that holds {0}.
func shellBase(words string) string {
	f := strings.Fields(words)
	if len(f) > 0 && path.Base(f[0]) == "env" {
		f = slices.DeleteFunc(f[1:], func(w string) bool { return strings.HasPrefix(w, "-") })
	}
	if len(f) == 0 || strings.Contains(words, "{0}") {
		return ""
	}
	return path.Base(f[0])
}

// unsetLine writes the command that unsets names in the shell of a step, with ok false when the shell has no such form.
func unsetLine(shell string, names []string) (line string, posix, ok bool) {
	if shell == "" {
		return "unset -v " + strings.Join(names, " "), true, true
	}
	var cmds []string
	switch base := shellBase(shell); {
	case slices.Contains(posixShells, base):
		return "unset -v " + strings.Join(names, " "), true, true
	case base == "pwsh" || base == "powershell":
		for _, n := range names {
			cmds = append(cmds, "Remove-Item Env:"+n+" -ErrorAction Ignore")
		}
	case base == "cmd":
		for _, n := range names {
			cmds = append(cmds, "set "+n+"=")
		}
	default:
		return "", false, false
	}
	return strings.Join(cmds, "\n"), false, true
}

// unsetInScripts makes the script steps of j read each hidden variable as unset, not as empty: the empty value
// differs from unset for ${NAME-x}, ${NAME+x} and test -v. Only the names that the files declare are unset: a value
// that a run sets by hand under an undeclared name still reaches the script.
// A step whose shell has no unset form keeps its script and gets a report entry.
func (f *flattener) unsetInScripts(j *ir.Job, hidden []ir.Var) {
	if len(hidden) == 0 {
		return
	}
	names := make([]string, len(hidden))
	for i, v := range hidden {
		names[i] = v.Name
	}
	for _, st := range j.Steps {
		if st == nil || st.Action != nil || len(st.Script) == 0 || emitkit.IsFailLoud(st.Script) {
			continue
		}
		first, rest, multi := strings.Cut(st.Script[0], "\n")
		shebang := strings.HasPrefix(first, "#!")
		line, posix, ok := unsetLine(st.Shell, names)
		if ok && shebang && (!posix || !slices.Contains(posixShells, shellBase(first[2:]))) {
			ok = false
		}
		if !ok {
			f.res.Entries = append(f.res.Entries, report.Entry{
				IRPath: jobPath(f.pl.ID, j.ID) + "/step/" + st.ID, Feature: caps.StepShell, Level: caps.Emulated, Strategy: EntryStrategy,
				Detail: "the shell of the step has no unset form, so " + strings.Join(names, ", ") + " stay set to the empty value in the script, not unset",
			})
			continue
		}
		switch {
		case !shebang:
			st.Script[0] = line + "\n" + st.Script[0]
		case multi:
			st.Script[0] = first + "\n" + line + "\n" + rest
		}
	}
}

// hideVars reads each variable that the child cannot see as unset in a condition.
func hideVars(e expr.Expr, h *hiding) expr.Expr {
	if h == nil {
		return e
	}
	return expr.FoldConst(expr.MapRefs(e, func(r expr.Ref) expr.Expr {
		if len(r.Path) == 2 && r.Path[0] == expr.PrefixEnv && !h.seen[r.Path[1]] {
			return expr.Lit{Kind: expr.LitNull}
		}
		return r
	}))
}
