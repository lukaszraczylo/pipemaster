package drone

import (
	"maps"
	"reflect"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	dexpr "github.com/lukaszraczylo/pipemaster/expr/drone"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

// cellDoc is a document that stands for one cell of a matrix job.
type cellDoc struct {
	d   *doc
	use ir.FeatureUse
	arg cellArg
}

// mergeCells folds the documents of each matrix job back into one job. It returns the documents that remain and
// the job ID that each cell document name stands for.
func mergeCells(rep *report.Report, docs []*doc) (kept []*doc, renamed map[string]string) {
	renamed = map[string]string{}
	groups := map[string][]cellDoc{}
	var order []string
	for _, d := range docs {
		u, ok := d.marks.markedWith(caps.JobMatrix, "")
		var a cellArg
		if !ok || !decodeArg(d.marks.args[u], &a) || a.Job == "" {
			continue
		}
		k := d.marks.pipe + "\x00" + a.Job
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], cellDoc{d, u, a})
	}
	drop := map[*doc]bool{}
	for _, k := range order {
		g := groups[k]
		slices.SortStableFunc(g, func(a, b cellDoc) int { return a.arg.Index - b.arg.Index })
		if !foldCells(rep, g) {
			shareMulti(g)
			continue
		}
		for _, c := range g {
			renamed[c.d.id] = g[0].arg.Job
			if c.d != g[0].d {
				drop[c.d] = true
			}
		}
		renameJob(rep, g[0].d, g[0].arg.Job)
	}
	for _, d := range docs {
		if id, ok := renamedJob(d); ok && !drop[d] && renamed[d.id] == "" {
			renamed[d.id] = id
			renameJob(rep, d, id)
		}
	}
	for _, d := range docs {
		if !drop[d] {
			kept = append(kept, d)
		}
	}
	return kept, renamed
}

// shareMulti gives each cell of an unfolded matrix the pipeline marker of the first cell, which is the only one
// that holds it, so the cells stay in their pipeline.
func shareMulti(g []cellDoc) {
	first, ok := g[0].d.marks.markedWith(caps.ProjectMultiPipeline, "")
	if !ok {
		return
	}
	for _, c := range g[1:] {
		if _, has := c.d.marks.markedWith(caps.ProjectMultiPipeline, ""); has {
			continue
		}
		c.d.marks.uses = append(c.d.marks.uses, first)
		c.d.marks.args[first] = g[0].d.marks.args[first]
	}
}

// renamedJob reads the job id that the marker of a renamed document holds. It applies when the document still
// carries the name the marker says.
func renamedJob(d *doc) (string, bool) {
	for _, u := range d.marks.uses {
		var a renameArg
		if u.Feature == caps.ExtPassthrough && decodeArg(d.marks.args[u], &a) && a.ID != "" && a.Name == d.id {
			return a.ID, true
		}
	}
	return "", false
}

// renameJob gives a document's job its ID and points the report at it.
func renameJob(rep *report.Report, d *doc, id string) {
	from, to := d.jobPath(), pipelinePth+"/job/"+id
	for i, e := range rep.Entries {
		if e.IRPath == from || strings.HasPrefix(e.IRPath, from+"/") {
			rep.Entries[i].IRPath = to + strings.TrimPrefix(e.IRPath, from)
		}
	}
	d.job.ID = id
}

