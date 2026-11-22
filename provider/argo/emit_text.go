package argo

import (
	"maps"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprargo "github.com/lukaszraczylo/pipemaster/expr/argo"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	kindSecretRef = "secretKeyRef"
	kindConfigRef = "configMapKeyRef"
	needsPrefix   = "needs-"
	varPrefix     = "PM_VAR_"
)

// reservedTags are the roots that Argo resolves in a tag. A tag that starts with one of them is substituted, or
// rejected when it resolves to nothing. Source: workflow/common/common.go:303 (GlobalVarValidWorkflowVariablePrefix)
// and workflow/validate/validate.go:801-840 (resolveAllVariables) of argo-workflows v4.1.6.
var reservedTags = []string{"item", "steps.", "inputs.", "outputs.", "pod.", "workflow.", "tasks.", "lastRetry.", "retries", "node.", "cronworkflow.", "resourcesDuration."}

type textMode uint8

const (
	// scriptText is shell text: a context value is read from the container env, never substituted into it.
	scriptText textMode = iota
	// valueText is an env value, an image or a path: Argo substitution is safe there.
	valueText
)

// stepScope collects the env entries that the text of one step needs.
type stepScope struct {
	names map[string]bool
	env   []*yaml.Node
}

func newScope() *stepScope { return &stepScope{names: map[string]bool{}} }

func (s *stepScope) add(name string, n *yaml.Node) {
	if s.names[name] {
		return
	}
	s.names[name] = true
	s.env = append(s.env, n)
}

// scopeState is a copy of what convert adds to a stepScope.
type scopeState struct {
	names map[string]bool
	env   int
}

func (s *stepScope) snapshot() scopeState {
	return scopeState{names: maps.Clone(s.names), env: len(s.env)}
}

func (s *stepScope) restore(h scopeState) { s.names, s.env = h.names, s.env[:h.env] }

// jobCtx is the state of the template being written for one job.
type jobCtx struct {
	e        *emitter
	pp       *plan
	jp       *jobPlan
	matrix   map[string]bool
	byKey    map[string]string
	taken    map[string]bool
	needed   map[string]bool
	vars     map[string]string
	safeTags map[string]bool
	params   []paramBind
}

type paramBind struct{ name, value string }

func (e *emitter) newJobCtx(pp *plan, jp *jobPlan) *jobCtx {
	return &jobCtx{
		e: e, pp: pp, jp: jp, matrix: map[string]bool{}, byKey: map[string]string{}, taken: map[string]bool{},
		needed: map[string]bool{}, safeTags: map[string]bool{}, vars: map[string]string{},
	}
}

func (tc *jobCtx) bind(key, want, value string) string {
	if n, ok := tc.byKey[key]; ok {
		return n
	}
	name := unique(tc.taken, want, maxName)
	tc.byKey[key] = name
	tc.params = append(tc.params, paramBind{name: name, value: value})
	return name
}

// declareMatrix makes an axis a template input that the task binds to the loop item.
func (tc *jobCtx) declareMatrix(name string) bool {
	if !paramNameRE.MatchString(name) {
		return false
	}
	if !tc.matrix[name] {
		tc.matrix[name] = true
		tc.taken[name] = true
		tc.params = append(tc.params, paramBind{name: name, value: "{{item." + name + "}}"})
		tc.byKey[expr.PrefixMatrix+"."+name] = name
	}
	return true
}

// needParam declares the template input that carries an output of another job. The task binds it to an
// expression with a default, because the output of a skipped task is absent, and an absent value fails the node.
func (tc *jobCtx) needParam(job, out string) (string, bool) {
	jj, ok := tc.pp.byID[job]
	if !ok || !paramNameRE.MatchString(out) || tc.jp.finally != jj.finally {
		return "", false
	}
	ref, ok := exprargo.RefExpr(expr.Ref{Path: []string{expr.PrefixNeeds, jj.task, "outputs", out}}, exprargo.ScopeTask)
	if !ok {
		return "", false
	}
	tc.needed[job] = true
	return tc.bind("needs."+job+"."+out, needsPrefix+jj.task+"-"+out, "{{= "+ref+" }}"), true
}

func (tc *jobCtx) ctxParam(p string) { tc.pp.ctx[p] = true }

