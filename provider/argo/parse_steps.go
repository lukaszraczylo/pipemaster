package argo

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprargo "github.com/lukaszraczylo/pipemaster/expr/argo"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// varsArg lists the shell variables that the emitter read in place of an expression, with the reference of each.
type varsArg struct {
	Vars map[string]string `json:"vars"`
}

// collapseSteps puts back what the emitter wrote around the user script: the continue-on-error trap, the
// condition guard, the shell variables that stand for expressions and the output writers.
func (b *jobBuilder) collapseSteps() {
	var va varsArg
	hasVars := markerArg(b.c.src.doc.Head, b.path, caps.ScriptInterpolation, &va)
	for _, st := range b.job.Steps {
		if st.Kind != ir.StepScript || len(st.Script) == 0 {
			continue
		}
		if st.Script[0] == continueTrap {
			st.ContinueOnError, st.Script = true, st.Script[1:]
		}
		if st == b.job.Steps[len(b.job.Steps)-1] {
			b.outputValues(st)
		}
		drop := b.guard(st)
		if hasVars {
			drop = append(drop, b.scriptVars(st, va.Vars)...)
		}
		removeEnv(st, drop)
	}
}

// guard moves a leading guard line into the step condition. It returns the env entries that only the guard read.
func (b *jobBuilder) guard(st *ir.Step) []string {
	lines, cond, ok := exprargo.RemoveGuard(st.Script)
	if !ok {
		return nil
	}
	st.Script, st.If = lines, cond
	env, ok := exprargo.GuardEnv(cond)
	if !ok {
		return nil
	}
	var drop []string
	for _, v := range env {
		if want, ok := exprargo.EnvRef(v.Name); ok && envIs(st, v.Name, want) {
			drop = append(drop, v.Name)
		}
	}
	return drop
}

// envIs is true when the step env entry name holds the reference want.
func envIs(st *ir.Step, name string, want expr.Ref) bool {
	for _, v := range st.Env {
		if v.Name != name {
			continue
		}
		got, isRef := v.Value.Expr.(expr.Ref)
		return v.Value.Kind == ir.ValueExpr && isRef && strings.Join(got.Path, ".") == strings.Join(want.Path, ".")
	}
	return false
}

// storeEnvIs is true when the step env entry name holds the secret or variable that the reference names, as
// the emitter writes it for a script that reads the reference.
func storeEnvIs(st *ir.Step, name string, ref []string) bool {
	if len(ref) != 2 {
		return false
	}
	for _, v := range st.Env {
		if v.Name != name {
			continue
		}
		switch ref[0] {
		case expr.PrefixSecrets:
			return v.Value.Kind == ir.ValueSecret && v.Value.Secret == ref[1]
		case expr.PrefixVars:
			r, isRef := v.Value.Expr.(expr.Ref)
			return v.Value.Kind == ir.ValueExpr && isRef && len(r.Path) == 2 && r.Path[0] == expr.PrefixVars && r.Path[1] == ref[1]
		}
	}
	return false
}

// removeEnv drops the named env entries, unless the script still reads them.
func removeEnv(st *ir.Step, names []string) {
	var keep []ir.Var
	for _, v := range st.Env {
		gone := false
		for _, n := range names {
			gone = gone || n == v.Name
		}
		if !gone {
			keep = append(keep, v)
		}
	}
	st.Env = keep
}

// scriptVars turns ${NAME} back into the expression that it stands for. A context variable needs its env entry
// as the emitter wrote it; without it the text stays and the report says the marker was ignored. It returns the
// context env entries that the restored text no longer needs.
func (b *jobBuilder) scriptVars(st *ir.Step, vars map[string]string) []string {
	var drop []string
	for _, name := range sortedKeys(vars) {
		path := vars[name]
		tok := "${" + name + "}"
		used := false
		for _, l := range st.Script {
			used = used || strings.Contains(l, tok)
		}
		if !used {
			continue
		}
		ref := strings.Split(path, ".")
		if _, isCtx := ctxparam.Param(ref); isCtx {
			if !envIs(st, name, expr.Ref{Path: ref}) {
				b.c.ignored(b.path, caps.ScriptInterpolation, "the env entry "+name+" was edited or removed after pipemaster wrote it; the text "+tok+" stays")
				continue
			}
			drop = append(drop, name)
		}
		if storeEnvIs(st, name, ref) {
			drop = append(drop, name)
		}
		for i, l := range st.Script {
			st.Script[i] = strings.ReplaceAll(l, tok, "${{ "+path+" }}")
		}
	}
	return drop
}

// outputValues reads the lines that open the last step and write the value of each job output. They come before
// the guard, so a skipped step still publishes the outputs.
func (b *jobBuilder) outputValues(st *ir.Step) {
	// The lines follow the order of the outputs, so the first output owns the first line.
	for i := range b.job.Outputs {
		o := &b.job.Outputs[i]
		if len(st.Script) == 0 || st.Script[0] != outputEpilogue(o.Name) {
			continue
		}
		env := outputEnv(o.Name)
		for j, v := range st.Env {
			if text, ok := outputText(v.Value); v.Name == env && ok {
				o.Value = text
				st.Env = append(st.Env[:j], st.Env[j+1:]...)
				st.Script = st.Script[1:]
				break
			}
		}
	}
}

// outputText is the text of an output value that the emitter wrote as an env value: a literal, or text around
// references in the script template form.
func outputText(v ir.Value) (string, bool) {
	switch v.Kind {
	case ir.ValueLit:
		return v.Lit, true
	case ir.ValueExpr:
		parts := []expr.Part{{Expr: v.Expr}}
		if c, ok := v.Expr.(expr.Call); ok {
			if p, ok := emitkit.UnmixFormat(c); ok {
				parts = p
			}
		}
		text, err := expr.PrintScriptTemplate(parts)
		return text, err == nil
	}
	return "", false
}
