package gitlab

import (
	"bytes"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/provider/inline"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/vars"
)

const (
	jobIDPrefix = "# pipemaster:jobid "
	rawExprOpen = "${{"
	extWorkflow = "workflow"
	extDefault  = "default"
	scheme      = "://"
)

// Emit writes the project as one .gitlab-ci.yml. Every feature use goes through the ledger; Finish
// fails the call when one was never accounted for. Local include files are not re-emitted: Parse
// merged them into the jobs, so the emitted file stands alone.
func (adapter) Emit(p *ir.Project) (map[string][]byte, *report.Report, error) {
	return inline.Emit(p, dialect, adapter{}.emitFlat)
}

func (a adapter) emitFlat(p *ir.Project) (map[string][]byte, *report.Report, error) {
	p, notes := singleIncludes(p)
	files, rep, err := a.emitMain(p)
	if err != nil {
		return files, rep, err
	}
	rep.Entries = append(rep.Entries, notes...)
	return files, rep, a.emitChildren(p, files, rep)
}

func (adapter) emitMain(p *ir.Project) (map[string][]byte, *report.Report, error) {
	p, vres := vars.Prepare(p, dialect)
	p, noForm, injected := provider.WithDefaultTriggers(p, dialect)
	origin := ""
	if p != nil {
		origin = p.Origin.Provider
	}
	e := &emitter{originID: origin, splits: map[*ir.Job]splitPart{}, plugins: map[string]pluginUse{}, pluginWhy: map[string]string{}, noSplit: map[string]string{}, from: sourceDialect(origin), led: emitkit.NewLedger("ir", dialect, adapter{}.Caps())}
	e.gated = emitkit.GatedSteps(p)
	e.injected = injected
	uses := ir.Features(p)
	for _, u := range uses {
		e.led.Decide(u)
	}
	for id, events := range noForm {
		for _, g := range provider.GroupByFeature(events) {
			e.lose("pipeline/"+id, g.Feature, "the source runs on the events "+strings.Join(g.Names, ", ")+" and its file has no trigger form for them; GitLab has no pipeline source for them")
		}
	}
	files := map[string][]byte{}
	if p != nil {
		var pls []*ir.Pipeline
		for _, pl := range p.Pipelines {
			if pl != nil {
				pls = append(pls, pl)
			}
		}
		if len(pls) > 0 {
			data, err := e.pipelineFile(pls)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", dialect, err)
			}
			if bytes.Contains(data, []byte(rawExprOpen)) {
				return nil, nil, fmt.Errorf("%s: %s holds a raw %s expression, which GitLab never expands", dialect, rootFile, rawExprOpen)
			}
			files[rootFile] = data
		}
	}
	rep, err := e.led.Finish(uses)
	if err != nil {
		return nil, rep, fmt.Errorf("%s: %w", dialect, err)
	}
	vres.Report(rep)
	return files, rep, nil
}

// pipelineFile writes one pipeline as it is, and several pipelines folded into one (see emit_multi.go).
func (e *emitter) pipelineFile(pls []*ir.Pipeline) ([]byte, error) {
	pl := pls[0]
	if len(pls) > 1 {
		var err error
		if pl, err = e.foldPipelines(pls); err != nil {
			return nil, err
		}
	}
	root, err := e.document(pl)
	if err != nil {
		return nil, fmt.Errorf("pipeline %s: %w", pl.ID, err)
	}
	body, err := marshalDoc(root)
	if err != nil || len(pls) > 1 {
		return body, err
	}
	spec, ok := extOf(pl.Ext)[extSpec].(map[string]any)
	if !ok {
		return body, nil
	}
	hn, err := specNode(spec, pl.Inputs)
	if err != nil {
		return nil, err
	}
	head, err := marshalDoc(hn)
	if err != nil {
		return nil, err
	}
	return append(append(head, "---\n"...), body...), nil
}

