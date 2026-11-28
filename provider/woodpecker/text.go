package woodpecker

import (
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	wpexpr "github.com/lukaszraczylo/pipemaster/expr/woodpecker"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/container"
	"github.com/lukaszraczylo/pipemaster/vars"
)

const dollar = "$"

// texts reads Woodpecker text. Woodpecker substitutes ${NAME} in the raw YAML and turns $$ into a literal $.
// A name that is a matrix axis, a built-in or a plain env name becomes a canonical reference; any other form
// (modifiers such as ${X##v}) stays shell text.
type texts struct {
	axes map[string]bool
	// kept lists the ${...} forms left as written.
	kept []string
	// lostVars lists the CI_ run-time variables that have no IR reference.
	lostVars []string
}

// markAxes reads the names of the matrix an emitter replaced with plain variables, so text that names one reads
// as a matrix reference.
func (x *texts) markAxes(m *marks) {
	for _, u := range m.uses {
		var stray axesArg
		if u.Feature == caps.ScriptInterpolation && decodeArg(m.args[u], &stray) {
			for _, n := range stray.Axes {
				x.axes[n] = true
			}
		}
		var mx ir.Matrix
		if u.Feature != caps.JobMatrix || !decodeArg(m.args[u], &mx) {
			continue
		}
		for _, a := range mx.Axes {
			x.axes[a.Name] = true
		}
		for _, c := range mx.Include {
			for _, kv := range c {
				x.axes[kv.Key] = true
			}
		}
	}
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter := c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func (x *texts) ref(name string, script bool) (expr.Ref, bool) {
	if x.axes[name] {
		return expr.Ref{Path: []string{expr.PrefixMatrix, name}}, true
	}
	if script && vars.ScriptText(name) {
		return expr.Ref{}, false
	}
	e, err := wpexpr.Parse(name)
	if err != nil {
		return expr.Ref{}, false
	}
	r, ok := e.(expr.Ref)
	return r, ok
}

// shellName reads NAME or {NAME} at the start of s, the forms of a shell variable, and returns the name and the
// length of the form.
func shellName(s string) (name string, n int) {
	if strings.HasPrefix(s, "{") {
		end := strings.IndexByte(s, '}')
		if end < 0 || !isIdent(s[1:end]) {
			return "", 0
		}
		return s[1:end], end + 1
	}
	n = 0
	for n < len(s) && (s[n] == '_' || s[n] >= 'a' && s[n] <= 'z' || s[n] >= 'A' && s[n] <= 'Z' || n > 0 && s[n] >= '0' && s[n] <= '9') {
		n++
	}
	return s[:n], n
}

// runtimeVar reads a shell variable that Woodpecker sets in the step environment. A built-in with a canonical
// name becomes that reference. Any other CI_ name has no IR reference, so it is noted as lost.
func (x *texts) runtimeVar(name string, script bool) (expr.Ref, bool) {
	if !isIdent(name) || x.axes[name] || script && vars.ScriptText(name) {
		return expr.Ref{}, false
	}
	e, err := wpexpr.Parse(name)
	if err != nil {
		return expr.Ref{}, false
	}
	r, ok := e.(expr.Ref)
	switch {
	case !ok || len(r.Path) == 0 || r.Path[0] == expr.PrefixEnv:
	case r.Path[0] == wpexpr.Dialect:
		x.lostVars = append(x.lostVars, name)
	default:
		return r, true
	}
	return expr.Ref{}, false
}

// parts splits s into text and refs. In a script, names whose value depends on the event stay text.
func (x *texts) parts(s string, script bool) []expr.Part {
	var parts []expr.Part
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, expr.Part{Text: lit.String()})
			lit.Reset()
		}
	}
	var mask []bool
	if script {
		mask = vars.Protected(s)
	}
	// quoted reports whether the shell expands nothing at s[i], as inside single quotes.
	quoted := func(i int) bool { return mask != nil && mask[i] }
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "$$"):
			if name, n := shellName(s[i+2:]); n > 0 && !quoted(i) {
				if r, ok := x.runtimeVar(name, script); ok {
					flush()
					parts = append(parts, expr.Part{Expr: r})
					i += 2 + n
					break
				}
			}
			lit.WriteString(dollar)
			i += 2
		case strings.HasPrefix(s[i:], "${"):
			closer := "}"
			if strings.HasPrefix(s[i:], "${{") {
				closer = "}}"
			}
			end := strings.Index(s[i:], closer)
			if end < 0 {
				lit.WriteString(s[i:])
				i = len(s)
				break
			}
			end += len(closer) - 1
			whole, name := s[i:i+end+1], s[i+2:i+end]
			if r, ok := x.ref(name, script); ok && isIdent(name) {
				flush()
				parts = append(parts, expr.Part{Expr: r})
			} else {
				lit.WriteString(whole)
				x.kept = append(x.kept, whole)
			}
			i += end + 1
		case s[i] == '$':
			if name, n := shellName(s[i+1:]); n > 0 && !quoted(i) {
				if r, ok := x.runtimeVar(name, script); ok {
					flush()
					parts = append(parts, expr.Part{Expr: r})
					i += 1 + n
					break
				}
			}
			lit.WriteByte(s[i])
			i++
		default:
			lit.WriteByte(s[i])
			i++
		}
	}
	flush()
	if script {
		parts = foldQuoteBreaks(parts)
	}
	return parts
}

