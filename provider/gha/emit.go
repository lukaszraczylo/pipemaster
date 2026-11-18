package gha

import (
	"fmt"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

const workflowExt = ".yml"

// Emit writes one workflow file per pipeline at <Dir>/<pipeline id>.yml. Every feature use goes
// through the ledger; Finish fails the call when one was never accounted for.
func (a *adapter) Emit(p *ir.Project) (map[string][]byte, *report.Report, error) {
	p, idRenames := withValidJobIDs(p)
	e := &emitter{d: a.d, from: sourceDialect(originProvider(p)), led: emitkit.NewLedger("ir", a.d.ID, a.Caps())}
	uses := ir.Features(p)
	for _, u := range uses {
		e.led.Decide(u)
	}
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
	rep, err := e.led.Finish(uses)
	if err != nil {
		return nil, rep, fmt.Errorf("%s: %w", a.d.ID, err)
	}
	return files, rep, nil
}

func (e *emitter) pipelineFile(pl *ir.Pipeline) (string, []byte, error) {
	if pl.ID == "" || strings.ContainsAny(pl.ID, `/\`) || pl.ID == "." || pl.ID == ".." {
		return "", nil, fmt.Errorf("pipeline id %q cannot be a file name", pl.ID)
	}
	root, err := e.workflow(pl)
	if err != nil {
		return "", nil, fmt.Errorf("pipeline %s: %w", pl.ID, err)
	}
	data, err := marshalDoc(root)
	if err != nil {
		return "", nil, fmt.Errorf("pipeline %s: %w", pl.ID, err)
	}
	return path.Join(e.d.Dir, pl.ID+workflowExt), data, nil
}

func (e *emitter) workflow(pl *ir.Pipeline) (*yaml.Node, error) {
	pp := "pipeline/" + pl.ID
	ext := extMap(pl.Ext, e.d.ID)
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
		m.set("permissions", e.permissions(pp, pl.Permissions))
	}
	if len(pl.Env) > 0 {
		env, err := e.vars(pp, pl.Env)
		if err != nil {
			return nil, err
		}
		m.set("env", env)
	}
	if pl.Concurrency != nil {
		c, err := e.concurrency(pp, pl.Concurrency, ext)
		if err != nil {
			return nil, err
		}
		m.set("concurrency", c)
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
	for _, j := range pl.Jobs {
		if j != nil {
			c.mainIDs = append(c.mainIDs, j.ID)
		}
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
	return jm.n, nil
}

// permissions writes the read-all and write-all shorthands for Scopes["*"], else a scope mapping.
func (e *emitter) permissions(pp string, p *ir.Permissions) *yaml.Node {
	if lvl, ok := p.Scopes[scopeAll]; ok && len(p.Scopes) == 1 && !p.OIDC {
		return str(lvl + "-all")
	}
	scopes := map[string]any{}
	for k, v := range p.Scopes {
		if k == scopeAll {
			e.lose(pp, caps.PipelinePermissions, `scope "*" cannot sit beside other permissions`)
			continue
		}
		scopes[k] = v
	}
	m := newMap()
	if p.OIDC {
		scopes[idToken] = "write"
	}
	for _, k := range sortedKeys(scopes) {
		m.set(k, str(scopes[k].(string)))
	}
	if m.empty() {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: tagMap, Style: yaml.FlowStyle}
	}
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