// foldCells turns the first cell document into the matrix job when the cells are as the marker lists them. A
// document set that differs, or a cell whose text differs from the held text with the cell values put in, was
// edited, and the marker is ignored.
func foldCells(rep *report.Report, g []cellDoc) bool {
	first := g[0]
	mx := first.arg.Matrix
	if mx == nil {
		return false
	}
	combos := ir.Expand(mx)
	asEmitted := first.arg.Index == 0
	if len(combos) == 0 {
		asEmitted = asEmitted && len(g) == 1
	} else {
		asEmitted = asEmitted && len(g) == len(combos)
	}
	for i, c := range g {
		asEmitted = asEmitted && c.arg.Index == i && (len(combos) == 0 || sameCombo(combos[i], c.arg.Cell)) &&
			len(c.d.written) == len(first.d.written)
	}
	if asEmitted && len(combos) > 0 {
		asEmitted = heldMatches(first.arg.Held, g, combos) && jobVarsAre(first.arg.Env, g, combos) &&
			servicesAre(first.arg.Services, g, combos)
	}
	if !asEmitted {
		reportIgnored(rep, first.use, "the matrix cells")
		return false
	}
	first.d.job.Matrix = mx
	restoreJobVars(first.arg.Env, first.d.job)
	for i := range first.d.job.Services {
		if held, ok := first.arg.Services[first.d.job.Services[i].Name]; ok {
			first.d.job.Services[i].Image.Ref = held
		}
	}
	for _, h := range first.arg.Held {
		st := first.d.written[h.Pos]
		if h.Script != nil {
			st.Script = slices.Clone(h.Script)
		}
		if h.WorkDir != "" {
			st.WorkDir = h.WorkDir
		}
		if h.Image != "" && st.Image != nil {
			st.Image.Ref = h.Image
		}
		restoreVars(h, st)
	}
	return true
}

// servicesAre is true when each held service image, with the cell values put in, is what the cell holds. A service
// that the file does not hold had no Drone spelling.
func servicesAre(held map[string]string, g []cellDoc, combos []ir.Combo) bool {
	for i, c := range g {
		cell := cellValues(combos[i])
		for _, sv := range c.d.job.Services {
			if h, ok := held[sv.Name]; ok && !textIs(h, sv.Image.Ref, cell) {
				return false
			}
		}
	}
	return true
}

// jobVarsAre is true when each held job variable, with the values of the cell put in, is what the cell holds.
func jobVarsAre(held string, g []cellDoc, combos []ir.Combo) bool {
	if held == "" {
		return true
	}
	var vars []ir.Var
	if yaml.Unmarshal([]byte(held), &vars) != nil {
		return false
	}
	for i, c := range g {
		cell := cellValues(combos[i])
		for _, v := range vars {
			at := slices.IndexFunc(c.d.job.Env, func(w ir.Var) bool { return w.Name == v.Name })
			if at >= 0 && !valueIs(v.Value, c.d.job.Env[at].Value, cell) {
				return false
			}
		}
	}
	return true
}

// restoreJobVars puts the held job variables back.
func restoreJobVars(held string, j *ir.Job) {
	var vars []ir.Var
	if held == "" || yaml.Unmarshal([]byte(held), &vars) != nil {
		return
	}
	for _, v := range vars {
		if at := slices.IndexFunc(j.Env, func(w ir.Var) bool { return w.Name == v.Name }); at >= 0 {
			j.Env[at] = v
		}
	}
}

func cellValues(c ir.Combo) map[string]string {
	cell := map[string]string{}
	for _, kv := range c {
		cell[kv.Key] = kv.Value
	}
	return cell
}

// heldVars decodes the variables and settings that a held text carries. ok is false when they do not decode.
func heldVars(h heldText) (env []ir.Var, with map[string]ir.Value, ok bool) {
	if h.Env != "" && yaml.Unmarshal([]byte(h.Env), &env) != nil {
		return nil, nil, false
	}
	if h.With != "" && yaml.Unmarshal([]byte(h.With), &with) != nil {
		return nil, nil, false
	}
	return env, with, true
}

// varsAre is true when each held variable and setting, with the cell values put in, is what the step holds. One
// that the file does not hold had no Drone spelling and is in the round-trip metadata.
func varsAre(h heldText, st *ir.Step, cell map[string]string) bool {
	env, with, ok := heldVars(h)
	if !ok {
		return false
	}
	for _, v := range env {
		i := slices.IndexFunc(st.Env, func(w ir.Var) bool { return w.Name == v.Name })
		if i >= 0 && !valueIs(v.Value, st.Env[i].Value, cell) {
			return false
		}
	}
	for k, v := range with {
		got, has := st.With[k]
		if has && !valueIs(v, got, cell) || !has && st.Kind == ir.StepPlugin {
			return false
		}
	}
	return true
}

