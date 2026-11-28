package gha

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/provider/inline"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/vars"
)

const (
	workflowExt = ".yml"
	scopeSeg    = "/scope/"
	eventSeg    = "/event/"
	// runGroup is the concurrency group of the per-run default; defaultGroup is how the parser reads it.
	runGroup     = "${{ github.run_id }}"
	defaultGroup = "${{ gha.github.run_id }}"
)

// Emit writes one workflow file per pipeline at <Dirs[0]>/<pipeline id>.yml. Every feature use goes
// through the ledger; Finish fails the call when one was never accounted for.
func (a *adapter) Emit(p *ir.Project) (map[string][]byte, *report.Report, error) {
	return inline.Emit(p, a.d.ID, a.emitFlat)
}

func (a *adapter) emitFlat(p *ir.Project) (map[string][]byte, *report.Report, error) {
	p, idRenames := withValidJobIDs(withNativeChildren(p, a.d.ID))
	p, vres := vars.Prepare(p, a.d.ID)
	p = bindRefEnv(p, a.d.ID)
	fresh := provider.FreshGitLabDefaults(p, a.d.ID)
	// The gated jobs come from the source conditions: a rule of the source can have the shape of the gate.
	gated := provider.GatedJobs(p, a.d.ID)
	p, noForm, injected := provider.WithDefaultTriggers(p, a.d.ID)
	p, srcEvents, srcLost := withGitLabSources(p, a.d.ID, fresh, gated)
	e := &emitter{origin: originProvider(p), d: a.d, from: sourceDialect(originProvider(p)), led: emitkit.NewLedger("ir", a.d.ID, a.Caps()),
		jobGuards: p.Origin.Guarded}
	e.injected = injected
	for id, arg := range srcEvents {
		// A GitLab file with the default triggers keeps its push and tag triggers, so only the added kinds need a marker.
		if len(arg.Kinds) > 0 && fresh[id] {
			delete(e.injected, id)
		}
	}
	e.srcEvents = srcEvents
	e.srcLost = srcLost
	uses := ir.Features(p)
	for _, u := range uses {
		e.led.Decide(u)
		e.dialectNotes(u)
	}
	e.noteDefaultEvents(noForm)
	for _, r := range idRenames {
		e.emulate(r.path, caps.ExtPassthrough, "job id renamed",
			"job id "+r.from+" is not valid on "+a.d.ID+"; written as "+r.to+", restored only with round-trip metadata")
	}
	files := map[string][]byte{}
	if p != nil {
		for _, pl := range p.Pipelines {
			if pl == nil {
				continue
			}
			name, data, err := e.pipelineFile(pl)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", a.d.ID, err)
			}
			if _, dup := files[name]; dup {
				return nil, nil, fmt.Errorf("%s: two pipelines emit %s", a.d.ID, name)
			}
			files[name] = data
		}
	}
	rep, err := e.led.FinishNoted(uses)
	if err != nil {
		return nil, rep, fmt.Errorf("%s: %w", a.d.ID, err)
	}
	vres.Report(rep)
	return files, rep, nil
}

// dialectNotes records what the dialect says about one use: a native caveat, or a name it rejects.
func (e *emitter) dialectNotes(u ir.FeatureUse) {
	for _, c := range e.d.Caveats {
		if c.Feature == u.Feature {
			e.led.Note(u, caps.Support{Level: caps.Native}, c.Caveat)
		}
	}
	if u.Feature != caps.ConfigName {
		return
	}
	family, name, ok := ir.ConfigRef(u.IRPath)
	if !ok {
		return
	}
	rule, exact := e.d.nameRule(family, name)
	if rule == "" {
		return
	}
	kind, why := "secret", "names that start with "+rule
	if family == expr.PrefixVars {
		kind = "variable"
	}
	if exact {
		why = "the name " + rule
	}
	e.led.Note(u, caps.Support{
		Level:    caps.External,
		Strategy: "create " + kind + " " + name + ": " + e.d.title() + " rejects " + why + "; rename it and update the reference",
	}, "")
}

