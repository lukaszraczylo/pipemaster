package drone

import (
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	dexpr "github.com/lukaszraczylo/pipemaster/expr/drone"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/provider/internal/container"
)

const (
	lostPrefix = "# pipemaster:lost "
	fromSecret = container.FromSecret
)

// textMode says where converted text lands. Drone substitutes ${NAME} in the raw file before it parses it, so a
// value field holds only names that substitution knows. A command also reaches a shell, which reads $${NAME}
// as the run-time variable $NAME.
type textMode uint8

const (
	valueText textMode = iota
	commandText
)

// emitter carries the state of one Emit call: the ledger and the comment lines of the document being written.
type emitter struct {
	led  *emitkit.Ledger
	said map[ir.FeatureUse][2]string
	// stepIDs maps the path of each renamed step to its ID in the IR.
	stepIDs map[string]string
	// cell is the matrix combination of the document being written.
	cell map[string]string
	// declared lists the matrix names of the job being written.
	declared map[string]bool
	// sem is the equality semantics of the dialect that wrote the IR conditions.
	sem expr.Semantics
	// from is the source dialect of a condition that shares the Drone class, so its event names map to Drone's.
	from *expr.Dialect
	head []string
	// cut is set when an image or workdir lost an expression; the step or service being written owns it.
	cut bool
}

func use(path string, f caps.Feature) ir.FeatureUse { return ir.FeatureUse{IRPath: path, Feature: f} }

func (e *emitter) lose(path string, f caps.Feature, detail string) {
	e.note(path, f, caps.Lost, "", detail)
}

// emulate notes an emulation. A second note for the same path and feature adds its text to the first, so the
// report keeps every caveat.
func (e *emitter) emulate(path string, f caps.Feature, strategy, detail string) {
	e.note(path, f, caps.Emulated, strategy, detail)
}

func (e *emitter) external(path string, f caps.Feature, strategy, detail string) {
	e.note(path, f, caps.External, strategy, detail)
}

