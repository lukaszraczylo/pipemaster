package gitlab

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// pipeCtx is what a job needs to know about the pipeline it sits in.
type pipeCtx struct {
	pl      *ir.Pipeline
	plan    *stagePlan
	preps   map[string]*jobPrep
	defKeys map[string]bool
	keys    map[string]string
	group   string
}

// key is the top-level YAML key a job is written under.
func (c *pipeCtx) key(id string) string {
	if k, ok := c.keys[id]; ok {
		return k
	}
	return id
}

func (c *pipeCtx) isFinally(id string) bool {
	for _, f := range c.pl.Finally {
		if f != nil && f.ID == id {
			return true
		}
	}
	return false
}

// declares reports whether the job's artifacts block exists, which is what lets a needs entry carry artifacts.
func (c *pipeCtx) declares(id string) bool {
	p := c.preps[id]
	if p == nil {
		return false
	}
	if len(p.j.Artifacts) > 0 || p.art.seen {
		return true
	}
	a, _ := extOf(p.j.Ext)[extArtifacts].(map[string]any)
	u, _ := a["untracked"].(bool)
	return u
}

func (c *pipeCtx) artifactNames(id string) []string {
	p := c.preps[id]
	if p == nil {
		return nil
	}
	names := append([]string{}, p.uploads...)
	for _, a := range p.j.Artifacts {
		names = append(names, a.Name)
	}
	return names
}

// needsNode writes needs. It is omitted when the stage order already implies the edges, and for a
// finally job with no edges, which must wait for its stage.
func (e *emitter) needsNode(c *pipeCtx, p *jobPrep) (*yaml.Node, error) {
	j := p.j
	if c.plan.derived[j.ID] {
		return nil, nil
	}
	ext := extOf(j.Ext)
	raw, _ := ext[extNeedsKey].([]any)
	rawJobs := map[string]bool{}
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			if name, ok := m["job"].(string); ok {
				rawJobs[name] = true
			}
		}
	}
	extras, _ := ext[ExtNeedsExtra].(map[string]any)
	deps, hasDeps := stageList(ext[extDeps])
	finally := c.isFinally(j.ID)
	downloaded := e.downloadedFrom(c, p)
	out := seqOf()
	for _, n := range j.Needs {
		if rawJobs[n.JobID] {
			continue
		}
		if finally && c.isFinally(n.JobID) {
			e.lose(p.path, caps.JobNeeds, "finally job needs another finally job "+n.JobID+", which share the last stage")
			continue
		}
		shown := n
		shown.JobID = c.key(n.JobID)
		entry, bad, err := needEntry(shown, n.JobID, c.declares(n.JobID), downloaded[n.JobID], hasDeps, deps, extras[n.JobID])
		if err != nil {
			return nil, err
		}
		if bad != "" {
			e.lose(p.path, caps.JobNeeds, "matrix instance "+strconv.Quote(bad)+" of need "+n.JobID+" is not a valid GitLab need name; the need selects the whole job")
		}
		out.Content = append(out.Content, entry)
	}
	for _, r := range raw {
		n, err := plainNode(r)
		if err != nil {
			return nil, err
		}
		out.Content = append(out.Content, n)
	}
	if len(out.Content) == 0 && finally {
		return nil, nil
	}
	return out, nil
}

// needEntry writes one needs entry. GitLab downloads the producer's artifacts by default when it has any
// (and dependencies lists it), so the map form appears only when the IR says otherwise or an action asked for them.
func needEntry(n ir.Need, origID string, producerHasArtifacts, downloaded, hasDeps bool, deps []string, extra any) (node *yaml.Node, badInstance string, err error) {
	x, _ := extra.(map[string]any)
	job := n.JobID
	if inst, _ := x[ExtNeedsInstance].(string); inst != "" {
		x = maps.Clone(x)
		delete(x, ExtNeedsInstance)
		// The instance name starts with the job name, which a fold prefix may have changed.
		switch at := strings.Index(inst, ": ["); {
		case at > 0:
			job = n.JobID + inst[at:]
		case inst == origID || inst == n.JobID:
			job = n.JobID
		default:
			badInstance = inst
		}
	}
	m := newMap()
	m.set("job", str(job))
	explicit := false
	if producerHasArtifacts {
		want := n.Artifacts || downloaded
		def := !hasDeps || slices.Contains(deps, n.JobID)
		if want != def || downloaded {
			m.set("artifacts", boolNodeOf(want))
			explicit = true
		}
	}
	if n.Optional {
		m.set("optional", boolNodeOf(true))
		explicit = true
	}
	if len(x) > 0 {
		if err := m.addExtra(x, nil); err != nil {
			return nil, badInstance, err
		}
		explicit = true
	}
	if !explicit {
		return str(job), badInstance, nil
	}
	return m.n, badInstance, nil
}