func (e *emitter) document(pl *ir.Pipeline) (*yaml.Node, error) {
	pl = e.splitImages(pl)
	plan, err := e.stagePlan(pl)
	if err != nil {
		return nil, err
	}
	if err := e.markDeadOutputs(pl); err != nil {
		return nil, err
	}
	e.markUnownedInputs(pl)
	e.foreignExt("pipeline/"+pl.ID, pl.Ext)
	if !pipelineName(pl.Name) {
		e.loseName("pipeline/"+pl.ID, pl.Name, "")
	}
	ext := extOf(pl.Ext)
	c := &pipeCtx{pl: pl, plan: plan, preps: map[string]*jobPrep{}, defKeys: defaultKeySet(ext), keys: jobKeys(pl, e.from != nil)}
	for _, j := range allJobs(pl) {
		if _, dup := c.preps[j.ID]; dup {
			return nil, fmt.Errorf("job id %q is used twice", j.ID)
		}
		p := newJobPrep(e.pathOf(pl.ID, j), j)
		p.imgPath = e.splits[j].imgPath
		if err := e.prepSteps(p); err != nil {
			return nil, err
		}
		c.preps[j.ID] = p
	}
	m := newMap()
	if inc := localFreeIncludes(ext["include"]); len(inc) > 0 {
		n, err := plainNode(inc)
		if err != nil {
			return nil, err
		}
		m.set("include", n)
	}
	if len(plan.list) > 0 {
		m.set("stages", strSeq(plan.list))
	}
	var marks []string
	if e.fold != nil {
		marks, err = e.foldHeader(m, c)
	} else {
		marks, err = e.headerKeys(m, pl, ext, c)
	}
	if err != nil {
		return nil, err
	}
	if err := e.jobEntries(m, c); err != nil {
		return nil, err
	}
	skip := map[string]bool{"include": true, extSpec: true, extStages: true, extVariables: true, extWorkflow: true, extDefault: true, extHidden: true}
	if err := m.addExtra(ext, skip); err != nil {
		return nil, err
	}
	if len(m.n.Content) == 0 {
		return nil, fmt.Errorf("pipeline has no jobs")
	}
	head := strings.Join(append(marks, e.drain()...), "\n")
	if first := m.n.Content[0]; head != "" {
		first.HeadComment = strings.TrimPrefix(head+"\n"+first.HeadComment, "\n")
		first.HeadComment = strings.TrimSuffix(first.HeadComment, "\n")
	}
	return m.n, nil
}

// localFreeIncludes drops include entries that name local files, which Parse already merged.
func localFreeIncludes(v any) []any {
	l, _ := v.([]any)
	var out []any
	for _, it := range l {
		switch x := it.(type) {
		case string:
			if strings.Contains(x, scheme) {
				out = append(out, it)
			}
		case map[string]any:
			if _, local := x["local"]; !local {
				out = append(out, it)
			}
		}
	}
	return out
}

func defaultKeySet(ext map[string]any) map[string]bool {
	set := map[string]bool{}
	d, _ := ext[extDefault].(map[string]any)
	for _, k := range defaultKeyOrder {
		if _, ok := d[k]; ok {
			set[k] = true
		} else if _, ok := ext[k]; ok && deprecatedGlobals[k] {
			set[k] = true
		}
	}
	return set
}

// headerKeys writes variables, workflow, default and the hidden jobs, and returns the pipeline-level markers.
func (e *emitter) headerKeys(m *omap, pl *ir.Pipeline, ext map[string]any, c *pipeCtx) ([]string, error) {
	pp := "pipeline/" + pl.ID
	vars, extras, marks, err := e.pipelineVars(pp, pl, ext)
	if err != nil {
		return nil, err
	}
	if len(vars) > 0 {
		n, err := e.vars(pp, caps.PipelineEnv, vars, extras)
		if err != nil {
			return nil, err
		}
		if len(n.Content) > 0 {
			m.set("variables", n)
		}
	}
	w, err := e.workflow(pl)
	if err != nil {
		return nil, err
	}
	m.set(extWorkflow, w)
	tm, err := e.triggerMarks(pl, w)
	if err != nil {
		return nil, err
	}
	marks = append(marks, tm...)
	if d, ok := ext[extDefault]; ok {
		n, err := plainNode(d)
		if err != nil {
			return nil, err
		}
		m.set(extDefault, n)
	}
	hidden, _ := ext[extHidden].(map[string]any)
	for _, name := range sortedAny(hidden) {
		n, err := plainNode(hidden[name])
		if err != nil {
			return nil, err
		}
		m.set(name, n)
	}
	if pl.Concurrency != nil {
		g, err := e.field(pp, caps.ScriptInterpolation, pl.Concurrency.Group)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(g) == "" {
			g = pl.ID
		}
		c.group = g
		e.cancelLost(pp, pl.Concurrency)
		marks = append(marks, emitkit.Marker(caps.PipelineConcurrency, pp))
	}
	rm, err := refMarks(pp, pl)
	if err != nil {
		return nil, err
	}
	return append(marks, rm...), nil
}

