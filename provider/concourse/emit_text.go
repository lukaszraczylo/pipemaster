package concourse

import (
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprconcourse "github.com/lukaszraczylo/pipemaster/expr/concourse"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	varPrefix     = "PM_VAR_"
	preludeSuffix = " # pipemaster:ctx"
	gitDir        = ".git/"
)

var (
	identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	nameRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
)

// ctxFiles maps the canonical context values that the git resource writes into its checkout to the file.
var ctxFiles = map[string]string{expr.CtxBranch: gitDir + "branch", expr.CtxSHA: gitDir + "commit"}

type param struct{ name, value string }

// scope collects what the text of one task needs: params, and the lines that read context files.
type scope struct {
	jx      *jobEmitter
	prefix  string
	names   map[string]bool
	intent  map[string]bool
	ctxSeen map[string]bool
	params  []param
	prelude []string
	// ps is true when the task runs PowerShell, so a reference is $env:NAME and no sh text may be written.
	ps bool
	// prefixOK is true when the task directory has a fixed place, so a relative path reaches the workspace.
	prefixOK bool
}

// newScope starts the scope of a task whose run.dir is dir.
func (jx *jobEmitter) newScope(dir string) *scope {
	s := &scope{jx: jx, names: map[string]bool{}, intent: map[string]bool{}, ctxSeen: map[string]bool{}}
	s.prefix, s.prefixOK = ctxPath(dir)
	return s
}

// ctxOK reports whether the task can read a context file: the directory has a place, and a branch file exists
// only when the primary source follows a branch.
func (s *scope) ctxOK(file string) bool {
	if !s.prefixOK || s.ps {
		return false
	}
	if file == ctxFiles[expr.CtxBranch] {
		_, tagged := srcOf(s.jx.pe.primary.def)["tag_filter"]
		return !tagged
	}
	return true
}

func (s *scope) add(name, value string) {
	if s.names[name] {
		return
	}
	s.names[name] = true
	s.params = append(s.params, param{name, value})
}

// snapshot and restore undo what one line added, so a line that cannot be written leaves no param behind.
type scopeState struct {
	names   map[string]bool
	intent  map[string]bool
	ctxSeen map[string]bool
	params  int
	prelude int
}

