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
// the Ext of the jobs. A job that binds a volume downloads the directory from each latest writer that it runs
// after, directly or not. A writer that runs after another writer has restored the files of that writer and
// uploads them again, so it carries the state of both.
//
// A need on a job that the reader already runs after through other jobs adds no order. The DAG order and the
// events stay as they were. The added needs are recorded under "added", and the emitter of the source drops them.
func (s SharedDirs) Link(jobs []*ir.Job, rep *report.Report) {
	byID := map[string]*ir.Job{}
	direct := map[string][]string{}
	for _, j := range jobs {
		byID[j.ID] = j
		for _, n := range j.Needs {
			direct[j.ID] = append(direct[j.ID], n.JobID)
		}
	}
	before := map[string]map[string]bool{}
	for _, j := range jobs {
		before[j.ID] = reach(j.ID, direct)
	}
	shared := map[string][]string{}
	added := map[string][]string{}
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
		for _, j := range jobs {
			if !binders[j.ID] {
				continue
			}
			var cand []string
			for _, o := range jobs {
				if binders[o.ID] && before[j.ID][o.ID] {
					cand = append(cand, o.ID)
				}
			}
			latest := slices.DeleteFunc(slices.Clone(cand), func(x string) bool {
				return slices.ContainsFunc(cand, func(y string) bool { return y != x && before[y][x] })
			})
			for _, w := range latest {
				if s.addNeed(j, w) {
					added[j.ID] = append(added[j.ID], w)
				}
				shared[w] = append(shared[w], s.addArtifact(byID[w], name, dir, rep))
			}
			if len(latest) == 0 {
				continue
			}
			detail := "the " + s.Noun + " reads " + name + " that " + strings.Join(latest, ", ") + " wrote, so it downloads the artifact of " + dir + "; the target copies the files, the source mounts one volume"
			if len(latest) > 1 {
				var order []string
				for _, n := range j.Needs {
					if slices.Contains(latest, n.JobID) {
						order = append(order, n.JobID)
					}
				}
				detail += "; " + strings.Join(order, " and ") + " run in parallel and write the same directory. The target downloads the artifacts in the order of the needs of this " + s.Noun + ", so the files of " + order[len(order)-1] + ", the last of them, win on a path that both write"
			}
			rep.Add(s.entry(j.ID, caps.JobNeedsArtifacts, caps.Emulated, detail))
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
			s.record(j, shared[j.ID], needs[j.ID], added[j.ID])
		}
	}
}

// addNeed makes the need of j on w carry artifacts. It reports whether it added the need, which j did not hold.
func (s SharedDirs) addNeed(j *ir.Job, w string) bool {
	for i := range j.Needs {
		if j.Needs[i].JobID == w {
			j.Needs[i].Artifacts = true
			return false
		}
	}
	j.Needs = append(j.Needs, ir.Need{JobID: w, Artifacts: true})
	return true
}

func (s SharedDirs) entry(job string, f caps.Feature, level caps.Level, detail string) report.Entry {
	return report.Entry{IRPath: "pipeline/" + s.Pipeline + "/job/" + job, Feature: f, Level: level, Strategy: "workspace directory as artifact", Detail: detail, SourceLoc: s.File}
}

// addArtifact adds the directory to the artifacts of the job that writes it and returns the artifact name. The
// name holds the writer ID, because upload-artifact v4 and later keep an artifact immutable and a second upload
// of one name in a run fails (https://github.com/actions/upload-artifact/blob/main/docs/MIGRATION.md: "In v4,
// Artifacts are immutable unless deleted"). Each writer of a chain thus uploads its own name.
func (s SharedDirs) addArtifact(j *ir.Job, name, dir string, rep *report.Report) string {
	art := SharedArtifactPrefix + name + "-" + j.ID
	if slices.ContainsFunc(j.Artifacts, func(a ir.Artifact) bool { return a.Name == art }) {
		return art
	}
	j.Artifacts = append(j.Artifacts, ir.Artifact{Name: art, Paths: []string{dir}})
	rep.Add(s.entry(j.ID, caps.JobArtifacts, caps.Emulated, "a later "+s.Noun+" reads "+name+" after this "+s.Noun+", so the directory "+dir+" is an artifact; the source shares the files through "+s.Medium+", the target through an artifact upload"))
	return art
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
func (s SharedDirs) record(j *ir.Job, arts, needs, implicit []string) {
	if j.Ext == nil {
		j.Ext = ir.Ext{}
	}
	d, _ := j.Ext[s.Dialect].(map[string]any)
	if d == nil {
		d = map[string]any{}
	}
	rec := map[string]any{}
	for k, v := range map[string][]string{"artifacts": arts, "needs": needs, "added": implicit} {
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
	Carry(d, s.Key)
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

// WithoutAddedNeeds returns the job without the needs that Link added, so the file of the source gets no new edge.
func WithoutAddedNeeds(j *ir.Job, dialect, key string) *ir.Job {
	added := SharedRecord(j, dialect, key)["added"]
	if len(added) == 0 {
		return j
	}
	out := *j
	out.Needs = slices.DeleteFunc(slices.Clone(j.Needs), func(n ir.Need) bool { return slices.Contains(added, n.JobID) })
	return &out
}
