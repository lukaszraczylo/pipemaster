package argo

import (
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// withoutSharedVolumes returns the job without the artifacts and artifact needs that the parser made for the
// volumes of an Argo file, and without the step that creates their directories. The volume carries those files.
func (e *emitter) withoutSharedVolumes(j *ir.Job) *ir.Job {
	if !e.own || j == nil {
		return j
	}
	rec := emitkit.SharedRecord(j, dialect, ExtWorkspaceArtifacts)
	synthetic := func(st *ir.Step) bool { return st != nil && extOf(st.Ext)[ExtWorkspaceDirs] == true }
	if len(rec) == 0 && !slices.ContainsFunc(j.Steps, synthetic) {
		return j
	}
	out := *j
	out.Steps = slices.DeleteFunc(slices.Clone(j.Steps), synthetic)
	out.Artifacts = slices.DeleteFunc(slices.Clone(j.Artifacts), func(a ir.Artifact) bool { return slices.Contains(rec["artifacts"], a.Name) })
	if len(out.Artifacts) == 0 {
		out.Artifacts = nil
	}
	out.Needs = slices.DeleteFunc(slices.Clone(j.Needs), func(n ir.Need) bool { return slices.Contains(rec["added"], n.JobID) })
	for i := range out.Needs {
		if slices.Contains(rec["needs"], out.Needs[i].JobID) {
			out.Needs[i].Artifacts = false
		}
	}
	return &out
}

// mountPathKeys are the template keys whose text can hold a mount path.
var mountPathKeys = map[string]bool{"container": true, "script": true, "containerSet": true}

// restoreMountPaths writes the mount path of each volume back in place of its directory in the texts of the
// containers. dirs maps a directory to its mount path.
func restoreMountPaths(tm *yaml.Node, dirs any) {
	m, ok := dirs.(map[string]any)
	if !ok || len(m) == 0 {
		return
	}
	order := make([]string, 0, len(m))
	for d := range m {
		order = append(order, d)
	}
	slices.SortFunc(order, func(a, b string) int { return len(b) - len(a) })
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode {
			for _, d := range order {
				if p, ok := m[d].(string); ok {
					n.Value = strings.ReplaceAll(n.Value, d, p)
				}
			}
			return
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	for i := 0; i+1 < len(tm.Content); i += 2 {
		if mountPathKeys[tm.Content[i].Value] {
			walk(tm.Content[i+1])
		}
	}
}