// restoreVars puts the held variables and settings back into the step.
func restoreVars(h heldText, st *ir.Step) {
	env, with, _ := heldVars(h)
	for _, v := range env {
		if i := slices.IndexFunc(st.Env, func(w ir.Var) bool { return w.Name == v.Name }); i >= 0 {
			st.Env[i] = v
		}
	}
	if len(with) > 0 && st.With == nil {
		st.With = map[string]ir.Value{}
	}
	maps.Copy(st.With, with)
}

// valueIs is true when the held expression value, with the cell values put in, is the literal that got holds.
func valueIs(held, got ir.Value, cell map[string]string) bool {
	parts, ok := valueParts(held)
	if !ok || got.Kind != ir.ValueLit {
		return false
	}
	var sb strings.Builder
	for _, p := range parts {
		r, isRef := p.Expr.(expr.Ref)
		switch {
		case p.Expr == nil:
			sb.WriteString(p.Text)
		case isRef && len(r.Path) == 2 && r.Path[0] == expr.PrefixMatrix:
			sb.WriteString(cell[r.Path[1]])
		default:
			return false
		}
	}
	return sb.String() == got.Lit
}

// heldMatches is true when every held text, with the values of the cell put in, is what the cell document holds.
func heldMatches(held []heldText, g []cellDoc, combos []ir.Combo) bool {
	for _, h := range held {
		for i, c := range g {
			if h.Pos < 0 || h.Pos >= len(c.d.written) {
				return false
			}
			st := c.d.written[h.Pos]
			cell := cellValues(combos[i])
			if !textIs(h.WorkDir, st.WorkDir, cell) || !imageIs(h.Image, st, cell) || !scriptIs(h.Script, st.Script, cell) ||
				!varsAre(h, st, cell) {
				return false
			}
		}
	}
	return true
}

func imageIs(held string, st *ir.Step, cell map[string]string) bool {
	if held == "" {
		return true
	}
	return st.Image != nil && textIs(held, st.Image.Ref, cell)
}

// scriptIs is true when got is the held script with the cell values put in. A held line that holds an expression
// has no Drone spelling for may be missing from got: the emitter comments it out.
func scriptIs(held, got []string, cell map[string]string) bool {
	if held == nil {
		return true
	}
	at := 0
	for _, h := range held {
		switch {
		case at < len(got) && textIs(h, got[at], cell):
			at++
		case !strings.Contains(h, "${{"):
			return false
		}
	}
	return at == len(got)
}

// textIs is true when the held template text, with the cell values put in for the matrix references, is got.
func textIs(held, got string, cell map[string]string) bool {
	if held == "" {
		return true
	}
	parts, err := expr.ParseScriptTemplate(held)
	if err != nil {
		return false
	}
	for i, p := range parts {
		r, ok := p.Expr.(expr.Ref)
		switch {
		case ok && len(r.Path) == 2 && r.Path[0] == expr.PrefixMatrix:
			parts[i] = expr.Part{Text: cell[r.Path[1]]}
		}
	}
	out, err := expr.PrintScriptTemplate(parts)
	return err == nil && out == got
}

// group is the documents that form one pipeline.
type group struct {
	id   string
	docs []*doc
}

// multiOf reads the marker that says which pipeline and job a document came from.
func multiOf(d *doc) (multiArg, bool) {
	u, ok := d.marks.markedWith(caps.ProjectMultiPipeline, "")
	var a multiArg
	return a, ok && decodeArg(d.marks.args[u], &a) && a.Pipeline != "" && a.Job != ""
}