// downloadedFrom maps each needed job whose artifacts an action downloads, and reports downloads that match no needed job.
func (e *emitter) downloadedFrom(c *pipeCtx, p *jobPrep) map[string]bool {
	out := map[string]bool{}
	for _, d := range p.downs {
		var jobs []downJob
		for _, n := range p.j.Needs {
			names := c.artifactNames(n.JobID)
			if d.name == "" && c.declares(n.JobID) || slices.Contains(names, d.name) {
				out[n.JobID] = true
				jobs = append(jobs, downJob{ID: n.JobID, Was: n.Artifacts})
			}
		}
		if len(jobs) == 0 {
			e.lose(p.path, caps.JobNeedsArtifacts, "download of artifact "+d.name+" matches no job in needs")
		}
		if err := e.markDownload(d, p, jobs); err != nil {
			e.comment("download source not carried: " + err.Error())
		}
	}
	return out
}

// jobNode builds one job mapping in the fixed key order.
func (e *emitter) jobNode(c *pipeCtx, p *jobPrep) (*yaml.Node, []string, error) {
	j := p.j
	ext := extOf(j.Ext)
	finally := c.isFinally(j.ID)
	m := newMap()
	skip := map[string]bool{"stage": true, ExtDerivedNeeds: true, ExtNoScript: true, ExtRulesAlways: true, extNeedsKey: true, ExtNeedsExtra: true, "rules": true}
	if s := c.plan.stage[j.ID]; s != "" {
		m.set("stage", str(s))
	}
	if err := e.imageKey(m, p, ext, skip); err != nil {
		return nil, nil, err
	}
	if len(j.Services) > 0 {
		n, err := e.servicesNode(p.path, j.Services, ext[extServices])
		if err != nil {
			return nil, nil, err
		}
		m.set("services", n)
		skip[extServices] = true
	}
	if tags, err := e.tags(p.path, j.Runner); err != nil {
		return nil, nil, err
	} else if tags != nil {
		m.set("tags", tags)
	}
	needs, err := e.needsNode(c, p)
	if err != nil {
		return nil, nil, err
	}
	m.set("needs", needs)
	t, err := e.timing(p.path, j, finally)
	if err != nil {
		return nil, nil, err
	}
	m.set("rules", t.rules)
	if err := e.variablesKey(m, p, ext, skip); err != nil {
		return nil, nil, err
	}
	if err := e.scriptKeys(m, p, ext); err != nil {
		return nil, nil, err
	}
	if err := e.cacheKeyNode(m, p, ext, skip); err != nil {
		return nil, nil, err
	}
	if err := e.artifactsKey(m, p, ext, skip); err != nil {
		return nil, nil, err
	}
	var marks []string
	if err := e.limitKeys(m, p, t, ext, skip, &marks); err != nil {
		return nil, nil, err
	}
	m.set("when", whenNode(t.when))
	if _, own := ext["resource_group"]; !own && c.group != "" {
		m.set("resource_group", str(c.group))
	}
	if err := e.defaultNulls(m, c, p, ext); err != nil {
		return nil, nil, err
	}
	return m.n, append(marks, e.drain()...), m.addExtra(ext, skip)
}

func whenNode(w string) *yaml.Node {
	if w == "" {
		return nil
	}
	return str(w)
}

func (e *emitter) imageKey(m *omap, p *jobPrep, ext map[string]any, skip map[string]bool) error {
	j := p.j
	switch {
	case j.Container != nil:
		n, err := e.imageNode(p.path, *j.Container, ext[extImage])
		if err != nil {
			return err
		}
		m.set("image", n)
		skip[extImage] = true
	case p.image != "":
		m.set("image", str(p.image))
	}
	return nil
}

func (e *emitter) variablesKey(m *omap, p *jobPrep, ext map[string]any, skip map[string]bool) error {
	vars := append([]ir.Var{}, p.j.Env...)
	for _, v := range p.vars {
		if !hasVar(vars, v.Name) {
			vars = append(vars, v)
		}
	}
	if len(vars) == 0 {
		return nil
	}
	n, err := e.vars(p.path, caps.JobEnv, vars, ext[extVariables])
	if err != nil {
		return err
	}
	if len(n.Content) > 0 {
		m.set("variables", n)
	}
	skip[extVariables] = true
	return nil
}

func hasVar(vs []ir.Var, name string) bool {
	return slices.ContainsFunc(vs, func(v ir.Var) bool { return v.Name == name })
}

