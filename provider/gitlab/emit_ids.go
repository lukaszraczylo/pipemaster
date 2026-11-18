package gitlab

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const renameSuffix = "-job"

// reservedJobKeys cannot be job names: the keywords the GitLab docs list as unavailable, the other global
// keywords and the YAML scalars that read as booleans or null. pages is not reserved, but as a job name it
// starts a Pages deployment (docs: "Using pages as a job name is deprecated"), so only foreign jobs avoid it.
var reservedJobKeys = map[string]bool{
	"image": true, "services": true, "stages": true, "before_script": true, "after_script": true,
	"variables": true, "cache": true, "include": true, "default": true, "workflow": true, "spec": true,
	"true": true, "false": true, "null": true, "yes": true, "no": true, "on": true, "off": true,
}

const pagesJob = "pages"

func unsafeJobKey(id string, foreign bool) bool {
	return reservedJobKeys[strings.ToLower(id)] || strings.HasPrefix(id, ".") || (foreign && id == pagesJob)
}

// jobKeys maps each job id to the key it is written under. An unsafe id gets a suffix, repeated until the
// key is free, so the result does not depend on map order.
func jobKeys(pl *ir.Pipeline, foreign bool) map[string]string {
	taken := map[string]bool{}
	for _, j := range allJobs(pl) {
		taken[j.ID] = true
	}
	keys := map[string]string{}
	for _, j := range allJobs(pl) {
		if !unsafeJobKey(j.ID, foreign) {
			continue
		}
		k := strings.TrimLeft(j.ID, ".") + renameSuffix
		for taken[k] || unsafeJobKey(k, foreign) {
			k += renameSuffix
		}
		taken[k], keys[j.ID] = true, k
	}
	return keys
}

// jobIDMarkers reads the "# pipemaster:jobid <id>" comments: each names the original id of the job whose key follows.
func jobIDMarkers(src []byte) map[string]string {
	out := map[string]string{}
	orig := ""
	for _, raw := range strings.Split(string(src), "\n") {
		line := strings.TrimSpace(raw)
		if rest, ok := strings.CutPrefix(line, jobIDPrefix); ok {
			orig = rest
			continue
		}
		if orig == "" || strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if key, _, ok := strings.Cut(line, ":"); ok && !strings.HasPrefix(raw, " ") {
			out[strings.Trim(key, `"'`)] = orig
		}
		orig = ""
	}
	return out
}

// restoreJobIDs renames the jobs a marker names back to their original ids, in needs and dependencies too.
func restoreJobIDs(pl *ir.Pipeline, ids map[string]string) {
	if len(ids) == 0 {
		return
	}
	rename := func(id string) string {
		if o, ok := ids[id]; ok {
			return o
		}
		return id
	}
	for _, j := range allJobs(pl) {
		j.ID = rename(j.ID)
		for i := range j.Needs {
			j.Needs[i].JobID = rename(j.Needs[i].JobID)
		}
		if deps, ok := extOf(j.Ext)[extDeps].([]any); ok {
			for i, d := range deps {
				if s, ok := d.(string); ok {
					deps[i] = rename(s)
				}
			}
		}
	}
}

// EmittedIDs lists what Emit renames in a single-pipeline project: the pipeline, which Parse always calls
// gitlab-ci. Jobs under a reserved key are not listed: the "# pipemaster:jobid" marker in the file restores
// them, with or without metadata, so metadata must not apply the rename a second time. Several pipelines
// fold into one and carry markers instead.
func (adapter) EmittedIDs(p *ir.Project) map[string]string {
	if p == nil {
		return nil
	}
	var pls []*ir.Pipeline
	for _, pl := range p.Pipelines {
		if pl != nil {
			pls = append(pls, pl)
		}
	}
	if len(pls) != 1 {
		return nil
	}
	pl := pls[0]
	out := map[string]string{}
	if pl.ID != pipelineID {
		out["pipeline/"+pipelineID] = pl.ID
	}
	return out
}