func (e *emitter) note(path string, f caps.Feature, lv caps.Level, strategy, detail string) {
	u := use(path, f)
	if prev, ok := e.said[u]; ok {
		strategy, detail = joinNew(prev[0], strategy), joinNew(prev[1], detail)
	}
	if e.said == nil {
		e.said = map[ir.FeatureUse][2]string{}
	}
	e.said[u] = [2]string{strategy, detail}
	if lv == caps.Lost {
		strategy = ""
	}
	e.led.Note(u, caps.Support{Level: lv, Strategy: strategy}, detail)
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

// foreignExt reports Ext data of other providers: it is not in the Drone file, only in the round-trip metadata.
func (e *emitter) foreignExt(path string, x ir.Ext) {
	emitkit.ForeignExt(e.led, dialect, "Drone", path, x)
}

// mark writes the marker line of an emulated construct into the document head.
func (e *emitter) mark(f caps.Feature, path string) { e.head = append(e.head, emitkit.Marker(f, path)) }

// markArg writes a marker and one JSON argument line. Drone substitutes ${NAME} in comments too, so the
// argument escapes every "$"; the parser reverses it.
func (e *emitter) markArg(f caps.Feature, path string, v any) error {
	line, err := emitkit.ArgLine(v)
	if err != nil {
		return err
	}
	e.mark(f, path)
	e.head = append(e.head, esc(line))
	return nil
}

// comment queues a lost-text line for the document head; "${{" never reaches the output.
func (e *emitter) comment(s string) { e.head = append(e.head, lostPrefix+commentSafe(s)) }

// esc doubles each $ so the text reaches Drone's pre-processor, and then the shell, unchanged.
func esc(s string) string { return container.Escape(s, dollarEsc) }

func escAll(l []string) []string {
	out := make([]string, len(l))
	for i, s := range l {
		out[i] = esc(s)
	}
	return out
}

// bindings are the references a step's text reads as run-time variables. The step must map each secret from a
// secret; refs lists every converted reference as "<namespace>.<name>" so the parser can restore the text.
type bindings struct {
	// hits lists the references that the command being written converted, with their place in its text.
	hits []refHit
	// at holds, for each reference, the ordinals of its variable among every ${NAME} of the script.
	at                          map[string]refAt
	secrets, vars, inputs, refs []string
}

// refHit is one converted reference and the offset of its variable in the written command.
type refHit struct {
	ref  string
	name string
	off  int
}

// refAt says which ${NAME} of a step script the conversion wrote for a reference, and how many there are of that
// name, so the parser can tell them from a ${NAME} that the author wrote.
type refAt struct {
	At []int `json:"at"`
	Of int   `json:"of"`
}

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

var escVarUseRE = regexp.MustCompile(`\$\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// place numbers the ${NAME} of a written command among those of the script and records the ones that the
// conversion wrote. seen counts the occurrences of each name so far.
func (b *bindings) place(out string, seen map[string]int) {
	for _, m := range escVarUseRE.FindAllStringSubmatchIndex(out, -1) {
		name := out[m[2]:m[3]]
		for _, h := range b.hits {
			if h.off == m[0] && h.name == name {
				if b.at == nil {
					b.at = map[string]refAt{}
				}
				a := b.at[h.ref]
				a.At = append(a.At, seen[name])
				b.at[h.ref] = a
			}
		}
		seen[name]++
	}
}

// total records how many ${NAME} the script holds for each converted reference.
func (b *bindings) total(seen map[string]int) {
	for ref, a := range b.at {
		_, name, _ := strings.Cut(ref, ".")
		a.Of = seen[name]
		b.at[ref] = a
	}
}

func appendNew(l []string, name string) []string {
	if slices.Contains(l, name) {
		return l
	}
	return append(l, name)
}

// refText spells one canonical reference in Drone text. ok is false when Drone has no spelling.
func (e *emitter) refText(b *bindings, mode textMode, path []string, off int) (string, bool) {
	if len(path) == 2 && path[0] == expr.PrefixMatrix {
		if v, ok := e.cell[path[1]]; ok {
			return esc(v), true
		}
		if e.declared[path[1]] {
			return "", true
		}
		return "", false
	}
	if len(path) == 2 && isIdent(path[1]) {
		switch path[0] {
		case expr.PrefixEnv, expr.PrefixSecrets, expr.PrefixVars, expr.PrefixInputs:
			if mode != commandText {
				return "", false
			}
			b.add(path[0], path[1])
			if b != nil {
				b.hits = append(b.hits, refHit{ref: path[0] + "." + path[1], name: path[1], off: off})
			}
			return dollarEsc + "{" + path[1] + "}", true
		}
	}
	if dp, ok := dexpr.Vocab.Dialect(path); ok && len(dp) == 1 {
		if mode == commandText {
			return dollarEsc + "{" + dp[0] + "}", true
		}
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

// convertParts joins the parts as Drone text. lost holds the canonical text of each expression that has no spelling.
func (e *emitter) convertParts(b *bindings, mode textMode, parts []expr.Part) (out string, lost []string) {
	var sb strings.Builder
	for _, p := range parts {
		switch x := p.Expr.(type) {
		case nil:
			sb.WriteString(esc(p.Text))
		case expr.Lit:
			sb.WriteString(esc(litText(x)))
		case expr.Ref:
			if t, ok := e.refText(b, mode, x.Path, sb.Len()); ok {
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

// text converts canonical template text. Expressions with no Drone spelling are reported and omitted.
func (e *emitter) text(b *bindings, path string, f caps.Feature, mode textMode, s string) (string, error) {
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil {
		return "", err
	}
	out, lost := e.convertParts(b, mode, parts)
	for _, l := range lost {
		e.lose(path, f, "no Drone spelling for expression "+l)
		e.comment(string(f) + " " + path + ": {{ " + l + " }}")
	}
	if len(lost) > 0 && (f == caps.StepImage || f == caps.StepWorkdir || f == caps.JobServices) {
		e.cut = true
	}
	return out, nil
}

// command converts one script entry. An entry with an expression Drone cannot spell is commented out whole,
// because dropping the part could change what the command does.
func (e *emitter) command(b *bindings, path, s string) (out string, keep bool, err error) {
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil {
		return "", false, err
	}
	out, lost := e.convertParts(b, commandText, parts)
	if len(lost) == 0 {
		return out, true, nil
	}
	e.lose(path, caps.ScriptInterpolation, "no Drone spelling for expression "+strings.Join(lost, ", ")+"; the line is commented out")
	e.comment("script.interpolation " + path + ": " + s)
	return "", false, nil
}

// valueNode renders an environment or settings value; nil means the value was lost and must be omitted.
func (e *emitter) valueNode(path string, v ir.Value) (*yaml.Node, error) {
	switch v.Kind {
	case ir.ValueSecret:
		m := newYMap()
		m.set(fromSecret, ystr(v.Secret))
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
			e.lose(path, caps.ExprOpaque, "no Drone spelling for expression "+l)
			e.comment("expr.opaque " + path + ": " + l)
		}
		if len(lost) > 0 {
			return nil, nil
		}
		return ystr(out), nil
	}
	return ystr(esc(v.Lit)), nil
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
func (e *emitter) envNode(path string, vs []ir.Var, m *ymap) error {
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
