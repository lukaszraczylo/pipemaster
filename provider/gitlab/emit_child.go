package gitlab

import (
	"fmt"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/inline"
	"github.com/lukaszraczylo/pipemaster/report"
)

// emitChildren writes the child pipeline of each trigger job as its own file at the path that the trigger names,
// with the files that its own children need.
func (a adapter) emitChildren(p *ir.Project, files map[string][]byte, rep *report.Report) error {
	if p == nil {
		return nil
	}
	for _, pl := range p.Pipelines {
		if pl == nil {
			continue
		}
		for _, j := range pl.Jobs {
			if j == nil || !inline.Native(j.Child, dialect) || j.Child.Pipeline == nil || len(j.Child.Files) == 0 {
				continue
			}
			cf, crep, err := a.Emit(&ir.Project{Origin: p.Origin, Pipelines: []*ir.Pipeline{j.Child.Pipeline}})
			if err != nil {
				return fmt.Errorf("child pipeline %s: %w", strings.Join(j.Child.Files, ", "), err)
			}
			for name, data := range cf {
				if name == rootFile {
					name = j.Child.Files[0]
				}
				files[name] = data
			}
			rep.Merge(crep)
		}
	}
	return nil
}

// singleIncludes names the first file of a child include list as the only one: the child pipeline is written as one
// merged file. The trigger of such a job is changed in a copy of the project, with a report entry.
func singleIncludes(p *ir.Project) (*ir.Project, []report.Entry) {
	var notes []report.Entry
	var out *ir.Project
	if p == nil {
		return p, nil
	}
	for _, pl := range p.Pipelines {
		if pl == nil {
			continue
		}
		for _, j := range pl.Jobs {
			if j == nil || !inline.Native(j.Child, dialect) || len(j.Child.Files) < 2 {
				continue
			}
			if out == nil {
				out = ir.Clone(p)
			}
			for _, cj := range findJobs(out, pl.ID, j.ID) {
				files := cj.Child.Files
				cj.Child.Files = files[:1]
				own, _ := cj.Ext[dialect].(map[string]any)
				if trig, ok := own[extTrigger].(map[string]any); ok {
					trig[triggerInclude] = files[0]
				}
				notes = append(notes, report.Entry{
					IRPath: "pipeline/" + pl.ID + "/job/" + j.ID, Feature: caps.ExtPassthrough, Level: caps.Emulated,
					Strategy: "one child file",
					Detail:   "the include list " + strings.Join(files, ", ") + " is written as the one file " + files[0] + ", because GitLab merges the files of the list",
				})
			}
		}
	}
	if out == nil {
		return p, nil
	}
	return out, notes
}

func findJobs(p *ir.Project, pid, jid string) []*ir.Job {
	var out []*ir.Job
	for _, pl := range p.Pipelines {
		if pl == nil || pl.ID != pid {
			continue
		}
		for _, j := range pl.Jobs {
			if j != nil && j.ID == jid {
				out = append(out, j)
			}
		}
	}
	return out
}
