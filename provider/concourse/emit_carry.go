package concourse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
)

// carrySuffix ends the marker path of a carry: the IR fields of a pipeline, job or task that the file cannot say.
const carrySuffix = "/carry"

// carryArg holds those IR fields as YAML text, and the digest of the file text they were written beside. The
// parser applies the fields only when the digest still matches, so an edited job or task keeps what the file says.
type carryArg struct {
	Digest string `json:"digest"`
	Carry  string `json:"carry"`
}

// carry writes a carry marker for an IR value of a Concourse file that another origin wrote.
func (e *emitter) carry(path string, v any, digest string) {
	if e.own {
		return
	}
	body, err := carryYAML(v)
	if err != nil {
		e.lose(path, caps.ExtPassthrough, "the carry marker cannot be written: "+err.Error())
		return
	}
	e.markArg(caps.ExtPassthrough, path+carrySuffix, carryArg{Digest: digest, Carry: body})
}

// carryJob writes the carry of a job: every job field but its steps and Ext.
func (jx *jobEmitter) carryJob(node *yaml.Node) {
	j := *jx.j
	j.Steps, j.Ext = nil, nil
	if j.Manual && isManualEvent(j.If) {
		j.If = nil
	}
	jx.pe.e.carry(jx.mpath, &j, nodeText(node))
}

// carryStep writes the carry of a script task: every step field but its script and Ext.
func (jx *jobEmitter) carryStep(st *ir.Step, name string, node *yaml.Node, body []string) {
	s := *st
	s.Script, s.Ext = nil, nil
	if !slices.Equal(body, st.Script) {
		s.Script = st.Script
	}
	jx.pe.e.carry(jx.sm(name), &s, nodeText(node))
}

// carryPipeline writes the carry of a pipeline: every pipeline field but its jobs and Ext. The digest covers the
// resources and the gets of the jobs, which decide the triggers.
func (pe *pipeEmitter) carryPipeline(top *yaml.Node) {
	p := *pe.pl
	p.Jobs, p.Ext = nil, nil
	p.Triggers = make([]ir.Trigger, len(pe.pl.Triggers))
	for i, t := range pe.pl.Triggers {
		t.Ext = nil
		p.Triggers[i] = t
	}
	p.Finally = make([]*ir.Job, len(pe.pl.Finally))
	for i, f := range pe.pl.Finally {
		p.Finally[i] = withoutExt(f)
	}
	pe.e.carry(pe.mpath, &p, pipelineDigest(top))
}

// withoutExt copies a job without its Ext and the Ext of its steps; the carry holds no Ext of another provider.
func withoutExt(j *ir.Job) *ir.Job {
	c := *j
	c.Ext = nil
	c.Steps = make([]*ir.Step, len(j.Steps))
	for i, st := range j.Steps {
		s := *st
		s.Ext = nil
		c.Steps[i] = &s
	}
	return &c
}

// pipelineDigest is the text of the resources and of the top-level get steps of every job.
func pipelineDigest(top *yaml.Node) string {
	var res, jobs *yaml.Node
	for i := 0; i+1 < len(top.Content); i += 2 {
		switch top.Content[i].Value {
		case "resources":
			res = top.Content[i+1]
		case "jobs":
			jobs = top.Content[i+1]
		}
	}
	var gets []any
	if jobs != nil {
		for _, j := range jobs.Content {
			for i := 0; i+1 < len(j.Content); i += 2 {
				if j.Content[i].Value != "plan" {
					continue
				}
				for _, step := range j.Content[i+1].Content {
					if hasKey(step, "get") {
						gets = append(gets, nodeValue(step))
					}
				}
			}
		}
	}
	var r any
	if res != nil {
		r = nodeValue(res)
	}
	return digestOf([]any{r, gets})
}

func hasKey(n *yaml.Node, key string) bool {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return true
		}
	}
	return false
}

// nodeText is the digest text of a YAML node.
func nodeText(n *yaml.Node) string { return digestOf(nodeValue(n)) }

// nodeValue turns a node into maps, lists and strings, so a digest does not depend on scalar types or order.
func nodeValue(n *yaml.Node) any {
	switch n.Kind {
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			m[n.Content[i].Value] = nodeValue(n.Content[i+1])
		}
		return m
	case yaml.SequenceNode:
		l := make([]any, len(n.Content))
		for i, c := range n.Content {
			l[i] = nodeValue(c)
		}
		return l
	case yaml.AliasNode:
		return nodeValue(n.Alias)
	case yaml.DocumentNode:
		if len(n.Content) == 1 {
			return nodeValue(n.Content[0])
		}
	}
	return n.Value
}

// plainValue is nodeValue for a parsed value.
func plainValue(v any) any {
	switch t := v.(type) {
	case *omap:
		m := make(map[string]any, len(t.Order))
		for _, k := range t.Order {
			m[k] = plainValue(t.Vals[k])
		}
		return m
	case []any:
		l := make([]any, len(t))
		for i, e := range t {
			l[i] = plainValue(e)
		}
		return l
	case ynode.Scal:
		return t.Text()
	}
	return nil
}

func digestOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
