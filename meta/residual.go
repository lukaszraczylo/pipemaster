package meta

import (
	"github.com/lukaszraczylo/pipemaster/ir"
)

// Residual holds the display names of pipelines, jobs and steps by source IR path. GitLab has no
// key for most of them; each fills only a name the parsed node left empty.
type Residual struct {
	Name string `yaml:"name,omitempty"`
}

// CollectResidual records every display name.
func CollectResidual(p *ir.Project) map[string]Residual {
	out := map[string]Residual{}
	for _, n := range ir.Nodes(p) {
		var r Residual
		switch {
		case n.Pipeline != nil:
			r.Name = n.Pipeline.Name
		case n.Job != nil:
			r.Name = n.Job.Name
		case n.Step != nil:
			r.Name = n.Step.Name
		}
		if r.Name != "" {
			out[n.Path] = r
		}
	}
	return out
}

// applyResidual fills names the parsed node left empty. It returns the paths in m that match a node.
func applyResidual(p *ir.Project, m map[string]Residual) map[string]bool {
	found := map[string]bool{}
	for _, n := range ir.Nodes(p) {
		r, ok := m[n.Path]
		if !ok {
			continue
		}
		found[n.Path] = true
		switch {
		case n.Pipeline != nil:
			fill(&n.Pipeline.Name, r.Name)
		case n.Job != nil:
			fill(&n.Job.Name, r.Name)
		case n.Step != nil:
			fill(&n.Step.Name, r.Name)
		}
	}
	return found
}

func fill(dst *string, v string) {
	if *dst == "" {
		*dst = v
	}
}