func (e *emitter) pipelineFile(pl *ir.Pipeline) (string, []byte, error) {
	if pl.ID == "" || strings.ContainsAny(pl.ID, `/\`) || pl.ID == "." || pl.ID == ".." {
		return "", nil, fmt.Errorf("pipeline id %q cannot be a file name", pl.ID)
	}
	if e.injected[pl.ID] {
		if err := e.markArg(caps.TriggerPush, "pipeline/"+pl.ID, provider.DefaultTriggersArg{Default: true}); err != nil {
			return "", nil, err
		}
	}
	if arg, ok := e.srcEvents[pl.ID]; ok {
		if err := e.markArg(caps.TriggerPush, "pipeline/"+pl.ID, arg); err != nil {
			return "", nil, err
		}
	}
	e.loseSources(pl)
	root, err := e.workflow(pl)
	if err != nil {
		return "", nil, fmt.Errorf("pipeline %s: %w", pl.ID, err)
	}
	data, err := marshalDoc(root)
	if err != nil {
		return "", nil, fmt.Errorf("pipeline %s: %w", pl.ID, err)
	}
	return path.Join(e.d.Dirs[0], pl.ID+workflowExt), data, nil
}

func (e *emitter) workflow(pl *ir.Pipeline) (*yaml.Node, error) {
	pp := "pipeline/" + pl.ID
	ext := e.ext(pl.Ext)
	e.foreignExt(pp, pl.Ext)
	m := newMap()
	if pl.Name != "" {
		m.set("name", str(pl.Name))
	}
	jobManual := ""
	for _, j := range pl.Jobs {
		if j != nil && j.Manual && jobManual == "" {
			jobManual = jobPath(pl.ID, j.ID)
		}
	}
	on, err := e.on(pl, jobManual)
	if err != nil {
		return nil, err
	}
	m.set("on", on)
	if pl.Permissions != nil {
		if n := e.permissions(pp, pl.Permissions); n != nil {
			m.set("permissions", n)
		}
		if pl.Permissions.OIDC && e.d.OIDCKey != "" {
			m.set(e.d.OIDCKey, boolNodeOf(true))
		}
	}
	if len(pl.Env) > 0 {
		env, err := e.vars(pp, pl.Env)
		if err != nil {
			return nil, err
		}
		if len(env.Content) > 0 {
			m.set("env", env)
		}
	}
	if pl.Concurrency != nil {
		c, err := e.concurrency(pp, pl.Concurrency, ext)
		if err != nil {
			return nil, err
		}
		m.set("concurrency", c)
	} else if e.d.DefaultCancel && cancelsByDefault(pl) {
		m.set("concurrency", e.runGroupConcurrency(pp))
	}
	if err := m.addExtra(ext, map[string]bool{extEvents: true, extConcurrency: pl.Concurrency != nil}); err != nil {
		return nil, err
	}
	jobs, err := e.jobs(pl)
	if err != nil {
		return nil, err
	}
	m.set("jobs", jobs)
	if head := append(e.marks, e.drain()...); len(head) > 0 {
		m.n.Content[0].HeadComment = strings.Join(head, "\n")
	}
	e.marks = nil
	return m.n, nil
}

func (e *emitter) jobs(pl *ir.Pipeline) (*yaml.Node, error) {
	jm := newMap()
	c := jobCtx{pl: pl}
	e.changeAtoms, e.changesBlocked = nil, false
	for _, j := range pl.Jobs {
		if j != nil {
			c.mainIDs = append(c.mainIDs, j.ID)
		}
	}
	for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
		e.changesBlocked = e.changesBlocked || (j != nil && j.ID == changesJobID)
	}
	for _, group := range []struct {
		jobs    []*ir.Job
		finally bool
	}{{pl.Jobs, false}, {pl.Finally, true}} {
		c.finally = group.finally
		for _, j := range group.jobs {
			if j == nil {
				continue
			}
			if jm.has(j.ID) {
				return nil, fmt.Errorf("job id %q is used twice", j.ID)
			}
			n, comments, err := e.jobNode(c, j)
			if err != nil {
				return nil, err
			}
			jm.setComment(j.ID, n, comments)
		}
	}
	if len(e.changeAtoms) > 0 {
		n, err := e.changesJob(pl)
		if err != nil {
			return nil, err
		}
		jm.n.Content = append([]*yaml.Node{str(changesJobID), n}, jm.n.Content...)
		jm.keys[changesJobID] = true
	}
	if jm.empty() {
		e.placeholderJob(jm, pl)
	}
	return jm.n, nil
}

// emptyJobID names the job that keeps a workflow with no jobs valid.
const emptyJobID = "pipemaster-empty"

func (e *emitter) placeholderJob(jm *omap, pl *ir.Pipeline) {
	pp := "pipeline/" + pl.ID
	job, step := newMap(), newMap()
	job.set("runs-on", str(defaultRunner))
	step.set("run", str(noopScript))
	job.set("steps", seqOf(step.n))
	jm.set(emptyJobID, job.n)
	e.emulate(pp, caps.StepScript, "placeholder job "+emptyJobID, "a GHA workflow needs at least one job")
	e.mark(caps.StepScript, pp)
}

// permissions writes the read-all and write-all shorthands for Scopes["*"], else a scope mapping. A scope the
// dialect has no name for is dropped with a lost entry per scope; the result is nil when the dialect carries the
// whole grant in another key.
func (e *emitter) permissions(pp string, p *ir.Permissions) *yaml.Node {
	if lvl, ok := p.Scopes[scopeAll]; ok && len(p.Scopes) == 1 && !p.OIDC {
		return str(lvl + "-all")
	}
	scopes := map[string]any{}
	for k, v := range p.Scopes {
		switch {
		case k == scopeAll:
			e.lose(pp, caps.PipelinePermissions, `scope "*" cannot sit beside other permissions`)
		case !e.hasScope(k):
			e.dropScope(pp, k)
		default:
			scopes[k] = v
		}
	}
	if p.OIDC && e.d.OIDCKey == "" {
		if e.hasScope(idToken) {
			scopes[idToken] = "write"
		} else {
			e.lose(pp+scopeSeg+idToken, caps.PipelinePermissions, e.d.NoOIDC)
		}
	}
	m := newMap()
	for _, k := range sortedKeys(scopes) {
		m.set(k, str(scopes[k].(string)))
	}
	if m.empty() {
		if p.OIDC && e.d.OIDCKey != "" {
			return nil
		}
		return &yaml.Node{Kind: yaml.MappingNode, Tag: tagMap, Style: yaml.FlowStyle}
	}
	return m.n
}

func (e *emitter) dropScope(pp, k string) {
	e.lose(pp+scopeSeg+k, caps.PipelinePermissions, "scope "+k+" is not a "+e.d.title()+" permission; dropped")
}

// jobPermissions writes the job's permissions Ext filtered like the workflow-level mapping. The unfiltered value
// stays in the round-trip Ext record, so a return to the source dialect restores the dropped scopes.
func (e *emitter) jobPermissions(m *omap, jp string, ext map[string]any, consumed map[string]bool) error {
	v, ok := ext["permissions"]
	if !ok {
		return nil
	}
	consumed["permissions"] = true
	for _, ig := range e.d.Ignored {
		if ig.Feature == caps.PipelinePermissions {
			e.lose(jp, caps.PipelinePermissions, ig.Caveat)
		}
	}
	scopes, isMap := v.(map[string]any)
	if !isMap {
		n, err := plainNode(v)
		if err != nil {
			return err
		}
		m.set("permissions", n)
		return nil
	}
	out := newMap()
	for _, k := range sortedKeys(scopes) {
		switch {
		case k == idToken && !e.hasScope(k):
			e.lose(jp+scopeSeg+k, caps.PipelinePermissions, e.d.NoOIDC)
		case !e.hasScope(k):
			e.dropScope(jp, k)
		default:
			n, err := plainNode(scopes[k])
			if err != nil {
				return err
			}
			out.set(k, n)
		}
	}
	if out.empty() {
		m.set("permissions", &yaml.Node{Kind: yaml.MappingNode, Tag: tagMap, Style: yaml.FlowStyle})
		return nil
	}
	m.set("permissions", out.n)
	return nil
}

func (e *emitter) hasScope(name string) bool {
	return len(e.d.Scopes) == 0 || slices.Contains(e.d.Scopes, name)
}

// cancelsByDefault reports whether the dialect would cancel earlier runs of pl: it has a push or pull_request event.
func cancelsByDefault(pl *ir.Pipeline) bool {
	for _, t := range pl.Triggers {
		if t.Kind == ir.TriggerPush || t.Kind == ir.TriggerTag || t.Kind == ir.TriggerPR {
			return true
		}
	}
	return false
}

// runGroupConcurrency gives every run its own group, which switches the dialect's default cancelling off.
func (e *emitter) runGroupConcurrency(pp string) *yaml.Node {
	m := newMap()
	m.set("group", str(runGroup))
	m.set("cancel-in-progress", boolNodeOf(false))
	e.emulate(pp, caps.PipelineConcurrency, "concurrency group per run",
		e.d.title()+" cancels earlier push and pull_request runs while concurrency is unset; the source had no concurrency, so each run gets its own group")
	e.mark(caps.PipelineConcurrency, pp)
	return m.n
}

func (e *emitter) concurrency(pp string, c *ir.Concurrency, ext map[string]any) (*yaml.Node, error) {
	rest, _ := ext[extConcurrency].(map[string]any)
	m := newMap()
	if c.Group != "" {
		g, err := e.template(pp, c.Group)
		if err != nil {
			return nil, err
		}
		m.set("group", str(g))
	}
	if _, raw := rest["cancel-in-progress"]; !raw && c.CancelInProgress {
		m.set("cancel-in-progress", boolNodeOf(true))
	}
	return m.n, m.addExtra(rest, nil)
}

func originProvider(p *ir.Project) string {
	if p == nil {
		return ""
	}
	return p.Origin.Provider
}
