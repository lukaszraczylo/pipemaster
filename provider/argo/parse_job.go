package argo

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprargo "github.com/lukaszraczylo/pipemaster/expr/argo"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// artifactFrom reads the task output that an input artifact comes from.
var artifactFrom = regexp.MustCompile(`^\{\{\s*tasks\.([A-Za-z0-9_-]+)\.outputs\.artifacts\.[A-Za-z0-9_-]+\s*\}\}$`)

// jobBuilder reads one task and the template that it runs.
type jobBuilder struct {
	whenNeeds     map[string]bool
	argLines      map[string]int
	job           *ir.Job
	args          map[string]any
	ids           map[string]string
	px            *texts
	tx            *texts
	explicit      map[string]bool
	cl            *call
	c             *plCtx
	k             *sink
	artifactNeeds map[string]bool
	gitArts       map[*ir.Step]string
	holds         map[string]string
	outArts       map[string]string
	path          string
	fileOutputs   []string
	finally       bool
	cacheMounts   bool
}

func (c *plCtx) newJob(cl *call, ids map[string]string, finally bool) *jobBuilder {
	b := &jobBuilder{
		c: c, cl: cl, job: &ir.Job{ID: cl.id}, ids: ids, finally: finally, explicit: map[string]bool{}, whenNeeds: map[string]bool{},
		args: map[string]any{}, argLines: map[string]int{}, artifactNeeds: map[string]bool{},
		path: "pipeline/" + c.plID + "/job/" + cl.id,
	}
	b.px, b.tx = newTexts(b.taskID, false), newTexts(b.taskID, true)
	b.px.inNames, b.tx.inNames = c.inNames, c.inNames
	b.k = newSink(c.p.rep, c.file, b.path)
	return b
}

func (b *jobBuilder) taskID(name string) (string, bool) {
	id, ok := b.ids[name]
	return id, ok
}

// need adds a need on the task with the Argo name; the task must exist.
func (b *jobBuilder) need(name string) error {
	id, ok := b.taskID(name)
	if !ok {
		return fmt.Errorf("names the unknown task %q", name)
	}
	b.explicit[name] = true
	b.job.Needs = appendNeed(b.job.Needs, ir.Need{JobID: id})
	return nil
}

func (b *jobBuilder) build() error {
	r := newReader(b.cl.m)
	r.Take("name")
	if err := b.header(r); err != nil {
		return err
	}
	if v, ok := r.Take("arguments"); ok {
		if err := b.arguments(v); err != nil {
			return err
		}
	}
	t, err := b.resolve(r)
	if err != nil {
		return err
	}
	if t != nil {
		if err := b.body(t); err != nil {
			return err
		}
	}
	b.k.KeepRest(r)
	b.finish()
	return nil
}

// header reads the task keys that map to job fields.
func (b *jobBuilder) header(r *reader) error {
	for _, name := range b.cl.prev {
		if err := b.need(name); err != nil {
			return err
		}
	}
	depV, hasDeps := r.Take("dependencies")
	depsV, hasDepends := r.Take("depends")
	if hasDeps && hasDepends {
		return fmt.Errorf("dependencies and depends cannot be combined")
	}
	if hasDeps {
		names, ok := textList(depV)
		if !ok {
			return fmt.Errorf("dependencies must be a list of task names")
		}
		for _, n := range names {
			if err := b.need(n); err != nil {
				return fmt.Errorf("dependencies %w", err)
			}
		}
	}
	if hasDepends {
		if err := b.depends(depsV, r.Line("depends")); err != nil {
			return err
		}
	}
	restoredIf := false
	if v, ok := r.Take("when"); ok {
		w, isText := text(v)
		if !isText {
			return fmt.Errorf("when must be a string")
		}
		if w != disabledWhen() || !b.disabledMarker() {
			b.job.If = andExpr(b.job.If, b.when(w, r.Line("when")))
		} else {
			restoredIf = true
		}
	}
	if !restoredIf {
		var held disabledArg
		if markerArg(b.c.src.doc.Head, b.path, caps.JobIf, &held) {
			b.c.ignored(b.path, caps.JobIf, "the task is no longer disabled by when false after pipemaster wrote the file; the file content is kept")
		}
	}
	if v, ok := r.Take("continueOn"); ok {
		b.continueOn(v, r.Line("continueOn"))
	}
	for _, key := range []string{"withItems", "withParam", "withSequence"} {
		if v, ok := r.Take(key); ok {
			if key == "withItems" && b.matrixMarker(v) {
				continue
			}
			b.k.Keep(key, v, caps.JobMatrix, "task "+b.cl.name+": "+key+" has no IR form; kept in Ext", r.Line(key))
		}
	}
	return nil
}

