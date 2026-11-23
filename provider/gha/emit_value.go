package gha

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgha "github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	refTypeBranch = "branch"
	refTypeTag    = "tag"
)

// emitter carries the state of one Emit call: the ledger, and the lost-text comments waiting for a node.
type emitter struct {
	led      *emitkit.Ledger
	from     *expr.Dialect
	comments []string
	marks    []string
	// stepIDs maps the step IDs of the job being written to the IDs written.
	stepIDs map[string]string
	// pluginVars holds the PLUGIN_ variables of the plugin step being written, for stepTail to add to its env.
	pluginVars *yaml.Node
	d          Dialect
}

// sourceDialect is the expression dialect of a project's origin; nil means GHA semantics already hold.
func sourceDialect(origin string) *expr.Dialect { return expr.SourceFor(origin, exprgha.Lang) }

// source rewrites x from the origin dialect's semantics into GHA's. An approximate result is
// reported as emulated with the translator's caveats; ok is false when no translation exists.
func (e *emitter) source(path string, f caps.Feature, x expr.Expr) (expr.Expr, bool) {
	if e.from == nil || isStatus(x) {
		return x, true
	}
	if b, ok := x.(expr.Binary); ok && b.Op == expr.OpAnd && (isStatus(b.L) || isStatus(b.R)) {
		l, lok := e.source(path, f, b.L)
		r, rok := e.source(path, f, b.R)
		return expr.Binary{Op: expr.OpAnd, L: l, R: r}, lok && rok
	}
	if t, ok := expr.TranslateFacts(x, *e.from, exprgha.Lang, expr.Facts{}); ok {
		// An exact translation can still change the shape, such as a tag test that GHA spells with ref_type.
		if (f == caps.JobIf || f == caps.StepIf) && expr.String(t) != expr.String(x) {
			if err := e.markArg(f, path, srcArg{Src: expr.String(x), From: markFrom(e.from)}); err != nil {
				e.comment("expression source not carried: " + err.Error())
			}
		}
		return t, true
	}
	t, cav, ok := expr.TranslateApprox(x, *e.from, exprgha.Lang)
	if !ok {
		return nil, false
	}
	e.emulate(path, f, "approximate expression translation", strings.Join(cav, "; "))
	if f == caps.JobIf || f == caps.StepIf {
		if err := e.markArg(f, path, srcArg{Src: expr.String(x), From: markFrom(e.from)}); err != nil {
			e.comment("expression source not carried: " + err.Error())
		}
	}
	return t, true
}

// srcArg carries a condition as the source dialect wrote it, which the emitted one only approximates.
type srcArg struct {
	Src  string `json:"src"`
	From string `json:"from,omitempty"`
}

// markFrom names the source dialect in a marker. GitLab is the default and stays unnamed.
func markFrom(d *expr.Dialect) string {
	if d == nil || d.ID == provider.GitLab {
		return ""
	}
	return d.ID
}

// isStatus is true for a job-status function, which both dialects' emitters share and no dialect translates.
func isStatus(x expr.Expr) bool {
	c, ok := x.(expr.Call)
	if !ok || len(c.Args) != 0 {
		return false
	}
	switch c.Name {
	case "always", "success", "failure", "cancelled":
		return true
	}
	return false
}

// condition renders an if: for a slot that must fail safe: when GHA cannot state it the construct is disabled.
func (e *emitter) condition(path string, f caps.Feature, x expr.Expr, what string) *yaml.Node {
	t, ok := e.source(path, f, x)
	if ok {
		if text, err := printExpr(t); err == nil {
			return str(text)
		}
	}
	e.disable(path, f, x, what)
	return boolNodeOf(false)
}

func (e *emitter) disable(path string, f caps.Feature, x expr.Expr, what string) {
	e.lose(path, f, "condition not expressible in GitHub Actions; "+what+" disabled")
	e.comment(string(f) + " " + path + ": " + expr.String(x))
}

func (e *emitter) lose(path string, f caps.Feature, detail string) {
	e.led.Note(ir.FeatureUse{IRPath: path, Feature: f}, caps.Support{Level: caps.Lost}, detail)
}

func (e *emitter) emulate(path string, f caps.Feature, strategy, detail string) {
	e.led.Note(ir.FeatureUse{IRPath: path, Feature: f}, caps.Support{Level: caps.Emulated, Strategy: strategy}, detail)
}

func (e *emitter) comment(s string) {
	e.comments = append(e.comments, lostPrefix+strings.ReplaceAll(s, "\n", `\n`))
}

// foreignExt reports Ext data of other providers: it is not in the GHA file, only in the round-trip metadata.
func (e *emitter) foreignExt(path string, x ir.Ext) {
	foreign := make(ir.Ext, len(x))
	for k, v := range x {
		if k != e.d.ID && k != provider.Family(e.d.ID) {
			foreign[k] = v
		}
	}
	emitkit.ForeignExt(e.led, "", "GHA", path, foreign)
}

// mark queues a marker line for the file head, where the parser finds it whatever the construct's position.
func (e *emitter) mark(f caps.Feature, path string) {
	e.marks = append(e.marks, emitkit.Marker(f, e.writtenPath(path)))
}

// writtenPath names a step by the ID the file holds, which is where the parser looks for it.
func (e *emitter) writtenPath(path string) string {
	head, id, ok := strings.Cut(path, "/step/")
	if !ok {
		return path
	}
	if w, ok := e.stepIDs[id]; ok {
		return head + "/step/" + w
	}
	return path
}

// markArg queues a marker and its JSON argument line.
func (e *emitter) markArg(f caps.Feature, path string, v any) error {
	line, err := emitkit.ArgLine(v)
	if err != nil {
		return err
	}
	e.marks = append(e.marks, emitkit.Marker(f, e.writtenPath(path)), line)
	return nil
}