// foldQuoteBreaks undoes the quote break that the emitter writes around a variable in single-quoted text.
func foldQuoteBreaks(parts []expr.Part) []expr.Part {
	for i := 1; i+1 < len(parts); i++ {
		if parts[i].Expr == nil || parts[i-1].Expr != nil || parts[i+1].Expr != nil {
			continue
		}
		if strings.HasSuffix(parts[i-1].Text, vars.QuoteBreakOpen) && strings.HasPrefix(parts[i+1].Text, vars.QuoteBreakClose) {
			parts[i-1].Text = strings.TrimSuffix(parts[i-1].Text, vars.QuoteBreakOpen)
			parts[i+1].Text = strings.TrimPrefix(parts[i+1].Text, vars.QuoteBreakClose)
		}
	}
	return parts
}

// template reads s as canonical script text.
func (x *texts) template(s string) (string, error) {
	if !strings.Contains(s, dollar) && !strings.Contains(s, "${{") {
		return s, nil
	}
	return expr.PrintScriptTemplate(x.parts(s, true))
}

// value reads s as a literal, or as an expression when it holds a reference.
func (x *texts) value(s string) ir.Value {
	parts := x.parts(s, false)
	var lit strings.Builder
	for _, p := range parts {
		if p.Expr != nil {
			return ir.Value{Kind: ir.ValueExpr, Expr: mixFormat(parts)}
		}
		lit.WriteString(p.Text)
	}
	return ir.Value{Lit: lit.String()}
}

// mixFormat joins parts into one expression: a lone reference stays itself, text around references becomes
// format('..{0}..', args) with literal braces doubled.
func mixFormat(parts []expr.Part) expr.Expr {
	if len(parts) == 1 && parts[0].Expr != nil {
		return parts[0].Expr
	}
	var pat strings.Builder
	args := []expr.Expr{nil}
	for _, p := range parts {
		if p.Expr == nil {
			pat.WriteString(strings.NewReplacer("{", "{{", "}", "}}").Replace(p.Text))
			continue
		}
		pat.WriteString("{" + strconv.Itoa(len(args)-1) + "}")
		args = append(args, p.Expr)
	}
	args[0] = expr.Lit{Kind: expr.LitStr, Str: pat.String()}
	return expr.Call{Name: "format", Args: args}
}

// unescape turns $$ into $ and leaves ${...} alone, for text that has no IR substitution form (when values).
func unescape(s string) string { return container.Unescape(s, dollarEsc) }
