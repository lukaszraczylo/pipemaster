package woodpecker

import (
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	wpexpr "github.com/lukaszraczylo/pipemaster/expr/woodpecker"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	lostPrefix = "# pipemaster:lost "
	fromSecret = "from_secret"
	dollarEsc  = "$$"
)

// textMode says where converted text lands. Woodpecker substitutes ${NAME} in the raw file before it
// parses it, so a value field can hold only names that substitution knows. A command also reaches a shell,
// which reads $$NAME as the run-time variable $NAME.
type textMode uint8

const (
	valueText textMode = iota
	commandText
)

// emitter carries the state of one Emit call: the ledger and the comment lines of the file being written.
type emitter struct {
	led  *emitkit.Ledger
	from *expr.Dialect
	head []string
	// cut is set when an image or workdir lost an expression, so the step in progress must not run.
	cut bool
	// own is true when the project came from Woodpecker, where a name that is not a built-in is a
	// substitution of the global environment.
	own bool
}

// sourceDialect is the expression dialect of a project's origin; nil means Woodpecker semantics already hold.
func sourceDialect(origin string) *expr.Dialect { return expr.SourceFor(origin, wpexpr.Lang) }

func use(path string, f caps.Feature) ir.FeatureUse { return ir.FeatureUse{IRPath: path, Feature: f} }

func (e *emitter) lose(path string, f caps.Feature, detail string) {
	e.led.Note(use(path, f), caps.Support{Level: caps.Lost}, detail)
}

func (e *emitter) emulate(path string, f caps.Feature, strategy, detail string) {
	e.led.Note(use(path, f), caps.Support{Level: caps.Emulated, Strategy: strategy}, detail)
}

func (e *emitter) external(path string, f caps.Feature, strategy, detail string) {
	e.led.Note(use(path, f), caps.Support{Level: caps.External, Strategy: strategy}, detail)
}

// foreignExt reports Ext data of other providers: it is not in the Woodpecker file, only in the round-trip metadata.
func (e *emitter) foreignExt(path string, x ir.Ext) {
	emitkit.ForeignExt(e.led, dialect, "Woodpecker", path, x)
}

// mark writes the marker line of an emulated construct into the file head.
func (e *emitter) mark(f caps.Feature, path string) { e.head = append(e.head, emitkit.Marker(f, path)) }

// comment queues a lost-text line for the file head; "${{" never reaches the output.
func (e *emitter) comment(s string) { e.head = append(e.head, lostPrefix+commentSafe(s)) }

// esc doubles each $ so the text reaches Woodpecker's pre-processor, and then the shell, unchanged.
func esc(s string) string { return strings.ReplaceAll(s, dollar, dollarEsc) }

// bindings are the secrets a step's text reads as run-time variables; the step must map each from a secret.
type bindings struct{ secrets []string }

func (b *bindings) add(name string) {
	if b != nil && !slices.Contains(b.secrets, name) {
		b.secrets = append(b.secrets, name)
	}
}

// refText spells one canonical reference in Woodpecker text. ok is false when Woodpecker has no spelling.
func (e *emitter) refText(b *bindings, mode textMode, path []string) (string, bool) {
	if len(path) == 2 && isIdent(path[1]) {
		switch path[0] {
		case expr.PrefixMatrix, dialect:
			return "${" + path[1] + "}", true
		case expr.PrefixEnv:
			if e.own {
				return "${" + path[1] + "}", true
			}
			fallthrough
		case expr.PrefixSecrets, expr.PrefixVars, expr.PrefixInputs:
			if mode != commandText {
				return "", false
			}
			if path[0] == expr.PrefixSecrets {
				b.add(path[1])
			}
			return "$${" + path[1] + "}", true
		}
	}
	if dp, ok := wpexpr.Vocab.Dialect(path); ok && len(dp) == 1 {
		return "${" + dp[0] + "}", true
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

// convertParts joins the parts as Woodpecker text. lost holds the canonical text of each expression that has no spelling.
func (e *emitter) convertParts(b *bindings, mode textMode, parts []expr.Part) (out string, lost []string) {
	var sb strings.Builder
	for _, p := range parts {
		switch x := p.Expr.(type) {
		case nil:
			sb.WriteString(esc(p.Text))
		case expr.Lit:
			sb.WriteString(esc(litText(x)))
		case expr.Ref:
			if t, ok := e.refText(b, mode, x.Path); ok {
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

// text converts canonical template text. Expressions with no Woodpecker spelling are reported and omitted.
func (e *emitter) text(b *bindings, path string, f caps.Feature, mode textMode, s string) (string, error) {
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil {
		return "", err
	}
	out, lost := e.convertParts(b, mode, parts)
	for _, l := range lost {
		e.lose(path, f, "no Woodpecker spelling for expression "+l)
		e.comment(string(f) + " " + path + ": {{ " + l + " }}")
	}
	if len(lost) > 0 && (f == caps.StepImage || f == caps.StepWorkdir) {
		e.cut = true
	}
	return out, nil
}

// command converts one script entry. An entry with an expression Woodpecker cannot spell is commented out
// whole, because dropping the part could change what the command does.
func (e *emitter) command(b *bindings, path, s string) (out string, keep bool, err error) {
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil {
		return "", false, err
	}
	out, lost := e.convertParts(b, commandText, parts)
	if len(lost) == 0 {
		return out, true, nil
	}
	e.lose(path, caps.ScriptInterpolation, "no Woodpecker spelling for expression "+strings.Join(lost, ", ")+"; the line is commented out")
	e.comment("script.interpolation " + path + ": " + s)
	return "", false, nil
}

// valueNode renders an environment or settings value; nil means the value was lost and must be omitted.
func (e *emitter) valueNode(path string, v ir.Value) (*yaml.Node, error) {
	switch v.Kind {
	case ir.ValueSecret:
		m := newMap()
		m.set(fromSecret, str(v.Secret))
		return m.n, nil
	case ir.ValueExpr:
		parts := []expr.Part{{Expr: v.Expr}}
		if c, ok := v.Expr.(expr.Call); ok {
			if mixed, ok := emitkit.UnmixFormat(c); ok {
				parts = mixed
			}
		}
		out, lost := e.convertParts(nil, valueText, parts)
		for _, l := range lost {
			e.lose(path, caps.ExprOpaque, "no Woodpecker spelling for expression "+l)
			e.comment("expr.opaque " + path + ": " + l)
		}
		if len(lost) > 0 {
			return nil, nil
		}
		return str(out), nil
	}
	return str(esc(v.Lit)), nil
}

// valueCanon renders a value as the canonical text the action registry reads.
func valueCanon(v ir.Value) (string, bool) {
	switch v.Kind {
	case ir.ValueSecret:
		return "${{ secrets." + v.Secret + " }}", true
	case ir.ValueExpr:
		parts := []expr.Part{{Expr: v.Expr}}
		if c, ok := v.Expr.(expr.Call); ok {
			if mixed, ok := emitkit.UnmixFormat(c); ok {
				parts = mixed
			}
		}
		s, err := expr.PrintScriptTemplate(parts)
		return s, err == nil
	}
	return v.Lit, true
}

// envNode renders variables as an environment mapping in order; a name already in have is skipped.
func (e *emitter) envNode(path string, vs []ir.Var, m *wmap) error {
	for _, v := range vs {
		if m.has(v.Name) {
			continue
		}
		n, err := e.valueNode(path, v.Value)
		if err != nil {
			return err
		}
		m.set(v.Name, n)
	}
	return nil
}
