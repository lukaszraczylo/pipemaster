package tekton

import (
	"maps"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	exprtekton "github.com/lukaszraczylo/pipemaster/expr/tekton"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// shellMark brackets a reference in converted script text until resolve writes the shell variable.
const shellMark = "\x00"

const (
	secretStore = "ci-secrets"
	configStore = "ci-vars"
	paramPrefix = "$(params."
	paramClose  = ")"
)

// tektonVarText finds a Tekton variable in literal text. Tekton replaces it in script text with no escaping and
// rejects one that names no param, so a literal one is reported; `$(date)` and the like are shell and pass.
var tektonVarText = regexp.MustCompile(`\$\((params|tasks|results|workspaces|context|steps|credentials|artifacts)\.[^)]*\)`)

type textMode uint8

const (
	// scriptText is shell text: a context value is read from the step env, never substituted into it.
	scriptText textMode = iota
	// valueText is an env value, an image or a path: Tekton substitution is safe there.
	valueText
)

// stepScope collects the env entries that the text of one step needs.
type stepScope struct {
	names map[string]bool
	// shell maps the reference of each env, secret or variable that the script reads as a shell variable to that variable.
	shell map[string]string
	env   []*yaml.Node
}

func newScope() *stepScope { return &stepScope{names: map[string]bool{}, shell: map[string]string{}} }

// shellVar returns the placeholder for a reference that the script reads as the shell variable name; resolve
// turns it into the variable and records where it stood.
func (s *stepScope) shellVar(ref, name string) string {
	s.shell[ref] = name
	return shellMark + ref + shellMark
}

// scopeState is a copy of what convert adds to a stepScope.
type scopeState struct {
	names map[string]bool
	shell map[string]string
	env   int
}

// snapshot copies the scope, so a line that is commented out can leave no binding behind.
func (s *stepScope) snapshot() scopeState {
	return scopeState{names: maps.Clone(s.names), shell: maps.Clone(s.shell), env: len(s.env)}
}

func (s *stepScope) restore(h scopeState) {
	s.names, s.shell, s.env = h.names, h.shell, s.env[:h.env]
}

func (s *stepScope) add(name string, n *yaml.Node) {
	if s.names[name] {
		return
	}
	s.names[name] = true
	s.env = append(s.env, n)
}

// taskCtx is the state of the Task being written for one job.
type taskCtx struct {
	matrix       map[string]bool
	doc          *outDoc
	jp           *jobPlan
	value        map[string]string
	byKey        map[string]string
	taken        map[string]bool
	needed       map[string]bool
	e            *emitter
	pp           *plan
	image        string
	containerRef string
	params       []string
	checkout     bool
	ws           bool
}

func (e *emitter) newTaskCtx(pp *plan, jp *jobPlan) *taskCtx {
	tc := &taskCtx{
		e: e, pp: pp, jp: jp, value: map[string]string{}, byKey: map[string]string{}, taken: map[string]bool{},
		matrix: map[string]bool{}, needed: map[string]bool{},
	}
	if m := jp.j.Matrix; m != nil {
		for _, a := range m.Axes {
			tc.declareMatrix(a.Name)
		}
		for _, c := range m.Include {
			for _, kv := range c {
				tc.declareMatrix(kv.Key)
			}
		}
	}
	return tc
}

func (tc *taskCtx) declareMatrix(name string) {
	if !paramNameRE.MatchString(name) || tc.matrix[name] {
		return
	}
	tc.matrix[name] = true
	tc.taken[name] = true
	tc.params = append(tc.params, name)
	tc.byKey[expr.PrefixMatrix+"."+name] = name
}

// param declares a Task param for a reference and returns its name. value is what the PipelineTask binds.
func (tc *taskCtx) param(key, want, value string) string {
	if n, ok := tc.byKey[key]; ok {
		return n
	}
	name := unique(tc.taken, want, maxName)
	tc.byKey[key] = name
	tc.value[name] = value
	tc.params = append(tc.params, name)
	return name
}

// ctxParam declares the Task param that carries a context param.
func (tc *taskCtx) ctxParam(p string) string {
	tc.pp.ctx[p] = true
	return tc.param("ctx."+p, p, paramPrefix+p+paramClose)
}

func (tc *taskCtx) inputParam(in string) (string, bool) {
	pname, ok := tc.pp.inputs[in]
	if !ok {
		return "", false
	}
	want := pname
	if tc.matrix[pname] {
		want = "input-" + pname
	}
	return tc.param(expr.PrefixInputs+"."+in, want, paramPrefix+pname+paramClose), true
}

// needParam declares the Task param that carries a result of another job.
func (tc *taskCtx) needParam(job, out string) (string, bool) {
	jj, ok := tc.pp.byID[job]
	if !ok || !resultNameRE.MatchString(out) || (!tc.jp.finally && jj.finally) {
		return "", false
	}
	tc.needed[job] = true
	return tc.param("needs."+job+"."+out, "needs-"+jj.tname+"-"+out, "$(tasks."+jj.tname+".results."+out+")"), true
}

func param(name string) string { return paramPrefix + name + paramClose }

// storeRef is an env entry that reads a Secret or ConfigMap key.
func storeRef(name, kind, store, key string) *yaml.Node {
	ref := newMap()
	ref.setStr("name", store)
	ref.setStr("key", key)
	from := newMap()
	from.set(kind, ref.n)
	m := newMap()
	m.setStr("name", name)
	m.set("valueFrom", from.n)
	return m.n
}

const (
	kindSecretRef = "secretKeyRef"
	kindConfigRef = "configMapKeyRef"
)

// storeEnv adds the env entry for a secrets.X or vars.X reference and returns its variable name.
func (tc *taskCtx) storeEnv(sc *stepScope, family, name string) (string, bool) {
	if !isIdent(name) {
		return "", false
	}
	if family == expr.PrefixSecrets {
		sc.add(name, storeRef(name, kindSecretRef, secretStore, name))
		return name, true
	}
	if sc.names[name] {
		name = "PM_VAR_" + name
	}
	sc.add(name, storeRef(name, kindConfigRef, configStore, strings.TrimPrefix(name, "PM_VAR_")))
	return name, true
}

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func isIdent(s string) bool { return identRE.MatchString(s) }

// refText spells one canonical reference in Tekton text; ok is false when Tekton has no spelling.
func (tc *taskCtx) refText(sc *stepScope, m textMode, r expr.Ref) (string, bool) {
	if p, ok := ctxparam.Param(r.Path); ok {
		name := tc.ctxParam(p)
		if m == valueText {
			return param(name), true
		}
		env, ok := exprtekton.EnvName(r)
		if !ok {
			return "", false
		}
		sc.add(env, nameValue(env, param(name)))
		return "${" + env + "}", true
	}
	switch {
	case len(r.Path) == 2 && r.Path[0] == expr.PrefixInputs:
		if n, ok := tc.inputParam(r.Path[1]); ok {
			return param(n), true
		}
	case len(r.Path) == 2 && r.Path[0] == expr.PrefixMatrix:
		if tc.matrix[r.Path[1]] {
			return param(r.Path[1]), true
		}
	case len(r.Path) == 4 && r.Path[0] == expr.PrefixNeeds && r.Path[2] == "outputs":
		if n, ok := tc.needParam(r.Path[1], r.Path[3]); ok {
			return param(n), true
		}
	case len(r.Path) == 2 && m == scriptText && isIdent(r.Path[1]):
		switch r.Path[0] {
		case expr.PrefixEnv:
			return sc.shellVar(r.Path[0]+"."+r.Path[1], r.Path[1]), true
		case expr.PrefixSecrets, expr.PrefixVars:
			if n, ok := tc.storeEnv(sc, r.Path[0], r.Path[1]); ok {
				return sc.shellVar(r.Path[0]+"."+r.Path[1], n), true
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

// convert writes canonical template text as Tekton text. lost holds the canonical text of each expression that
// has no spelling; the caller decides what to do with the text.
func (tc *taskCtx) convert(sc *stepScope, path string, m textMode, parts []expr.Part) (out string, lost []string) {
	var sb strings.Builder
	for _, p := range parts {
		switch x := p.Expr.(type) {
		case nil:
			tc.checkLiteral(path, p.Text)
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
	return sb.String(), lost
}

// checkLiteral reports a Tekton variable that the author wrote: Tekton substitutes or rejects it.
func (tc *taskCtx) checkLiteral(path, lit string) {
	for _, v := range tektonVarText.FindAllString(lit, -1) {
		tc.e.lose(path, caps.ScriptInterpolation, v+" is a Tekton variable; Tekton substitutes it, or rejects it when no param of that name exists")
	}
}

// partsOf splits template text, folding a format() call that the Tekton parser makes for mixed text.
func partsOf(s string) ([]expr.Part, error) { return expr.ParseScriptTemplate(s) }

// text converts template text that must convert whole: an expression with no spelling is reported and the
// feature f of path is lost for it. ok is false then.
func (tc *taskCtx) text(sc *stepScope, path string, f caps.Feature, m textMode, s string) (string, bool, error) {
	parts, err := partsOf(s)
	if err != nil {
		return "", false, err
	}
	out, lost := tc.convert(sc, path, m, parts)
	for _, l := range lost {
		tc.e.lose(path, f, "no Tekton spelling for expression "+l)
		tc.e.comment(path, string(f)+" "+path+": {{ "+l+" }}")
	}
	return out, len(lost) == 0, nil
}

// scriptLine converts one script line. why is empty when the line stays; otherwise it names the expressions that
// have no spelling, and the caller comments out the whole statement around the line, because dropping the part
// could change what the command does.
func (tc *taskCtx) scriptLine(sc *stepScope, path, s string) (out, why string, err error) {
	parts, err := partsOf(s)
	if err != nil {
		return "", "", err
	}
	held := sc.snapshot()
	out, lost := tc.convert(sc, path, scriptText, parts)
	if len(lost) == 0 {
		return out, "", nil
	}
	sc.restore(held)
	return "", "no Tekton spelling for expression " + strings.Join(lost, ", "), nil
}

// valueNode renders an env value. A secret or vars reference becomes a valueFrom entry. nil means the value was lost.
func (tc *taskCtx) valueNode(sc *stepScope, path, name string, v ir.Value) (*yaml.Node, error) {
	switch v.Kind {
	case ir.ValueSecret:
		return storeRef(name, kindSecretRef, secretStore, v.Secret), nil
	case ir.ValueExpr:
		if r, ok := v.Expr.(expr.Ref); ok && len(r.Path) == 2 && (r.Path[0] == expr.PrefixSecrets || r.Path[0] == expr.PrefixVars) {
			kind, store := kindSecretRef, secretStore
			if r.Path[0] == expr.PrefixVars {
				kind, store = kindConfigRef, configStore
			}
			return storeRef(name, kind, store, r.Path[1]), nil
		}
		parts := []expr.Part{{Expr: v.Expr}}
		if c, ok := v.Expr.(expr.Call); ok {
			if mixed, ok := emitkit.UnmixFormat(c); ok {
				parts = mixed
			}
		}
		out, lost := tc.convert(sc, path, valueText, parts)
		for _, l := range lost {
			tc.e.lose(path, caps.ExprOpaque, "no Tekton spelling for expression "+l)
			tc.e.comment(path, "expr.opaque "+path+": "+l)
		}
		if len(lost) > 0 {
			return nil, nil
		}
		return nameValue(name, out), nil
	}
	parts, err := partsOf(v.Lit)
	if err != nil {
		return nil, err
	}
	out, lost := tc.convert(sc, path, valueText, parts)
	for _, l := range lost {
		tc.e.lose(path, caps.ExprOpaque, "no Tekton spelling for expression "+l)
		tc.e.comment(path, "expr.opaque "+path+": "+l)
	}
	if len(lost) > 0 {
		return nil, nil
	}
	return nameValue(name, out), nil
}

// envList renders variables as a Kubernetes env list in order; a name already in sc is skipped.
func (tc *taskCtx) envList(sc *stepScope, path string, vs []ir.Var) ([]*yaml.Node, error) {
	var out []*yaml.Node
	seen := map[string]bool{}
	for _, v := range vs {
		if seen[v.Name] {
			continue
		}
		seen[v.Name] = true
		n, err := tc.valueNode(sc, path, v.Name, v.Value)
		if err != nil {
			return nil, err
		}
		if n != nil {
			out = append(out, n)
		}
	}
	return out, nil
}