// scriptKeys writes the three script sections; a job without any script gets a no-op so the file stays valid.
func (e *emitter) scriptKeys(m *omap, p *jobPrep, ext map[string]any) error {
	lines, err := e.outputLines(p)
	if err != nil {
		return err
	}
	var consumers []string
	for _, o := range allJobs(e.pl) {
		if slices.ContainsFunc(o.Needs, func(n ir.Need) bool { return n.JobID == p.j.ID }) {
			consumers = append(consumers, jobRefLines(o)...)
		}
	}
	arg, err := markerArg(refArg{Lines: consumers})
	if err != nil {
		return err
	}
	for i, l := range lines {
		if i == 0 {
			p.add(secScript, str(l), emitkit.Marker(caps.JobOutputs, p.path), arg)
		} else {
			p.add(secScript, str(l))
		}
	}
	_, isTrigger := ext["trigger"]
	noScript, _ := ext[ExtNoScript].(bool)
	isTrigger = isTrigger || noScript
	hasAny := false
	for _, s := range sectionOrder {
		hasAny = hasAny || len(p.sections[s]) > 0
	}
	if !hasAny && !isTrigger {
		held := p.marks
		p.marks = nil
		p.add(secScript, str(noopLine))
		p.marks = held
	}
	if len(p.pending) > 0 || len(p.marks) > 0 {
		if items := p.sections[secScript]; len(items) > 0 {
			last := items[len(items)-1]
			last.FootComment = joinLines(last.FootComment, append(p.marks, p.pending...))
			p.pending, p.marks = nil, nil
		}
	}
	for _, sec := range sectionOrder {
		items := p.sections[sec]
		if len(items) == 0 {
			if sec == secScript && !isTrigger && hasAny {
				m.set(sec, seqOf(str(noopLine)))
			}
			continue
		}
		m.set(sec, seqOf(items...))
	}
	return nil
}

func joinLines(existing string, more []string) string {
	all := more
	if existing != "" {
		all = append([]string{existing}, more...)
	}
	out := ""
	for i, l := range all {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

func (e *emitter) cacheKeyNode(m *omap, p *jobPrep, ext map[string]any, skip map[string]bool) error {
	n, err := e.cacheNodes(p.path, p, ext[extCache])
	if err != nil {
		return err
	}
	if n != nil {
		m.set("cache", n)
		skip[extCache] = true
	}
	return nil
}

func (e *emitter) artifactsKey(m *omap, p *jobPrep, ext map[string]any, skip map[string]bool) error {
	n, err := e.artifactsNode(p.path, p, ext[extArtifacts])
	if err != nil {
		return err
	}
	if n != nil {
		m.set("artifacts", n)
		skip[extArtifacts] = true
	}
	return nil
}

// limitKeys writes parallel, retry, timeout, allow_failure and environment.
func (e *emitter) limitKeys(m *omap, p *jobPrep, t jobTiming, ext map[string]any, skip map[string]bool, marks *[]string) error {
	j := p.j
	if j.Matrix != nil {
		n, mk, err := e.parallelNode(p.path, j.Matrix, jobRefLines(j), ext[extParallel])
		if err != nil {
			return err
		}
		if n != nil {
			m.set("parallel", n)
			skip[extParallel] = true
			*marks = append(*marks, mk...)
		}
	}
	if j.Retry != nil {
		n, err := e.retryNode(p.path, j.Retry, ext["retry"])
		if err != nil {
			return err
		}
		m.set("retry", n)
		skip["retry"] = true
	}
	if j.Timeout != nil {
		m.set("timeout", e.timeoutNode(p.path, *j.Timeout))
	}
	if _, raw := ext["allow_failure"]; !raw {
		def := j.Manual && t.rules == nil
		if j.AllowFailure != def {
			m.set("allow_failure", boolNodeOf(j.AllowFailure))
		}
	}
	if j.Environment != nil {
		n, err := e.environmentNode(p.path, j.Environment, ext[extEnvironment])
		if err != nil {
			return err
		}
		m.set("environment", n)
		skip[extEnvironment] = true
	}
	return nil
}

// defaultNulls writes key: null for an inheritable key the pipeline defaults set and the job lacks,
// so the defaults written back do not reappear on the job.
func (e *emitter) defaultNulls(m *omap, c *pipeCtx, p *jobPrep, ext map[string]any) error {
	inherit, _ := ext["inherit"].(map[string]any)
	if d, ok := inherit["default"]; ok {
		if b, isBool := d.(bool); isBool && !b {
			return nil
		}
		if _, isList := d.([]any); isList {
			return nil
		}
	}
	j := p.j
	present := map[string]bool{
		"image": j.Container != nil || p.image != "", "services": len(j.Services) > 0,
		"tags": len(j.Runner.Labels) > 0, "cache": m.has("cache"), "artifacts": m.has("artifacts"),
		"before_script": m.has(secBefore), "after_script": m.has(secAfter), "retry": j.Retry != nil, "timeout": j.Timeout != nil,
	}
	for _, k := range defaultKeyOrder {
		if !c.defKeys[k] || present[k] || m.has(k) {
			continue
		}
		if _, own := ext[k]; own {
			continue
		}
		m.set(k, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"})
	}
	return nil
}

var defaultKeyOrder = []string{"after_script", "artifacts", "before_script", "cache", "image", "retry", "services", "tags", "timeout"}
