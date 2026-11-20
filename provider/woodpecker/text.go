package woodpecker

import (
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	wpexpr "github.com/lukaszraczylo/pipemaster/expr/woodpecker"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const dollar = "$"

// texts reads Woodpecker text. Woodpecker substitutes ${NAME} in the raw YAML and turns $$ into a literal $.
// A name that is a matrix axis, a built-in or a plain env name becomes a canonical reference; any other form
// (modifiers such as ${X##v}) stays shell text.
type texts struct {
	axes map[string]bool
	// kept lists the ${...} forms left as written.
	kept []string
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

func (x *texts) ref(name string) (expr.Ref, bool) {
	if x.axes[name] {
		return expr.Ref{Path: []string{expr.PrefixMatrix, name}}, true
	}
	e, err := wpexpr.Parse(name)
	if err != nil {
		return expr.Ref{}, false
	}
	r, ok := e.(expr.Ref)
	return r, ok
}

// escapedBuiltin reads {NAME} after "$$" as the built-in variable the shell reads at run time, which is how a
// GitLab script writes the same reference.
func (x *texts) escapedBuiltin(s string) (r expr.Ref, whole string, ok bool) {
	end := strings.IndexByte(s, '}')
	if !strings.HasPrefix(s, "{") || end < 0 {
		return expr.Ref{}, "", false
	}
	name := s[1:end]
	if !isIdent(name) || x.axes[name] {
		return expr.Ref{}, "", false
	}
	e, err := wpexpr.Parse(name)
	if err != nil {
		return expr.Ref{}, "", false
	}
	r, ok = e.(expr.Ref)
	if !ok || len(r.Path) == 0 || r.Path[0] == expr.PrefixEnv || r.Path[0] == wpexpr.Dialect {
		return expr.Ref{}, "", false
	}
	return r, s[:end+1], true
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
		switch {
		case strings.HasPrefix(s[i:], "$$"):
			if r, whole, ok := x.escapedBuiltin(s[i+2:]); ok {
				flush()
				parts = append(parts, expr.Part{Expr: r})
				i += 2 + len(whole)
				break
			}
			lit.WriteString(dollar)
			i += 2
		case strings.HasPrefix(s[i:], "${"):
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				lit.WriteString(s[i:])
				i = len(s)
				break
			}
			whole, name := s[i:i+end+1], s[i+2:i+end]
			if r, ok := x.ref(name); ok && isIdent(name) {
				flush()
				parts = append(parts, expr.Part{Expr: r})
			} else {
				lit.WriteString(whole)
				x.kept = append(x.kept, whole)
			}
			i += end + 1
		default:
			lit.WriteByte(s[i])
			i++
		}
	}
	flush()
	return parts
}

// template reads s as canonical script text.
func (x *texts) template(s string) (string, error) {
	if !strings.Contains(s, dollar) && !strings.Contains(s, "${{") {
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
func unescape(s string) string { return strings.ReplaceAll(s, "$$", dollar) }