// splitPipelines groups the documents by the pipeline their marker names. A document without one belongs to the
// default pipeline.
func splitPipelines(docs []*doc) []*group {
	type placed struct {
		d *doc
		a multiArg
	}
	byID := map[string][]placed{}
	var order []string
	pos := map[string]int{}
	for _, d := range docs {
		a, ok := multiOf(d)
		id := pipelineID
		if ok {
			id = a.Pipeline
		}
		if _, seen := byID[id]; !seen {
			order = append(order, id)
			pos[id] = a.P
		}
		byID[id] = append(byID[id], placed{d, a})
	}
	slices.SortStableFunc(order, func(a, b string) int { return pos[a] - pos[b] })
	var out []*group
	for _, id := range order {
		ps := byID[id]
		slices.SortStableFunc(ps, func(a, b placed) int { return a.a.J - b.a.J })
		g := &group{id: id}
		for _, p := range ps {
			g.docs = append(g.docs, p.d)
		}
		out = append(out, g)
	}
	return out
}

// buildPipelines assembles the pipelines from the documents of a file.
func buildPipelines(rep *report.Report, docs []*doc) ([]*ir.Pipeline, error) {
	docs, renamed := mergeCells(rep, docs)
	var out []*ir.Pipeline
	for _, g := range splitPipelines(docs) {
		out = append(out, g.build(rep, renamed))
	}
	if len(out) == 0 {
		out = append(out, &ir.Pipeline{ID: pipelineID})
	}
	return out, nil
}

// build assembles one pipeline from its documents.
func (g *group) build(rep *report.Report, renamed map[string]string) *ir.Pipeline {
	pl := &ir.Pipeline{ID: g.id}
	names := maps.Clone(renamed)
	for _, d := range g.docs {
		if _, ok := names[d.id]; !ok {
			names[d.id] = d.job.ID
		}
		if a, ok := multiOf(d); ok {
			names[d.id] = a.Job
			if _, cell := d.marks.markedWith(caps.JobMatrix, ""); cell && renamed[d.id] == "" {
				// A cell that was not folded stays a job of its own, named without the pipeline prefix.
				names[d.id] = strings.TrimPrefix(d.id, g.id+"-")
			}
		}
	}
	var finally []*ir.Job
	for _, d := range g.docs {
		pl.Jobs = append(pl.Jobs, d.job)
		if pl.Env == nil {
			pl.Env = d.pipeEnv
		}
		mapNeeds(d.job, names)
	}
	pl.Triggers = g.placeTriggers(rep)
	pl.Jobs = slices.DeleteFunc(pl.Jobs, func(j *ir.Job) bool {
		return j.ID == emptyJobID && len(j.Steps) == 0 && slices.ContainsFunc(g.docs, func(d *doc) bool { return d.job == j && d.noop })
	})
	for _, d := range g.docs {
		if u, ok := d.marks.markedWith(caps.PipelineFinally, ""); ok {
			var a finallyArg
			if !decodeArg(d.marks.args[u], &a) {
				reportUncollapsed(rep, u, "its argument is not a finally record")
				continue
			}
			d.job.Needs = a.Needs
			if a.NoIf && isAlways(d.job.If) {
				d.job.If = nil
			}
			finally = append(finally, d.job)
		}
	}
	if len(finally) > 0 {
		pl.Jobs = slices.DeleteFunc(pl.Jobs, func(j *ir.Job) bool { return slices.Contains(finally, j) })
		pl.Finally = finally
	}
	g.rename(rep, pl, names)
	return pl
}

func isAlways(x expr.Expr) bool {
	c, ok := x.(expr.Call)
	return ok && c.Name == "always" && len(c.Args) == 0
}

// mapNeeds points each dependency at the job of the document it names, once.
func mapNeeds(j *ir.Job, names map[string]string) {
	var out []ir.Need
	for _, n := range j.Needs {
		if id, ok := names[n.JobID]; ok {
			n.JobID = id
		}
		if !slices.ContainsFunc(out, func(o ir.Need) bool { return o.JobID == n.JobID }) {
			out = append(out, n)
		}
	}
	j.Needs = out
}

