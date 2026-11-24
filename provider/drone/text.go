package drone

import (
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	dexpr "github.com/lukaszraczylo/pipemaster/expr/drone"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/container"
	"github.com/lukaszraczylo/pipemaster/vars"
)

const (
	dollar    = "$"
	dollarEsc = "$$"
)

// texts reads Drone text. Drone substitutes ${NAME} in the raw YAML and turns $$ into a literal $: drone/envsubst
// parse/scan.go scanEscaped (lines 193-200, commit 179042472c46) skips a "$" that another "$" follows, and a bare
// $NAME is never substituted. A built-in name becomes a canonical reference; a ${...} form with a modifier stays
// shell text.
type texts struct {
	// kept lists the ${...} forms left as written.
	kept []string
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
	for n < len(s) && (s[n] == '_' || s[n] >= 'a' && s[n] <= 'z' || s[n] >= 'A' && s[n] <= 'Z' || n > 0 && s[n] >= '0' && s[n] <= '9') {
		n++
	}
	return s[:n], n
}

// builtin reads a shell variable that Drone sets in the step environment as its canonical reference. Any other
// name stays text: the IR has no reference for a variable the pipeline does not define.
func builtin(name string, script bool) (expr.Ref, bool) {
	if !isIdent(name) || script && vars.ScriptText(name) {
		return expr.Ref{}, false
	}
	r := dexpr.Ref(name)
	if len(r.Path) == 0 || r.Path[0] == expr.PrefixEnv {
		return expr.Ref{}, false
	}
	return r, true
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
	ref := func(name string) bool {
		r, ok := builtin(name, script)
		if ok {
			flush()
			parts = append(parts, expr.Part{Expr: r})
		}
		return ok
	}
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], dollarEsc):
			if name, n := shellName(s[i+2:]); n > 0 && ref(name) {
				i += 2 + n
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
			if r, ok := builtin(name, script); ok {
				flush()
				parts = append(parts, expr.Part{Expr: r})
			} else {
				lit.WriteString(whole)
				x.kept = append(x.kept, whole)
			}
			i += end + 1
		case s[i] == '$':
			if name, n := shellName(s[i+1:]); n > 0 && ref(name) {
				i += 1 + n
				break
			}
			lit.WriteByte(s[i])
			i++
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
	if !strings.Contains(s, dollar) {
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

// unescape turns $$ into $ and leaves ${...} alone, for text that has no IR substitution form.
func unescape(s string) string { return container.Unescape(s, dollarEsc) }
