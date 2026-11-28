package gha

import (
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/vars"
)

const (
	refEnvPrefix = "PIPEMASTER_REF_"
	quoteOpen    = `'"`
	quoteClose   = `"'`
)

// untrustedRefs are the canonical refs whose value a contributor picks. GHA pastes an expression into the script
// text before the shell reads it, so a branch named a";touch pwn;" would run as code.
var untrustedRefs = map[string]bool{
	expr.CtxBranch: true, expr.CtxTag: true, expr.CtxRefName: true, expr.CtxRef: true,
	expr.CtxPRHeadRef: true, expr.CtxPRBaseRef: true,
}

// bindRefEnv moves the untrusted refs of the scripts that came from another provider into step variables. The
// script reads the variable, which the shell never parses as code.
func bindRefEnv(p *ir.Project, target string) *ir.Project {
	if p == nil || provider.SameFamily(p.Origin.Provider, target) {
		return p
	}
	cp := *p
	cp.Pipelines = make([]*ir.Pipeline, len(p.Pipelines))
	for i, pl := range p.Pipelines {
		if pl == nil {
			continue
		}
		npl := *pl
		npl.Jobs = bindJobs(pl.Jobs)
		npl.Finally = bindJobs(pl.Finally)
		cp.Pipelines[i] = &npl
	}
	return &cp
}

func bindJobs(jobs []*ir.Job) []*ir.Job {
	if jobs == nil {
		return nil
	}
	out := make([]*ir.Job, len(jobs))
	for i, j := range jobs {
		if j == nil {
			continue
		}
		nj := *j
		nj.Steps = make([]*ir.Step, len(j.Steps))
		for k, st := range j.Steps {
			nj.Steps[k] = bindStep(st)
		}
		out[i] = &nj
	}
	return out
}

func bindStep(st *ir.Step) *ir.Step {
	// An image step passes its script to the container as data, so no shell parses an expression there.
	if st == nil || len(st.Script) == 0 || st.Image != nil {
		return st
	}
	var binds []ir.Var
	index := map[string]string{}
	script := make([]string, len(st.Script))
	for i, line := range st.Script {
		script[i] = line
		parts, err := expr.ParseScriptTemplate(line)
		if err != nil {
			continue
		}
		var out strings.Builder
		changed := false
		for _, part := range parts {
			ref, ok := part.Expr.(expr.Ref)
			if part.Expr == nil || !ok || !untrustedRefs[strings.Join(ref.Path, ".")] {
				if part.Expr == nil {
					out.WriteString(part.Text)
				} else if txt, err := expr.PrintScriptTemplate([]expr.Part{part}); err == nil {
					out.WriteString(txt)
				}
				continue
			}
			key := strings.Join(ref.Path, ".")
			name, seen := index[key]
			if !seen {
				name = refEnvPrefix + strconv.Itoa(len(binds)+1)
				index[key] = name
				binds = append(binds, ir.Var{Name: name, Value: ir.Value{Kind: ir.ValueExpr, Expr: ref}})
			}
			cur := out.String()
			cur = cur[strings.LastIndexByte(cur, '\n')+1:]
			if vars.Protected(cur + "$")[len(cur)] {
				out.WriteString(quoteOpen + "$" + name + quoteClose)
			} else {
				out.WriteString("${" + name + "}")
			}
			changed = true
		}
		if changed {
			script[i] = out.String()
		}
	}
	if len(binds) == 0 {
		return st
	}
	cp := *st
	cp.Script = script
	cp.Env = append(append([]ir.Var(nil), st.Env...), binds...)
	return &cp
}

// foldRefEnv undoes bindRefEnv on a parsed step: each reserved variable goes back into the script as its ref.
func foldRefEnv(s *ir.Step) {
	if s == nil || s.Kind != ir.StepScript {
		return
	}
	kept := s.Env[:0:0]
	for _, v := range s.Env {
		r, ok := v.Value.Expr.(expr.Ref)
		if !strings.HasPrefix(v.Name, refEnvPrefix) || v.Value.Kind != ir.ValueExpr || !ok {
			kept = append(kept, v)
			continue
		}
		txt, err := expr.PrintScriptTemplate([]expr.Part{{Expr: r}})
		if err != nil {
			kept = append(kept, v)
			continue
		}
		for i, line := range s.Script {
			line = strings.ReplaceAll(line, quoteOpen+"$"+v.Name+quoteClose, txt)
			line = strings.ReplaceAll(line, "${"+v.Name+"}", txt)
			s.Script[i] = strings.ReplaceAll(line, "$"+v.Name, txt)
		}
	}
	if len(kept) == 0 {
		kept = nil
	}
	s.Env = kept
}