// rename gives the jobs of a folded pipeline their original IDs and points the report at them.
func (g *group) rename(rep *report.Report, pl *ir.Pipeline, names map[string]string) {
	if g.id == pipelineID {
		return
	}
	for _, d := range g.docs {
		id, ok := names[d.id]
		if !ok {
			continue
		}
		from, to := d.jobPath(), "pipeline/"+g.id+"/job/"+id
		for i, e := range rep.Entries {
			if e.IRPath == from || strings.HasPrefix(e.IRPath, from+"/") {
				rep.Entries[i].IRPath = to + strings.TrimPrefix(e.IRPath, from)
			}
		}
		d.job.ID = id
	}
	for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
		for i, n := range j.Needs {
			if id, ok := names[n.JobID]; ok {
				j.Needs[i].JobID = id
			}
		}
	}
}

// gate says how a document is entered: a manual or approval document stands outside the shared trigger list.
func (d *doc) gate() caps.Feature {
	for _, f := range []caps.Feature{caps.JobManual, caps.JobEnvironmentApproval} {
		if _, ok := d.marks.markedWith(f, ""); ok {
			return f
		}
	}
	return ""
}

// pure is the pipeline trigger form of the condition of a document. A finally document adds the status gate,
// which the marker explains.
func (d *doc) pure() []ir.Trigger {
	if d.merged != nil {
		return d.merged.triggers
	}
	if d.trig == nil {
		return nil
	}
	if _, ok := d.marks.markedWith(caps.PipelineFinally, ""); ok && d.trig.pure == nil {
		return d.trig.pureFinally
	}
	return d.trig.pure
}

// merged is the job condition and the pipeline triggers that the marker of a merged condition holds.
type merged struct {
	cond     expr.Expr
	triggers []ir.Trigger
}

// readMerged reads the marker of a job condition that the file holds merged with the pipeline triggers. It
// applies when the file still reads as the marker says.
func (d *doc) readMerged(rep *report.Report) {
	u, ok := d.marks.markedWith(caps.JobIf, "")
	if !ok || len(d.marks.args[u]) == 0 {
		return
	}
	var a condArg
	var ts []ir.Trigger
	if !decodeArg(d.marks.args[u], &a) {
		reportUncollapsed(rep, u, "its argument is not a condition")
		return
	}
	orig, err := expr.ParseCanonical(a.If)
	if err != nil || (a.Triggers != "" && yaml.Unmarshal([]byte(a.Triggers), &ts) != nil) {
		reportUncollapsed(rep, u, "its argument is not a condition")
		return
	}
	if d.trig == nil || d.trig.cond == nil || expr.String(d.trig.cond) != a.Back {
		reportIgnored(rep, u, "the job condition")
		return
	}
	d.merged = &merged{cond: orig, triggers: ts}
}

// placeTriggers reads the pipeline triggers and puts each condition that is not one of them on its job. A
// manual document stands outside the shared list, and a marker that holds the original triggers replaces the list
// that a file can only approximate.
func (g *group) placeTriggers(rep *report.Report) []ir.Trigger {
	var sel, gated []*doc
	for _, d := range g.docs {
		switch {
		case d.gate() != "":
			gated = append(gated, d)
		case d.trig != nil && d.trig.c.IsDisabled():
			// A disabled document never runs, so it says nothing about the pipeline triggers.
		default:
			sel = append(sel, d)
		}
	}
	for _, d := range g.docs {
		d.readMerged(rep)
	}
	list := sharedTriggers(sel)
	if list == nil {
		// A document that holds a job condition next to the triggers is not pure; the others still hold them.
		plain := slices.DeleteFunc(slices.Clone(sel), func(d *doc) bool { return d.merged != nil })
		if len(plain) > 0 && len(plain) < len(sel) {
			list = sharedTriggers(plain)
		}
	}
	restored := false
	if len(sel) > 0 && list == nil {
		list, restored = restoreTriggers(rep, sel)
	}
	if len(sel) == 0 {
		// A job that never runs holds the triggers only in its marker.
		if off := slices.DeleteFunc(slices.Clone(g.docs), func(d *doc) bool { return d.merged == nil || d.gate() != "" }); len(off) > 0 {
			list, restored = restoreTriggers(rep, off)
		}
	}
	if !restored {
		for _, d := range sel {
			d.restoreGlobs(rep, list)
		}
	}
	for _, d := range gated {
		f := d.gate()
		u, _ := d.marks.markedWith(f, "")
		var orig []ir.Trigger
		if !decodeYAMLArg(d.marks.args[u], &orig) {
			reportUncollapsed(rep, u, "its argument is not a list of triggers")
			continue
		}
		list = representable(orig)
		break
	}
	for _, d := range g.docs {
		d.placeCondition(list, restored)
	}
	return list
}