func andExpr(l, r expr.Expr) expr.Expr {
	switch {
	case l == nil:
		return r
	case r == nil:
		return l
	}
	return expr.Binary{Op: expr.OpAnd, L: l, R: r}
}

// continueOn maps failed to AllowFailure; the other keys stay in Ext.
func (b *jobBuilder) continueOn(v any, line int) {
	m, ok := asMap(v)
	if !ok {
		b.k.Keep("continueOn", v, caps.JobAllowFailure, "continueOn is not a mapping; kept in Ext", line)
		return
	}
	if f, isBool := boolOf(m.Vals["failed"]); isBool {
		b.job.AllowFailure = f
	}
	var rest []string
	for _, key := range m.Order {
		if _, isBool := boolOf(m.Vals[key]); key != "failed" || !isBool {
			rest = append(rest, key)
		}
	}
	if len(rest) > 0 {
		b.k.Keep("continueOn", m.Subset(rest), caps.JobAllowFailure, "continueOn keys beside failed have no IR field; kept in Ext", line)
	}
}

// depends maps a depends expression to needs and a condition on the task results.
func (b *jobBuilder) depends(v any, line int) error {
	s0, ok := text(v)
	if !ok {
		return fmt.Errorf("depends must be a string")
	}
	d, err := parseDepends(s0)
	if err != nil {
		return err
	}
	for _, n := range d.tasks {
		if err := b.need(n); err != nil {
			return fmt.Errorf("depends %w", err)
		}
	}
	cond, constant := d.cond, false
	if l, ok := cond.(expr.Lit); ok && l.Kind == expr.LitBool {
		constant = true
		if l.Bool {
			cond = nil
		}
	}
	switch {
	case d.plain, constant && cond == nil:
	case constant:
		b.job.If = andExpr(b.job.If, cond)
	default:
		b.job.If = andExpr(b.job.If, b.renameNeeds(cond))
	}
	if d.unreadable {
		b.k.Out[ExtDepends] = renameDepends(s0, b.taskID)
		b.k.Note(caps.JobIf, "depends "+s0+" holds an atom with no IR status (Errored, Omitted, Daemoned, AnySucceeded or AllFailed) or a negated task with no result; the condition reads that atom as never true, so the job runs in fewer cases than in Argo or never, and the text is kept in Ext", line)
	} else if d.bare {
		b.k.Out[ExtDepends] = renameDepends(s0, b.taskID)
		b.k.Note(caps.JobIf, "depends "+s0+" has a task with no result, which also passes when the task is Skipped or Daemoned; the condition holds Succeeded or Skipped, and the Daemoned case is kept in the text in Ext", line)
	}
	return nil
}

func (b *jobBuilder) renameNeeds(e expr.Expr) expr.Expr {
	return expr.RenameNeeds(e, func(n string) string {
		if id, ok := b.taskID(n); ok {
			return id
		}
		return n
	})
}

