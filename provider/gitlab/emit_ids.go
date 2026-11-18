package gitlab

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const renameSuffix = "-job"

// reservedJobKeys cannot be job names: the keywords the GitLab docs list as unavailable, the other global
// keywords, pages (a Pages deployment) and the YAML scalars that read as booleans or null.
var reservedJobKeys = map[string]bool{
	"image": true, "services": true, "stages": true, "before_script": true, "after_script": true,
	"variables": true, "cache": true, "include": true, "default": true, "workflow": true, "pages": true, "spec": true,
	"true": true, "false": true, "null": true, "yes": true, "no": true, "on": true, "off": true,
}

func unsafeJobKey(id string) bool {
	return reservedJobKeys[strings.ToLower(id)] || strings.HasPrefix(id, ".")
}

// jobKeys maps each job id to the key it is written under. An unsafe id gets a suffix, repeated until the
// key is free, so the result does not depend on map order.
func jobKeys(pl *ir.Pipeline) map[string]string {
	taken := map[string]bool{}
	for _, j := range allJobs(pl) {
		taken[j.ID] = true
	}
	keys := map[string]string{}
	for _, j := range allJobs(pl) {
		if !unsafeJobKey(j.ID) {
			continue
		}
		k := strings.TrimLeft(j.ID, ".") + renameSuffix
		for taken[k] || unsafeJobKey(k) {
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