// restoreGlobs gives the triggers the glob patterns that the marker holds, when the file still spells them as the
// emitter wrote them.
func (d *doc) restoreGlobs(rep *report.Report, list []ir.Trigger) {
	for _, u := range d.marks.uses {
		if (u.Feature != caps.TriggerFilterTags && u.Feature != caps.TriggerFilterBranch) || !strings.Contains(u.IRPath, "/trigger/") || len(d.marks.args[u]) == 0 {
			continue
		}
		var a globArg
		if !decodeArg(d.marks.args[u], &a) {
			reportUncollapsed(rep, u, "its argument is not a glob filter")
			continue
		}
		for i := range list {
			t := &list[i]
			switch {
			case u.Feature == caps.TriggerFilterTags && t.Kind == ir.TriggerTag:
				restoreFilter(rep, u, &t.Tags, a.Tags)
			case u.Feature == caps.TriggerFilterBranch && (t.Kind == ir.TriggerPush || t.Kind == ir.TriggerPR) && hasFilter(t.Branches):
				restoreFilter(rep, u, &t.Branches, a.Branches)
			}
		}
	}
}

// restoreFilter replaces the parsed filter with the original when the file holds what the emitter writes for it.
func restoreFilter(rep *report.Report, u ir.FeatureUse, parsed *ir.Filter, orig ir.Filter) {
	want, _ := irGlobs(orig, "")
	if !slices.Equal(want.Include, parsed.Include) || !slices.Equal(want.Exclude, parsed.Exclude) {
		reportIgnored(rep, u, "the glob filter")
		return
	}
	*parsed = orig
}

// sharedTriggers returns the pipeline triggers when every document has the same pure trigger filter, else nil:
// one pipeline holds all the jobs, so a trigger stands only for a filter that every job shares.
func sharedTriggers(docs []*doc) []ir.Trigger {
	var first []ir.Trigger
	for i, d := range docs {
		p := d.pure()
		if p == nil {
			return nil
		}
		if i > 0 && !reflect.DeepEqual(p, first) {
			return nil
		}
		first = p
	}
	return first
}

// representable keeps the triggers a Drone file can hold.
func representable(ts []ir.Trigger) []ir.Trigger {
	var out []ir.Trigger
	for _, t := range ts {
		if _, ok := eventOf[t.Kind]; ok {
			out = append(out, t)
		}
	}
	return out
}

// restoreTriggers reads the triggers from the marker of the ref list: the file holds them only as refs, which
// change a glob's meaning. It applies when every document carries the same marker and its conditions are what
// the emitter writes for the triggers of the marker.
func restoreTriggers(rep *report.Report, docs []*doc) ([]ir.Trigger, bool) {
	var orig []ir.Trigger
	for i, d := range docs {
		var u ir.FeatureUse
		var ok bool
		for _, f := range []caps.Feature{caps.TriggerFilterBranch, caps.TriggerFilterTags} {
			if u, ok = d.marks.markedWith(f, pipelinePth); ok {
				break
			}
		}
		var ts []ir.Trigger
		if d.merged != nil {
			if i > 0 && !reflect.DeepEqual(d.merged.triggers, orig) {
				return nil, false
			}
			orig = d.merged.triggers
			continue
		}
		if !ok || d.trig == nil {
			return nil, false
		}
		if !decodeYAMLArg(d.marks.args[u], &ts) {
			reportUncollapsed(rep, u, "its argument is not a list of triggers")
			return nil, false
		}
		if i > 0 && !reflect.DeepEqual(ts, orig) {
			return nil, false
		}
		orig = ts
		if !refListAsEmitted(orig, d.trig.c) {
			reportIgnored(rep, u, "the trigger refs")
			return nil, false
		}
	}
	return representable(orig), true
}