// when reads a when condition. An expression with no IR form stays opaque Argo text.
func (b *jobBuilder) when(w string, line int) expr.Expr {
	if st, ok := workflowStatus(w); ok && b.finally {
		return st
	}
	e := renameInputRefs(exprargo.FromWhen(w, exprargo.ScopeTask), b.c.inNames)
	if o, isOpaque := e.(expr.Opaque); isOpaque {
		b.c.p.rep.Add(report.Entry{
			IRPath: b.path, Feature: caps.ExprOpaque, Level: caps.Lost, Strategy: "kept as opaque Argo text", SourceLoc: b.c.k.Loc(line),
			Detail: "a when condition that has no IR form is kept as opaque Argo text: " + w,
		})
		return o
	}
	expr.NeedRefs(e, func(n string) {
		if _, ok := b.taskID(n); ok {
			b.whenNeeds[n] = true
		}
	})
	return b.renameNeeds(e)
}

var statusWhen = regexp.MustCompile(`^(?:\{\{\s*workflow\.status\s*\}\}\s*(==|!=)\s*(\w+)|\{\{=\s*workflow\.status\s*(==|!=)\s*['"](\w+)['"]\s*\}\})$`)

// workflowStatus reads a test of workflow.status in an exit handler as a status function.
func workflowStatus(w string) (expr.Expr, bool) {
	m := statusWhen.FindStringSubmatch(w)
	if m == nil {
		return nil, false
	}
	op, val := m[1], m[2]
	if op == "" {
		op, val = m[3], m[4]
	}
	var e expr.Expr
	switch val {
	case "Succeeded":
		e = expr.Call{Name: "success"}
	case "Failed":
		e = expr.Call{Name: "failure"}
	default:
		return nil, false
	}
	if op == "!=" {
		e = expr.Unary{Op: expr.OpNot, X: e}
	}
	return e, true
}

// arguments binds the argument parameters to the template inputs; an argument that no text reads stays in Ext.
func (b *jobBuilder) arguments(v any) error {
	m, ok := asMap(v)
	if !ok {
		return fmt.Errorf("arguments must be a mapping")
	}
	ar := newReader(m)
	params, _ := ar.Take("parameters")
	for _, e := range listOf(params) {
		pm, ok := asMap(e)
		name, _ := text(valueOf(pm, "name"))
		if !ok || name == "" {
			return fmt.Errorf("an argument parameter needs a name")
		}
		b.argLines[name] = pm.Lines["name"]
		val, isText := text(pm.Vals["value"])
		if b.tx.matrix[name] && isText && val == "{{item."+name+"}}" && len(pm.Order) == 2 {
			continue
		}
		if !isText || len(pm.Order) != 2 || unresolved(b.taskID, val) {
			b.args[name] = plain(e)
			b.k.Note(caps.ExtPassthrough, "argument "+name+" is not a plain string or holds a tag with no IR reference; it stays in Ext and the template keeps its input", pm.Lines["name"])
			continue
		}
		b.tx.bind[name] = b.px.parts(val)
		b.args[name] = plain(pm.Vals["value"])
	}
	if arts, ok := ar.Take("artifacts"); ok {
		for _, e := range listOf(arts) {
			am, _ := asMap(e)
			if from, ok := text(valueOf(am, "from")); ok {
				if mm := artifactFrom.FindStringSubmatch(from); mm != nil {
					if _, known := b.taskID(mm[1]); known {
						b.artifactNeeds[mm[1]] = true
					}
				}
			}
		}
		b.k.Keep("argument_artifacts", arts, caps.JobNeedsArtifacts, "argument artifacts have no IR field beside the needs flag; kept in Ext", ar.Line("artifacts"))
	}
	b.k.KeepRestAs(ar, "arguments_")
	return nil
}

