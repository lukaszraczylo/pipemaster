package emitkit

import (
	"maps"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// SharedArtifactPrefix starts the name of the artifact that carries a shared directory between jobs.
const SharedArtifactPrefix = "ws-"

// SharedDirs describes the directories that the jobs of a source file share through a volume. Link turns each
// directory into artifacts, so a target with one runner per job copies the files.
type SharedDirs struct {
	// Dir maps the name of a shared volume to its directory in the checkout.
	Dir map[string]string
	// Binds maps a job ID to the shared volumes that the job mounts.
	Binds map[string][]string
	// Skip names the volumes whose files do not pass between jobs, such as a read-only Secret.
	Skip func(name string) bool
	// Dialect and Key name the job Ext entry that records the artifacts and needs that Link made.
	Dialect, Key string
	// Noun is the word for a job in the source, and Medium the word for the volume that it shares.
	Noun, Medium string
	// Pipeline is the pipeline ID of the report paths, and File the source location.
	Pipeline, File string
}

// Link adds the artifacts and the artifact needs for the shared directories, reports each, and records them in
// the Ext of the jobs. A job that binds a volume uploads its directory when a later job that binds it runs
// directly after it, and that later job downloads the directory.
func (s SharedDirs) Link(jobs []*ir.Job, rep *report.Report) {
	byID := map[string]*ir.Job{}
	for _, j := range jobs {
		byID[j.ID] = j
	}
	shared := map[string][]string{}
	for _, name := range slices.Sorted(maps.Keys(s.Dir)) {
		if s.Skip != nil && s.Skip(name) {
			continue
		}
		dir := s.Dir[name]
		binders := map[string]bool{}
		for _, j := range jobs {
			if slices.Contains(s.Binds[j.ID], name) {
				binders[j.ID] = true
			}
		}
		linked := map[string][]string{}
		for _, j := range jobs {
			if !binders[j.ID] {
				continue
			}
			for i := range j.Needs {
				n := &j.Needs[i]
				if !binders[n.JobID] {
					continue
				}
				n.Artifacts = true
				linked[j.ID] = append(linked[j.ID], n.JobID)
				if s.addArtifact(byID[n.JobID], name, dir, rep) {
					shared[n.JobID] = append(shared[n.JobID], SharedArtifactPrefix+name)
				}
			}
		}
		s.reportUnlinked(jobs, binders, linked, name, rep)
		for _, id := range slices.Sorted(maps.Keys(linked)) {
			rep.Add(s.entry(id, caps.JobNeedsArtifacts, caps.Emulated, "the "+s.Noun+" reads "+name+" that "+strings.Join(linked[id], ", ")+" wrote, so it downloads the artifact of "+dir+"; the target copies the files, the source mounts one volume"))
		}
	}
	needs := map[string][]string{}
	for _, j := range jobs {
		for _, n := range j.Needs {
			if n.Artifacts && len(shared[n.JobID]) > 0 {
				needs[j.ID] = append(needs[j.ID], n.JobID)
			}
		}
	}
	for _, j := range jobs {
		if len(shared[j.ID]) > 0 || len(needs[j.ID]) > 0 {
			s.record(j, shared[j.ID], needs[j.ID])
		}
	}
}

func (s SharedDirs) entry(job string, f caps.Feature, level caps.Level, detail string) report.Entry {
	return report.Entry{IRPath: "pipeline/" + s.Pipeline + "/job/" + job, Feature: f, Level: level, Strategy: "workspace directory as artifact", Detail: detail, SourceLoc: s.File}
}

// addArtifact adds the directory to the artifacts of the job that writes it. It reports whether it added one.
func (s SharedDirs) addArtifact(j *ir.Job, name, dir string, rep *report.Report) bool {
	art := SharedArtifactPrefix + name
	if slices.ContainsFunc(j.Artifacts, func(a ir.Artifact) bool { return a.Name == art }) {
		return false
	}
	j.Artifacts = append(j.Artifacts, ir.Artifact{Name: art, Paths: []string{dir}})
	rep.Add(s.entry(j.ID, caps.JobArtifacts, caps.Emulated, "a later "+s.Noun+" reads "+name+" after this "+s.Noun+", so the directory "+dir+" is an artifact; the source shares the files through "+s.Medium+", the target through an artifact upload"))
	return true
}

// reportUnlinked names a job that reads a volume after another job with no chain of jobs that mount it between them.
func (s SharedDirs) reportUnlinked(jobs []*ir.Job, binders map[string]bool, linked map[string][]string, name string, rep *report.Report) {
	needs := map[string][]string{}
	for _, j := range jobs {
		for _, n := range j.Needs {
			needs[j.ID] = append(needs[j.ID], n.JobID)
		}
	}
	for _, j := range jobs {
		if !binders[j.ID] {
			continue
		}
		all, chain := reach(j.ID, needs), reach(j.ID, linked)
		for _, prev := range slices.Sorted(maps.Keys(all)) {
			if binders[prev] && !chain[prev] {
				rep.Add(s.entry(j.ID, caps.JobNeedsArtifacts, caps.Lost, "the "+s.Noun+" reads "+name+" that "+s.Noun+" "+prev+" wrote, but a "+s.Noun+" that does not mount it runs between them; the files do not reach this "+s.Noun+" on a target with one runner per job"))
			}
		}
	}
}

// reach lists the jobs that a job reaches along the edges.
func reach(from string, edges map[string][]string) map[string]bool {
	seen := map[string]bool{}
	var walk func(id string)
	walk = func(id string) {
		for _, n := range edges[id] {
			if !seen[n] {
				seen[n] = true
				walk(n)
			}
		}
	}
	walk(from)
	return seen
}

// record keeps the artifacts and needs that Link made in the Ext of the job.
func (s SharedDirs) record(j *ir.Job, arts, needs []string) {
	if j.Ext == nil {
		j.Ext = ir.Ext{}
	}
	d, _ := j.Ext[s.Dialect].(map[string]any)
	if d == nil {
		d = map[string]any{}
	}
	rec := map[string]any{}
	for k, v := range map[string][]string{"artifacts": arts, "needs": needs} {
		if len(v) == 0 {
			continue
		}
		v = slices.Compact(slices.Sorted(slices.Values(v)))
		list := make([]any, len(v))
		for i, e := range v {
			list[i] = e
		}
		rec[k] = list
	}
	d[s.Key] = rec
	j.Ext[s.Dialect] = d
}

// SharedRecord reads the artifact names and need IDs that Link recorded for the job.
func SharedRecord(j *ir.Job, dialect, key string) map[string][]string {
	d, _ := j.Ext[dialect].(map[string]any)
	rec, _ := d[key].(map[string]any)
	out := map[string][]string{}
	for k, v := range rec {
		list, _ := v.([]any)
		for _, e := range list {
			if s, ok := e.(string); ok {
				out[k] = append(out[k], s)
			}
		}
	}
	return out
}