// drain returns the pending comment lines and clears them.
func (e *emitter) drain() []string {
	c := e.comments
	e.comments = nil
	return c
}

// ghaRef rewrites the canonical branch and tag refs, which GHA has no names for, into ref_type tests.
func ghaRef(x expr.Expr) expr.Expr {
	switch v := x.(type) {
	case expr.Ref:
		kind := ""
		switch strings.Join(v.Path, ".") {
		case expr.CtxBranch:
			kind = refTypeBranch
		case expr.CtxTag:
			kind = refTypeTag
		default:
			return x
		}
		typed := expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxRefType}}, R: expr.Lit{Kind: expr.LitStr, Str: kind}}
		named := expr.Binary{Op: expr.OpAnd, L: typed, R: expr.Ref{Path: []string{expr.CtxRefName}}}
		return expr.Binary{Op: expr.OpOr, L: named, R: expr.Lit{Kind: expr.LitStr}}
	case expr.Unary:
		v.X = ghaRef(v.X)
		return v
	case expr.Binary:
		v.L, v.R = ghaRef(v.L), ghaRef(v.R)
		return v
	case expr.Call:
		args := make([]expr.Expr, len(v.Args))
		for i, a := range v.Args {
			args[i] = ghaRef(a)
		}
		v.Args = args
		return v
	}
	return x
}

func printExpr(x expr.Expr) (string, error) { return exprgha.Print(ghaRef(x)) }

// template converts a canonical script template into GHA text. An expression GHA cannot spell is
// dropped, reported as lost interpolation at path, and kept in a comment.
func (e *emitter) template(path, s string) (string, error) {
	if !strings.Contains(s, "${{") {
		return s, nil
	}
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Expr == nil {
			sb.WriteString(p.Text)
			continue
		}
		text, err := printExpr(p.Expr)
		if err != nil {
			e.lose(path, caps.ScriptInterpolation, err.Error())
			e.comment("script.interpolation " + path + ": ${{ " + expr.String(p.Expr) + " }}")
			continue
		}
		sb.WriteString("${{ " + text + " }}")
	}
	return sb.String(), nil
}

func (e *emitter) templates(path string, ss []string) ([]string, error) {
	out := make([]string, len(ss))
	for i, s := range ss {
		t, err := e.template(path, s)
		if err != nil {
			return nil, err
		}
		out[i] = t
	}
	return out, nil
}

// expression prints x for a bare slot such as if:; ok is false when GHA cannot spell it.
func (e *emitter) expression(path string, x expr.Expr) (text string, ok bool) {
	text, err := printExpr(x)
	if err != nil {
		e.lose(path, caps.ExprOpaque, err.Error())
		e.comment("expr.opaque " + path + ": " + expr.String(x))
		return "", false
	}
	return text, true
}

// value renders an env or with value; nil means it was lost.
func (e *emitter) value(path string, v ir.Value) (*yaml.Node, error) {
	switch v.Kind {
	case ir.ValueSecret:
		text, err := printExpr(expr.Ref{Path: []string{expr.PrefixSecrets, v.Secret}})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return str("${{ " + text + " }}"), nil
	case ir.ValueExpr:
		return e.exprValue(path, v.Expr), nil
	}
	if !strings.Contains(v.Lit, "${{") {
		return str(v.Lit), nil
	}
	s, err := expr.PrintScriptTemplate([]expr.Part{{Text: v.Lit}})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return str(s), nil
}

// refValue translates a value made of references from a foreign origin; ok is false when it is not one.
func (e *emitter) refValue(x expr.Expr) (expr.Expr, bool) {
	if e.from == nil {
		return nil, false
	}
	return expr.TranslateRefs(x, *e.from, exprgha.Lang)
}

func (e *emitter) exprValue(path string, x expr.Expr) *yaml.Node {
	t, ok := e.refValue(x)
	if !ok {
		t, ok = e.source(path, caps.ExprOpaque, x)
	}
	if !ok {
		e.lose(path, caps.ExprOpaque, "expression has no translation from "+e.from.ID)
		e.comment("expr.opaque " + path + ": " + expr.String(x))
		return nil
	}
	x = t
	if c, ok := x.(expr.Call); ok {
		if parts, ok := emitkit.UnmixFormat(c); ok {
			if s, ok := joinParts(parts); ok {
				return str(s)
			}
		}
	}
	text, ok := e.expression(path, x)
	if !ok {
		return nil
	}
	return str("${{ " + text + " }}")
}

// valueText is the text of a with or env value as a workflow file writes it.
func valueText(v ir.Value) string {
	if v.Kind != ir.ValueExpr {
		return v.Lit
	}
	if c, ok := v.Expr.(expr.Call); ok {
		if parts, ok := emitkit.UnmixFormat(c); ok {
			if s, ok := joinParts(parts); ok {
				return s
			}
		}
	}
	text, err := printExpr(v.Expr)
	if err != nil {
		return ""
	}
	return "${{ " + text + " }}"
}

func joinParts(parts []expr.Part) (string, bool) {
	var sb strings.Builder
	for _, p := range parts {
		if p.Expr == nil {
			sb.WriteString(p.Text)
			continue
		}
		text, err := printExpr(p.Expr)
		if err != nil {
			return "", false
		}
		sb.WriteString("${{ " + text + " }}")
	}
	return sb.String(), true
}

// vars renders an env mapping in order; lost values are omitted.
func (e *emitter) vars(path string, vs []ir.Var) (*yaml.Node, error) {
	m := newMap()
	for _, v := range vs {
		n, err := e.value(path, v.Value)
		if err != nil {
			return nil, err
		}
		if n != nil {
			m.set(v.Name, n)
		}
	}
	return m.n, nil
}
