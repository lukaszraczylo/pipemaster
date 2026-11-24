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
	"github.com/lukaszraczylo/pipemaster/provider/internal/container"
	"github.com/lukaszraczylo/pipemaster/vars"
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
	// gated holds the action steps that a later step reads, which no target may drop.
	gated map[*ir.Step]bool
	led   *emitkit.Ledger
	// origin is the provider ID the project was parsed from.
	origin string
	from   *expr.Dialect
	// stepIDs maps the path of each step under the ID a reader derives for it to the ID in the IR.
	stepIDs map[string]string
	// inexact is the first trigger filter feature of the workflow being written whose globs changed meaning.
	inexact caps.Feature
	head    []string
	// lostWork holds the path of every step that lost script, action or image work.
	lostWork []string
	// said holds the strategy and detail last noted per emulated use.
	said map[ir.FeatureUse][2]string
	// matrixRefs lists the matrix names the workflow being written reads.
	matrixRefs []string
	// cut is set when an image, workdir or service field lost an expression; the step or service being written owns it.
	cut bool
	// own is true when the project came from Woodpecker, where a name that is not a built-in is a
	// substitution of the global environment.
	own bool
	// facts is what the triggers of the pipeline being written guarantee about its conditions.
	facts expr.Facts
}

// sourceDialect is the expression dialect of a project's origin; nil means Woodpecker semantics already hold.
func sourceDialect(origin string) *expr.Dialect { return expr.SourceFor(origin, wpexpr.Lang) }

func use(path string, f caps.Feature) ir.FeatureUse { return ir.FeatureUse{IRPath: path, Feature: f} }

func (e *emitter) lose(path string, f caps.Feature, detail string) {
	e.noteLostWork(path, f)
	e.noteLost(path, f, detail)
}

func (e *emitter) noteLost(path string, f caps.Feature, detail string) {
	e.led.Note(use(path, f), caps.Support{Level: caps.Lost}, detail)
}

// noteLostWork remembers a step whose work has no Woodpecker form, so an empty job can tell it from one with no work.
func (e *emitter) noteLostWork(path string, f caps.Feature) {
	if strings.Contains(path, "/step/") && (f == caps.StepScript || f == caps.StepImage) {
		e.lostWork = append(e.lostWork, path)
	}
}

// lostStepWork is true when a step of the job at jp lost its work.
func (e *emitter) lostStepWork(jp string) bool {
	return slices.ContainsFunc(e.lostWork, func(p string) bool { return strings.HasPrefix(p, jp+"/step/") })
}

// emulate notes an emulation. A second note for the same path and feature adds its text to the first, so the
// report keeps every caveat.
func (e *emitter) emulate(path string, f caps.Feature, strategy, detail string) {
	u := use(path, f)
	if prev, ok := e.said[u]; ok {
		strategy, detail = joinNew(prev[0], strategy), joinNew(prev[1], detail)
	}
	if e.said == nil {
		e.said = map[ir.FeatureUse][2]string{}
	}
	e.said[u] = [2]string{strategy, detail}
	e.led.Note(u, caps.Support{Level: caps.Emulated, Strategy: strategy}, detail)
}

func joinNew(prev, next string) string {
	if next == "" || strings.Contains(prev, next) {
		return prev
	}
	if prev == "" {
		return next
	}
	return prev + "; " + next
}

func (e *emitter) external(path string, f caps.Feature, strategy, detail string) {
	u := use(path, f)
	if prev, ok := e.said[u]; ok {
		strategy, detail = joinNew(prev[0], strategy), joinNew(prev[1], detail)
	}
	if e.said == nil {
		e.said = map[ir.FeatureUse][2]string{}
	}
	e.said[u] = [2]string{strategy, detail}
	e.led.Note(use(path, f), caps.Support{Level: caps.External, Strategy: strategy}, detail)
}

// foreignExt reports Ext data of other providers: it is not in the Woodpecker file, only in the round-trip metadata.
func (e *emitter) foreignExt(path string, x ir.Ext) {
	emitkit.ForeignExt(e.led, dialect, "Woodpecker", path, x)
}

// mark writes the marker line of an emulated construct into the file head.
func (e *emitter) mark(f caps.Feature, path string) { e.head = append(e.head, emitkit.Marker(f, path)) }

// markArg writes a marker and one JSON argument line. Woodpecker substitutes ${NAME} in the raw file, comments
// included, so the argument escapes every "$" the way commentSafe does; the parser reverses it.
func (e *emitter) markArg(f caps.Feature, path string, v any) error {
	line, err := emitkit.ArgLine(v)
	if err != nil {
		return err
	}
	e.mark(f, path)
	e.head = append(e.head, container.Escape(line, dollarEsc))
	return nil
}

// comment queues a lost-text line for the file head; "${{" never reaches the output.
func (e *emitter) comment(s string) { e.head = append(e.head, lostPrefix+commentSafe(s)) }

// esc doubles each $ so the text reaches Woodpecker's pre-processor, and then the shell, unchanged.
func esc(s string) string { return container.Escape(s, dollarEsc) }

// bindings are the references a step's text reads as run-time variables. The step must map each secret from a
// secret; refs lists every converted reference as "<namespace>.<name>" so the parser can restore the text.
type bindings struct{ secrets, vars, inputs, refs []string }

func (b *bindings) add(ns, name string) {
	if b == nil {
		return
	}
	if ref := ns + "." + name; !slices.Contains(b.refs, ref) {
		b.refs = append(b.refs, ref)
	}
	switch ns {
	case expr.PrefixSecrets:
		b.secrets = appendNew(b.secrets, name)
	case expr.PrefixVars:
		b.vars = appendNew(b.vars, name)
	case expr.PrefixInputs:
		b.inputs = appendNew(b.inputs, name)
	}
}

func appendNew(l []string, name string) []string {
	if slices.Contains(l, name) {
		return l
	}
	return append(l, name)
}

// refText spells one canonical reference in Woodpecker text. ok is false when Woodpecker has no spelling.
func (e *emitter) refText(b *bindings, mode textMode, path []string) (string, bool) {
	if len(path) == 2 && isIdent(path[1]) {
		switch path[0] {
		case expr.PrefixMatrix, dialect:
			if path[0] == expr.PrefixMatrix && !slices.Contains(e.matrixRefs, path[1]) {
				e.matrixRefs = append(e.matrixRefs, path[1])
			}
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
			b.add(path[0], path[1])
			return "$${" + path[1] + "}", true
		}
	}
	if mode == commandText && vars.ScriptRefLost(path, e.origin, dialect) {
		return "", false
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
	if len(lost) > 0 && (f == caps.StepImage || f == caps.StepWorkdir || f == caps.JobServices) {
		e.cut = true
	}
	return out, nil
}

// command converts one script entry. lost names the expressions Woodpecker cannot spell; the caller comments
// the entry out whole, because dropping the part could change what the command does.
func (e *emitter) command(b *bindings, s string) (out string, lost []string, err error) {
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil {
		return "", nil, err
	}
	out, lost = e.convertParts(b, commandText, parts)
	return out, lost, nil
}

// commentSpans swaps every statement of texts that holds a lost entry for a failing piece and returns the pieces in order.
func (e *emitter) commentSpans(path string, texts, why []string) []emitkit.Piece {
	return emitkit.ReplaceLost(texts, why, func(detail, text string) {
		e.lose(path, caps.ScriptInterpolation, detail)
		e.comment("script.interpolation " + path + ": " + text)
	})
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

func noSpelling(lost []string) string {
	if len(lost) == 0 {
		return ""
	}
	return "no Woodpecker spelling for expression " + strings.Join(lost, ", ")
}