// storeRef is an env entry that reads a Secret or ConfigMap key.
func storeRef(name, kind, store, key string) *yaml.Node {
	ref := newMap()
	ref.SetStr("name", store)
	ref.SetStr("key", key)
	from := newMap()
	from.Set(kind, ref.N)
	m := newMap()
	m.SetStr("name", name)
	m.Set("valueFrom", from.N)
	return m.N
}

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func isShellIdent(s string) bool { return identRE.MatchString(s) }

// storeEnv adds the env entry for a secrets.X or vars.X reference and returns its variable name.
func storeEnv(sc *stepScope, family, name string) (string, bool) {
	if !isShellIdent(name) {
		return "", false
	}
	if family == expr.PrefixSecrets {
		sc.add(name, storeRef(name, kindSecretRef, secretStore, name))
		return name, true
	}
	if sc.names[name] {
		name = varPrefix + name
	}
	sc.add(name, storeRef(name, kindConfigRef, configStore, strings.TrimPrefix(name, varPrefix)))
	return name, true
}

// refText spells one canonical reference in Argo text; ok is false when Argo has no spelling.
func (tc *jobCtx) refText(sc *stepScope, m textMode, r expr.Ref) (string, bool) {
	if p, ok := ctxparam.Param(r.Path); ok {
		tc.ctxParam(p)
		tag, ok := exprargo.RefTag(r, exprargo.ScopeTemplate)
		if !ok {
			return "", false
		}
		if m == valueText {
			return tag, true
		}
		env, ok := exprargo.EnvName(r)
		if !ok {
			return "", false
		}
		sc.add(env, nameValue(env, tag))
		tc.vars[env] = strings.Join(r.Path, ".")
		return "${" + env + "}", true
	}
	switch {
	case len(r.Path) == 2 && r.Path[0] == expr.PrefixInputs:
		pname, ok := tc.pp.inputs[r.Path[1]]
		if !ok {
			return "", false
		}
		return exprargo.RefTag(expr.Ref{Path: []string{expr.PrefixInputs, pname}}, exprargo.ScopeTemplate)
	case len(r.Path) == 2 && r.Path[0] == expr.PrefixMatrix:
		if tc.matrix[r.Path[1]] {
			return exprargo.RefTag(r, exprargo.ScopeTemplate)
		}
	case len(r.Path) == 4 && r.Path[0] == expr.PrefixNeeds && r.Path[2] == "outputs":
		if n, ok := tc.needParam(r.Path[1], r.Path[3]); ok {
			return "{{inputs.parameters." + n + "}}", true
		}
	case len(r.Path) == 2 && m == scriptText && isShellIdent(r.Path[1]):
		switch r.Path[0] {
		case expr.PrefixEnv:
			tc.vars[r.Path[1]] = strings.Join(r.Path, ".")
			return "${" + r.Path[1] + "}", true
		case expr.PrefixSecrets, expr.PrefixVars:
			if n, ok := storeEnv(sc, r.Path[0], r.Path[1]); ok {
				tc.vars[n] = strings.Join(r.Path, ".")
				return "${" + n + "}", true
			}
		}
	}
	return "", false
}

func litText(l expr.Lit) string {
	switch l.Kind {
	case expr.LitStr:
		return l.Str
	case expr.LitNull:
		return ""
	}
	return expr.String(l)
}

// tagKinds sorts the tags in literal text: unsafe tags are substituted, evaluated or rejected by Argo, and safe
// tags pass to the shell unchanged. An unterminated {{ is unsafe, because the template engine refuses it.
func tagKinds(s string) (unsafe, safe []string) {
	for i := 0; i < len(s); {
		open := strings.Index(s[i:], tagOpen)
		if open < 0 {
			break
		}
		open += i
		end := strings.Index(s[open:], tagClose)
		if end < 0 {
			unsafe = append(unsafe, s[open:])
			break
		}
		tag := s[open : open+end+len(tagClose)]
		inner := strings.TrimSpace(tag[len(tagOpen) : len(tag)-len(tagClose)])
		bad := strings.HasPrefix(inner, "=")
		for _, p := range reservedTags {
			bad = bad || strings.HasPrefix(inner, p)
		}
		if bad {
			unsafe = append(unsafe, tag)
		} else {
			safe = append(safe, tag)
		}
		i = open + end + len(tagClose)
	}
	return unsafe, safe
}

