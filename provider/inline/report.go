package inline

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

func (f *flattener) report(j *ir.Job, path, kind string, child *ir.Pipeline, added, finally []*ir.Job, depend, fwd bool, args *callInputs, gate *ir.Job) {
	ids := make([]string, 0, len(added)+len(finally))
	for _, a := range append(slices.Clone(added), finally...) {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	files := child.ID
	if j.Child != nil && len(j.Child.Files) > 0 {
		files = strings.Join(j.Child.Files, ", ")
	}
	detail := fmt.Sprintf("the child pipeline %s runs inline as jobs %s", files, strings.Join(ids, ", "))
	if len(ids) == 0 {
		detail = fmt.Sprintf("the child pipeline %s has no jobs", files)
	}
	var caveats []string
	if gate != nil {
		caveats = append(caveats, "the job "+gate.ID+" fails when the workflow rules of the child pipeline do not create it, as GitLab fails the trigger job, and every job that needs the call waits for it")
	}
	if !depend {
		caveats = append(caveats, "without strategy: depend the trigger job passes when the child pipeline starts, so jobs that need it wait for its own needs only; a failed child job now fails the pipeline")
	}
	if kind == kindGitLab && fwd && len(j.Env) > 0 {
		caveats = append(caveats, "the trigger variables are variables of each child job")
	}
	if len(f.pl.Env) > 0 {
		if kind == kindGitLab && fwd {
			caveats = append(caveats, "the variables of the calling pipeline reach the child jobs, as the trigger forwards them")
		} else {
			caveats = append(caveats, "the variables of the calling pipeline are set empty in the child jobs, as the child does not receive them")
		}
	}
	if kind == kindGitLab && !forwardsPipelineVars(j) {
		caveats = append(caveats, "a variable that a run sets by hand reads as unset in the child conditions, as GitLab does not forward it without forward:pipeline_variables, but its value reaches the scripts of the child jobs, because they run in the same pipeline")
	}
	if j.Matrix != nil {
		caveats = append(caveats, "the matrix of the call applies to every child job, so a job that needs a matrix job waits for all its instances")
	}
	if len(child.Finally) > 0 {
		caveats = append(caveats, "the finally jobs of the child run after every job of the calling pipeline")
	}
	for _, c := range caveats {
		detail += "; " + c
	}
	detail += "; a return to " + callSource(kind) + " restores the call and the child file only with the round-trip metadata"
	f.res.Entries = append(f.res.Entries, report.Entry{
		IRPath: path, Feature: callFeature(kind), Level: caps.Emulated, Strategy: EntryStrategy, Detail: detail,
	})
	f.res.Entries = append(f.res.Entries, f.dropped(j, path, kind, child, args)...)
}

// dropped gives each key of the call or of the child that no inlined job carries its own lost verdict.
func (f *flattener) dropped(j *ir.Job, path, kind string, child *ir.Pipeline, args *callInputs) []report.Entry {
	var out []report.Entry
	lost := func(feature caps.Feature, detail string) {
		out = append(out, report.Entry{IRPath: path, Feature: feature, Level: caps.Lost, Strategy: EntryStrategy, Detail: detail})
	}
	if child.Concurrency != nil {
		lost(caps.PipelineConcurrency, "the concurrency group of the child pipeline has no place in the calling pipeline")
	}
	if child.Permissions != nil {
		lost(caps.PipelinePermissions, "the permissions of the child pipeline have no place in the calling pipeline")
	}
	if keys := childExtKeys(child); len(keys) > 0 {
		lost(caps.ExtPassthrough, "the child pipeline keys "+strings.Join(keys, ", ")+" are not carried")
	}
	if keys := callExtKeys(j, kind); len(keys) > 0 {
		lost(caps.ExtPassthrough, "the call job keys "+strings.Join(keys, ", ")+" are not carried")
	}
	if j.Timeout != nil {
		lost(caps.JobTimeout, "the timeout of the call job does not bound the child jobs")
	}
	if j.Retry != nil {
		lost(caps.JobRetry, "the retry of the call job does not apply to the child jobs")
	}
	for _, u := range args.unused {
		lost(caps.PipelineInputs, "the input "+u+" is not declared by the called workflow")
	}
	return out
}

// childExtKeys lists the Ext keys of the child pipeline that the rewrite did not fold into a job.
func childExtKeys(child *ir.Pipeline) []string {
	var keys []string
	for dialect, v := range child.Ext {
		m, _ := v.(map[string]any)
		for k := range m {
			if !foldedPipelineKeys[k] {
				keys = append(keys, dialect+"."+k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// foldedPipelineKeys are the pipeline keys that already act through the jobs of the child.
var foldedPipelineKeys = map[string]bool{"workflow": true, "stages": true, "default": true, "hidden": true}

// callExtKeys lists the Ext keys of the call job that no inlined job carries.
func callExtKeys(j *ir.Job, kind string) []string {
	var keys []string
	for dialect, v := range j.Ext {
		m, _ := v.(map[string]any)
		for k := range m {
			switch {
			case strings.HasPrefix(k, "_"), k == extTrigger, k == "secrets", kind == kindGitLab && k == "stage":
			default:
				keys = append(keys, dialect+"."+k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// RemapPaths moves report entries about the child pipeline from to the paths that its jobs take once they run
// inline in the pipeline `to` as jobs <call>-<id>. An entry about the pipeline itself lands on the call job.
func RemapPaths(entries []report.Entry, from, to, call string) []report.Entry {
	out := make([]report.Entry, len(entries))
	copy(out, entries)
	root := "pipeline/" + from
	for i, e := range out {
		rest, ok := strings.CutPrefix(e.IRPath, root)
		if !ok || (rest != "" && rest[0] != '/') {
			continue
		}
		if job, ok := strings.CutPrefix(rest, "/job/"); ok {
			out[i].IRPath = jobPath(to, call+idJoin+job)
			continue
		}
		out[i].IRPath = jobPath(to, call) + rest
	}
	return out
}

func callSource(kind string) string {
	if kind == kindGitLab {
		return "GitLab"
	}
	return "GitHub Actions"
}