func (s *scope) snapshot() scopeState {
	cp := func(m map[string]bool) map[string]bool {
		out := make(map[string]bool, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	return scopeState{names: cp(s.names), intent: cp(s.intent), ctxSeen: cp(s.ctxSeen), params: len(s.params), prelude: len(s.prelude)}
}

func (s *scope) restore(h scopeState) {
	s.names, s.intent, s.ctxSeen = h.names, h.intent, h.ctxSeen
	s.params, s.prelude = s.params[:h.params], s.prelude[:h.prelude]
}

// ctxPath is the path from the task directory to the workspace, or false when the task directory has no fixed
// place under the task root.
func ctxPath(dir string) (string, bool) {
	if dir == "" {
		return workspaceName + "/", true
	}
	if strings.HasPrefix(dir, "/") || strings.Contains(dir, varOpen) {
		return "", false
	}
	depth := 0
	for _, seg := range strings.Split(dir, "/") {
		switch seg {
		case "", ".":
		case "..":
			return "", false
		default:
			depth++
		}
	}
	return strings.Repeat("../", depth) + workspaceName + "/", true
}

// preludeLine reads one context file into its variable. The suffix lets the parser take the line out again.
func preludeLine(env, file string) string {
	return env + `="$(cat ` + file + `)"; export ` + env + preludeSuffix
}

// refVar gives the shell variable that holds a canonical reference and records the param or prelude line that fills it.
func (s *scope) refVar(r expr.Ref) (string, bool) {
	jx := s.jx
	if _, ok := ctxparam.Param(r.Path); ok {
		file, known := ctxFiles[strings.Join(r.Path, ".")]
		if !known || !s.ctxOK(file) {
			return "", false
		}
		env, ok := exprconcourse.EnvName(r)
		if !ok {
			return "", false
		}
		if !s.ctxSeen[env] {
			s.ctxSeen[env] = true
			s.prelude = append(s.prelude, preludeLine(env, s.prefix+file))
		}
		return env, true
	}
	switch {
	case len(r.Path) == 2 && r.Path[0] == expr.PrefixMatrix:
		v, ok := jx.matrixValue(r.Path[1])
		env, ename := exprconcourse.EnvName(r)
		if !ok || !ename {
			return "", false
		}
		s.add(env, v)
		return env, true
	case len(r.Path) == 2 && r.Path[0] == expr.PrefixEnv && identRE.MatchString(r.Path[1]):
		return r.Path[1], true
	case len(r.Path) == 2 && (r.Path[0] == expr.PrefixSecrets || r.Path[0] == expr.PrefixVars) && identRE.MatchString(r.Path[1]):
		name := r.Path[1]
		isSecret := r.Path[0] == expr.PrefixSecrets
		if s.names[name] && !s.sameVar(name, "(("+r.Path[1]+"))") {
			name = varPrefix + name
		}
		s.add(name, "(("+r.Path[1]+"))")
		s.intent[name] = isSecret
		return name, true
	}
	return "", false
}

func (s *scope) sameVar(name, value string) bool {
	for _, p := range s.params {
		if p.name == name {
			return p.value == value
		}
	}
	return false
}

// convert writes canonical template text as shell text: a reference becomes ${NAME}. lost holds the canonical
// text of each expression with no spelling.
func (s *scope) convert(parts []expr.Part) (out string, lost []string) {
	var sb strings.Builder
	for _, p := range parts {
		switch x := p.Expr.(type) {
		case nil:
			sb.WriteString(p.Text)
		case expr.Lit:
			sb.WriteString(litText(x))
		case expr.Ref:
			if v, ok := s.refVar(x); ok && s.ps {
				sb.WriteString("$env:" + v)
			} else if ok {
				sb.WriteString("${" + v + "}")
			} else {
				lost = append(lost, expr.String(x))
			}
		default:
			lost = append(lost, expr.String(p.Expr))
		}
	}
	return sb.String(), lost
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

// scriptLine converts one script line. why is empty when the line stays; otherwise the caller comments out the
// whole statement around the line.
func (s *scope) scriptLine(path, line string) (out, why string, err error) {
	line = emitkit.NeutralizeResultWriteWhy(s.jx.pe.e.lose, path, line, "Concourse does not read")
	parts, err := expr.ParseScriptTemplate(line)
	if err != nil {
		return "", "", err
	}
	held := s.snapshot()
	out, lost := s.convert(parts)
	switch {
	case len(lost) > 0:
		s.restore(held)
		return "", "no Concourse spelling for expression " + strings.Join(lost, ", "), nil
	case strings.Contains(out, varOpen):
		s.restore(held)
		return "", "the line holds ((, which Concourse reads as a credential variable and no script may carry", nil
	}
	return out, "", nil
}

// scriptLines converts the lines of a script; a statement with no spelling is commented out whole.
func (s *scope) scriptLines(path string, script []string) []string {
	e := s.jx.pe.e
	lines := make([]string, len(script))
	why := make([]string, len(script))
	for i, l := range script {
		out, w, err := s.scriptLine(path, l)
		if err != nil {
			why[i] = "the text cannot be read: " + err.Error()
			continue
		}
		lines[i], why[i] = out, w
	}
	lost := make([]bool, len(script))
	for i, w := range why {
		lost[i] = w != ""
	}
	spans := emitkit.CommentSpans(script, lost)
	var out []string
	for i := 0; i < len(script); i++ {
		var sp *emitkit.Span
		for k := range spans {
			if spans[k].From == i {
				sp = &spans[k]
			}
		}
		if sp == nil {
			out = append(out, lines[i])
			continue
		}
		first := ""
		for k := sp.From; k <= sp.To; k++ {
			if first == "" {
				first = why[k]
			}
			out = append(out, lostPrefix+commentSafe(script[k]))
		}
		e.lose(path, caps.ScriptInterpolation, emitkit.SpanDetail(first, sp.To-sp.From+1, sp.Open))
		i = sp.To
	}
	return out
}

// valueText writes an env value as the text of a task param: a reference to a secret or var becomes ((name)) and
// a matrix reference ((.:axis)). ok is false when the value has no spelling.
func (s *scope) valueText(path, name string, v ir.Value) (string, bool) {
	var parts []expr.Part
	switch v.Kind {
	case ir.ValueSecret:
		if !nameRE.MatchString(v.Secret) {
			s.jx.pe.e.lose(path, caps.ConfigName, "the secret name "+v.Secret+" is not a Concourse variable name; the variable "+name+" is not written")
			return "", false
		}
		s.intent[name] = true
		return "((" + v.Secret + "))", true
	case ir.ValueExpr:
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
	var sb strings.Builder
	var lost []string
	for _, p := range parts {
		switch x := p.Expr.(type) {
		case nil:
			if strings.Contains(p.Text, varOpen) {
				lost = append(lost, "text with (( ")
			}
			sb.WriteString(p.Text)
		case expr.Lit:
			sb.WriteString(litText(x))
		case expr.Ref:
			t, secret, ok := s.paramRef(x)
			if !ok {
				lost = append(lost, expr.String(x))
				continue
			}
			sb.WriteString(t)
			if len(parts) == 1 && secret != nil {
				s.intent[name] = *secret
			}
		default:
			lost = append(lost, expr.String(p.Expr))
		}
	}
	e := s.jx.pe.e
	if len(lost) > 0 {
		e.lose(path, caps.ExprOpaque, "no Concourse spelling for "+strings.Join(lost, ", ")+" in the value of "+name+"; the variable is not written")
		e.comment(path, "env "+name+": "+strings.Join(lost, ", "))
		return "", false
	}
	out := sb.String()
	if v.Kind == ir.ValueLit && strings.Contains(out, varOpen) {
		e.lose(path, caps.StepEnv, "the value of "+name+" holds ((, which Concourse reads as a credential variable; the variable is not written")
		return "", false
	}
	return out, true
}

// paramRef spells a reference inside a task param; secret says whether a var reference is a secret.
func (s *scope) paramRef(r expr.Ref) (text string, secret *bool, ok bool) {
	switch {
	case len(r.Path) == 2 && (r.Path[0] == expr.PrefixSecrets || r.Path[0] == expr.PrefixVars) && nameRE.MatchString(r.Path[1]):
		b := r.Path[0] == expr.PrefixSecrets
		return "((" + r.Path[1] + "))", &b, true
	case len(r.Path) == 2 && r.Path[0] == expr.PrefixMatrix:
		v, ok := s.jx.matrixValue(r.Path[1])
		return v, nil, ok
	}
	return "", nil, false
}

// envParams writes variables as task params in order; a name already in the scope is skipped.
func (s *scope) envParams(path string, vs []ir.Var) {
	for _, v := range vs {
		if s.names[v.Name] {
			continue
		}
		if t, ok := s.valueText(path, v.Name, v.Value); ok {
			s.add(v.Name, t)
		}
	}
}