// convert writes canonical template text as Argo text. lost holds the canonical text of each expression that
// has no spelling; hazards holds each literal tag that Argo would substitute or reject.
func (tc *jobCtx) convert(sc *stepScope, m textMode, parts []expr.Part) (out string, lost, hazards []string) {
	var sb strings.Builder
	for _, p := range parts {
		switch x := p.Expr.(type) {
		case nil:
			unsafe, safe := tagKinds(p.Text)
			hazards = append(hazards, unsafe...)
			for _, s := range safe {
				tc.safeTags[s] = true
			}
			sb.WriteString(p.Text)
		case expr.Lit:
			sb.WriteString(litText(x))
		case expr.Ref:
			if t, ok := tc.refText(sc, m, x); ok {
				sb.WriteString(t)
			} else {
				lost = append(lost, expr.String(x))
			}
		default:
			lost = append(lost, expr.String(p.Expr))
		}
	}
	return sb.String(), lost, hazards
}

// scriptLine converts one script line. why is empty when the line stays; otherwise the caller comments out the
// whole statement around the line, because dropping the part could change what the command does.
func (tc *jobCtx) scriptLine(sc *stepScope, path, s string) (out, why string, err error) {
	s = emitkit.NeutralizeResultWriteWhy(tc.e.lose, path, s, "Argo does not read; it loads an output from a file under "+outputsDir)
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil {
		return "", "", err
	}
	held := sc.snapshot()
	out, lost, hazards := tc.convert(sc, scriptText, parts)
	switch {
	case len(lost) > 0:
		sc.restore(held)
		return "", "no Argo spelling for expression " + strings.Join(lost, ", "), nil
	case len(hazards) > 0:
		sc.restore(held)
		return "", "Argo substitutes, evaluates or rejects the tag " + hazards[0] + " before the shell runs", nil
	}
	return out, "", nil
}

// reportSafeTags records, once per job, the tags that Argo leaves in the text.
func (tc *jobCtx) reportSafeTags() {
	if len(tc.safeTags) == 0 {
		return
	}
	tags := sortedPaths(tc.safeTags)
	tc.e.note(tc.jp.path, caps.ScriptInterpolation, caps.Emulated, "tags pass to the shell",
		"the text holds "+strings.Join(tags, ", ")+": Argo v4.1.6 leaves a tag with no reserved root as written (util/template/simple_template.go:51-55; workflow/validate/validate.go:808-810 skips custom roots), so the shell sees it unchanged")
}

// valueNode renders an env value. A secret or vars reference becomes a valueFrom entry. nil means the value was lost.
func (tc *jobCtx) valueNode(sc *stepScope, path, name string, v ir.Value) *yaml.Node {
	var parts []expr.Part
	switch v.Kind {
	case ir.ValueSecret:
		return storeRef(name, kindSecretRef, secretStore, v.Secret)
	case ir.ValueExpr:
		if r, ok := v.Expr.(expr.Ref); ok && len(r.Path) == 2 && (r.Path[0] == expr.PrefixSecrets || r.Path[0] == expr.PrefixVars) {
			kind, store := kindSecretRef, secretStore
			if r.Path[0] == expr.PrefixVars {
				kind, store = kindConfigRef, configStore
			}
			return storeRef(name, kind, store, r.Path[1])
		}
		parts = []expr.Part{{Expr: v.Expr}}
		if c, ok := v.Expr.(expr.Call); ok {
			if mixed, ok := emitkit.UnmixFormat(c); ok {
				parts = mixed
			}
		}
	default:
		var err error
		if parts, err = expr.ParseScriptTemplate(v.Lit); err != nil {
			parts = []expr.Part{{Text: v.Lit}}
		}
	}
	out, lost, hazards := tc.convert(sc, valueText, parts)
	for _, l := range lost {
		tc.e.lose(path, caps.ExprOpaque, "no Argo spelling for expression "+l)
		tc.e.comment(path, "expr.opaque "+path+": "+l)
	}
	for _, h := range hazards {
		tc.e.lose(path, caps.StepEnv, "the value of "+name+" holds the tag "+h+", which Argo substitutes or rejects; the variable is not written")
	}
	if len(lost) > 0 || len(hazards) > 0 {
		return nil
	}
	return nameValue(name, out)
}

// envList renders variables as a Kubernetes env list in order; a name already in sc is skipped.
func (tc *jobCtx) envList(sc *stepScope, path string, vs []ir.Var) []*yaml.Node {
	var out []*yaml.Node
	seen := map[string]bool{}
	for _, v := range vs {
		if seen[v.Name] {
			continue
		}
		seen[v.Name] = true
		if n := tc.valueNode(sc, path, v.Name, v.Value); n != nil {
			out = append(out, n)
		}
	}
	return out
}