// finish adds the needs that references imply, reports the kept tags and the unread arguments, and sets the Ext.
func (b *jobBuilder) finish() {
	unread := map[string]any{}
	for name, v := range b.args {
		if _, bound := b.tx.bind[name]; !bound || !b.tx.used[name] {
			unread[name] = v
			if _, bound := b.tx.bind[name]; bound {
				b.k.Note(caps.ExtPassthrough, "argument "+name+" is not read by the template text; kept in Ext", b.argLines[name])
			}
		}
	}
	if len(unread) > 0 {
		b.k.Out[ExtArguments] = unread
	}
	names := map[string]bool{}
	for _, t := range []*texts{b.px, b.tx} {
		for n := range t.needs {
			names[n] = true
		}
	}
	for n := range b.whenNeeds {
		names[n] = true
	}
	for n := range b.artifactNeeds {
		names[n] = true
	}
	var implicit []string
	for n := range names {
		id, ok := b.taskID(n)
		if !ok || id == b.job.ID {
			continue
		}
		if !b.explicit[n] {
			implicit = append(implicit, id)
		}
		b.job.Needs = appendNeed(b.job.Needs, ir.Need{JobID: id, Artifacts: b.artifactNeeds[n]})
	}
	sort.Strings(implicit)
	if len(implicit) > 0 {
		list := make([]any, len(implicit))
		for i, id := range implicit {
			list[i] = id
		}
		b.k.Out[ExtImplicitNeeds] = list
	}
	b.reportKept()
	b.job.Ext = b.k.Ext()
}

func (b *jobBuilder) reportKept() {
	seen := map[string]bool{}
	for _, t := range []*texts{b.px, b.tx} {
		for _, tag := range t.kept {
			if seen[tag] {
				continue
			}
			seen[tag] = true
			b.c.p.rep.Add(report.Entry{
				IRPath: b.path, Feature: caps.ScriptInterpolation, Level: caps.Lost, Strategy: "kept as written", SourceLoc: b.c.file,
				Detail: tag + " is an Argo tag with no IR reference; it stays in the text as written",
			})
		}
	}
}

// resolve finds the template that the task runs. A reference to a template that is not in the repo becomes a
// task step with the reference in Ext.
func (b *jobBuilder) resolve(r *reader) (*omap, error) {
	if v, ok := r.Take("inline"); ok {
		m, isMap := asMap(v)
		if !isMap {
			return nil, fmt.Errorf("inline must be a template")
		}
		return m, nil
	}
	if v, ok := r.Take("templateRef"); ok {
		return b.templateRef(v, r.Line("templateRef")), nil
	}
	name, ok := textOf(r.Take("template"))
	if !ok {
		return nil, fmt.Errorf("a task needs template, templateRef or inline")
	}
	t := b.c.src.tmpls[name]
	if t == nil {
		return nil, fmt.Errorf("template %q is not defined", name)
	}
	b.c.src.used[name] = true
	return t, nil
}

func (b *jobBuilder) templateRef(v any, line int) *omap {
	m, _ := asMap(v)
	wt, _ := text(valueOf(m, "name"))
	tn, _ := text(valueOf(m, "template"))
	if src := b.c.p.wts[wt]; src != nil && src.tmpls[tn] != nil {
		src.used[tn] = true
		return src.tmpls[tn]
	}
	b.k.Keep("templateRef", v, caps.StepAction, "templateRef "+wt+"/"+tn+" names a template that is not in the repo; the reference is kept in Ext", line)
	b.job.Steps = append(b.job.Steps, &ir.Step{ID: mainStep, Kind: ir.StepTask})
	return nil
}

// renameInputRefs puts the IR input name back in every input reference of e.
func renameInputRefs(e expr.Expr, names map[string]string) expr.Expr {
	if len(names) == 0 {
		return e
	}
	switch v := e.(type) {
	case expr.Ref:
		if len(v.Path) == 2 && v.Path[0] == expr.PrefixInputs {
			if n, ok := names[v.Path[1]]; ok {
				return expr.Ref{Path: []string{expr.PrefixInputs, n}}
			}
		}
	case expr.Unary:
		return expr.Unary{Op: v.Op, X: renameInputRefs(v.X, names)}
	case expr.Binary:
		return expr.Binary{Op: v.Op, L: renameInputRefs(v.L, names), R: renameInputRefs(v.R, names)}
	case expr.Call:
		args := make([]expr.Expr, len(v.Args))
		for i, a := range v.Args {
			args[i] = renameInputRefs(a, names)
		}
		return expr.Call{Name: v.Name, Args: args}
	}
	return e
}