// markUnownedInputs records the inputs of pl that pipelineVars leaves without a variable, before any script
// that reads them is converted.
func (e *emitter) markUnownedInputs(pl *ir.Pipeline) {
	if _, header := extOf(pl.Ext)[extSpec]; header {
		return
	}
	vars := append([]ir.Var{}, pl.Env...)
	for _, in := range pl.Inputs {
		name := inputVar(in.Name)
		if name == "" || hasVar(vars, name) {
			e.dropInput(in.Name)
			continue
		}
		vars = append(vars, ir.Var{Name: name})
	}
}

// pipelineVars joins pipeline env with the inputs, which GitLab can only carry as variables with a description.
func (e *emitter) pipelineVars(pp string, pl *ir.Pipeline, ext map[string]any) ([]ir.Var, map[string]any, []string, error) {
	vars := append([]ir.Var{}, pl.Env...)
	extras := map[string]any{}
	if raw, ok := ext[extVariables].(map[string]any); ok {
		for k, v := range raw {
			extras[k] = v
		}
	}
	if _, header := ext[extSpec]; len(pl.Inputs) == 0 || header {
		return vars, extras, nil, nil
	}
	var kept []ir.Input
	for _, in := range pl.Inputs {
		name := inputVar(in.Name)
		if name == "" || hasVar(vars, name) {
			e.lose(pp, caps.PipelineInputs, "input "+in.Name+" is not a unique variable name; the variable "+name+" is taken")
			continue
		}
		vars = append(vars, ir.Var{Name: name, Value: ir.Value{Lit: in.Default}})
		if in.Description != "" {
			e.flattenedNote(pp+"/input/"+in.Name, caps.PipelineInputs, in.Description)
			d, err := e.field(pp, caps.PipelineInputs, in.Description)
			if err != nil {
				return nil, nil, nil, err
			}
			extras[name] = map[string]any{"description": d}
		}
		kept = append(kept, in)
	}
	if len(kept) == 0 {
		return vars, extras, nil, nil
	}
	arg, err := markerArg(kept)
	if err != nil {
		return nil, nil, nil, err
	}
	return vars, extras, []string{emitkit.Marker(caps.PipelineInputs, pp), arg}, nil
}

func (e *emitter) jobEntries(m *omap, c *pipeCtx) error {
	e.pl = c.pl
	for _, j := range allJobs(c.pl) {
		p := c.preps[j.ID]
		n, comments, err := e.jobNode(c, p)
		if err != nil {
			return fmt.Errorf("job %s: %w", j.ID, err)
		}
		comments = append(append(e.foldJobMarkers(j), e.splitMarkers(c.pl.ID, j)...), comments...)
		key := c.key(j.ID)
		if m.has(key) {
			return fmt.Errorf("job id %q collides with a top-level key", key)
		}
		if key != j.ID {
			e.emulate(p.path, caps.ExtPassthrough, "job key "+key, "the id "+j.ID+" is a GitLab keyword or hidden-job name, so the job is written as "+key+" and the id is kept in a marker comment")
			comments = append([]string{jobIDPrefix + j.ID}, comments...)
		}
		m.setComment(key, n, comments)
	}
	return nil
}