// refListAsEmitted is true when the conditions of a document hold the events and refs that the emitter writes
// for the triggers.
func refListAsEmitted(orig []ir.Trigger, c dexpr.Conditions) bool {
	e := &emitter{led: emitkit.NewLedger("ir", dialect, adapter{}.Caps())}
	pl := &ir.Pipeline{ID: pipelineID, Triggers: orig}
	cs, ok := e.triggerSet(e.classify(pl))
	return ok && slices.Equal(cs.c.Event.Include, c.Event.Include) &&
		slices.Equal(cs.c.Ref.Include, c.Ref.Include) && slices.Equal(cs.c.Ref.Exclude, c.Ref.Exclude)
}

// triggerOnly is true when the conditions of a document hold nothing but the events and refs of a trigger.
func (t *trigger) triggerOnly() bool {
	return empty(t.c.Branch) && empty(t.c.Repo) && len(t.c.Status) == 0 && len(t.extra) == 0
}

// placeCondition puts the trigger block of the document on its job unless it is one of the pipeline triggers,
// the document gate, or the refs that a marker restored.
func (d *doc) placeCondition(list []ir.Trigger, restored bool) {
	t := d.trig
	if d.merged != nil {
		d.job.If = d.merged.cond
		return
	}
	if t == nil {
		return
	}
	gate := d.gate()
	if ev := map[caps.Feature]string{caps.JobManual: eventCustom, caps.JobEnvironmentApproval: eventPromote}[gate]; gate != "" && !t.hasEvent(ev) {
		u, _ := d.marks.markedWith(gate, "")
		reportIgnored(d.rep, u, "the job gate")
		d.job.If = t.cond
		return
	}
	switch gate {
	case caps.JobManual:
		d.job.Manual = true
		if t.only(eventCustom, "") {
			return
		}
	case caps.JobEnvironmentApproval:
		d.job.Environment = &ir.Environment{Approval: true}
		if tg, _ := t.raw[keyTarget].([]any); len(tg) == 1 {
			d.job.Environment.Name, _ = tg[0].(string)
		}
		if t.only(eventPromote, keyTarget) {
			return
		}
	default:
		pure := d.pure()
		if len(list) > 0 && (pure != nil || restored && t.triggerOnly()) {
			if pure != nil {
				if _, cron := t.raw[keyCron]; cron {
					d.ext.note(caps.TriggerSchedule, "trigger.cron names cron jobs of the repo settings; the IR keeps no schedule expression", 0)
				}
				if p := t.paths; p != nil {
					d.ext.note(caps.TriggerFilterPaths, "trigger.paths is kept as the trigger paths filter; the Drone OSS server does not evaluate it", p.line)
				}
			}
			return
		}
	}
	d.job.If = t.cond
	if t.report != nil {
		t.report()
	}
	if len(t.extra) > 0 {
		d.ext.vals[keyTrigger] = t.extra
		for _, k := range t.extraKeys {
			d.ext.note(caps.ExtPassthrough, "trigger."+k+" has no IR field; kept in Ext", t.lines[k])
		}
		d.job.Ext = d.ext.ext()
	}
}

// hasEvent is true when the condition lists event.
func (t *trigger) hasEvent(event string) bool {
	ev, _ := t.raw[dexpr.KeyEvent].([]any)
	return slices.Contains(ev, any(event))
}

// only is true when the condition holds the one event and, if key is not empty, that key, and nothing else.
func (t *trigger) only(event, key string) bool {
	ev, _ := t.raw[dexpr.KeyEvent].([]any)
	if len(ev) != 1 || ev[0] != event {
		return false
	}
	for k := range t.raw {
		if k != dexpr.KeyEvent && k != key {
			return false
		}
	}
	return true
}

func sameCombo(a, b ir.Combo) bool {
	if len(a) != len(b) {
		return false
	}
	for _, kv := range a {
		if !slices.Contains(b, kv) {
			return false
		}
	}
	return true
}
