package gitlab

import (
	"fmt"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	stagePre  = ".pre"
	stagePost = ".post"
	extNeeds  = "needs"

	// ExtNeedsExtra holds, per needed job, the needs keys the IR has no field for (parallel:matrix).
	ExtNeedsExtra = "needs_extra"
)

type needSpec struct {
	extra      map[string]any
	job        string
	noArtifact bool
	optional   bool
}

// jobNeeds reads needs. Entries for other projects or pipelines stay raw in Ext[gitlab][needs]; keys the IR lacks on
// a same-pipeline entry go to Ext[gitlab][needs_extra] by job name, so nothing the IR holds is duplicated.
func (s *state) jobNeeds(info *jobInfo, r *reader) error {
	if d := r.peek("dependencies"); d != nil {
		l, ok := stringList(d)
		if !ok {
			return s.errf(d, "dependencies must be a list of job names")
		}
		info.deps, info.hasDeps = l, true
	}
	n := r.take("needs")
	if n == nil {
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		return s.errf(n, "needs must be a list")
	}
	info.hasNeeds = true
	var raw []any
	extras := map[string]any{}
	for _, it := range n.Content {
		spec, whole, err := s.needEntry(it)
		if err != nil {
			return err
		}
		if whole {
			v, err := s.plain(it)
			if err != nil {
				return err
			}
			raw = append(raw, v)
			continue
		}
		info.needs = append(info.needs, *spec)
		if spec.extra != nil {
			extras[spec.job] = spec.extra
		}
	}
	if raw != nil {
		r.keep(extNeeds, raw)
		s.note(s.jobPath(info.name), "job.needs", "kept in Ext[gitlab][needs]", "needs entries for other projects or pipelines are not modelled", n)
	}
	if len(extras) > 0 {
		r.keep(ExtNeedsExtra, extras)
		s.note(s.jobPath(info.name), "job.needs", "kept in Ext[gitlab][needs_extra]", "needs keys without an IR field (parallel) are kept per job", n)
	}
	return nil
}

// needEntry returns whole=true for an entry the IR cannot model at all.
func (s *state) needEntry(it *yaml.Node) (spec *needSpec, whole bool, err error) {
	if it.Kind == yaml.ScalarNode {
		return &needSpec{job: it.Value}, false, nil
	}
	r, err := s.reader(it, "needs entry")
	if err != nil {
		return nil, false, err
	}
	for _, k := range []string{"project", "pipeline"} {
		if r.peek(k) != nil {
			return nil, true, nil
		}
	}
	jn := r.take("job")
	if jn == nil {
		return nil, false, s.errf(it, "needs entry needs a job")
	}
	spec = &needSpec{}
	if spec.job, err = s.scalar(jn, "needs job"); err != nil {
		return nil, false, err
	}
	if a := r.take("artifacts"); a != nil {
		if b, ok := boolNode(a); ok {
			spec.noArtifact = !b
		} else {
			r.release("artifacts")
		}
	}
	if o := r.take("optional"); o != nil {
		if b, ok := boolNode(o); ok {
			spec.optional = b
		} else {
			r.release("optional")
		}
	}
	for _, e := range r.ents {
		if r.used[e.name] {
			continue
		}
		v, err := s.plain(e.val)
		if err != nil {
			return nil, false, err
		}
		if spec.extra == nil {
			spec.extra = map[string]any{}
		}
		spec.extra[e.name] = v
	}
	return spec, false, nil
}

// wire places each job in Jobs or Finally and builds its needs: explicit entries as written, otherwise
// every job of every earlier stage, which is how GitLab sequences stages.
func (s *state) wire(pl *ir.Pipeline, infos []*jobInfo, stages []string) error {
	order := append(append([]string{stagePre}, stages...), stagePost)
	idx := map[string]int{}
	for i, st := range order {
		if _, dup := idx[st]; !dup {
			idx[st] = i
		}
	}
	artifacts := map[string]bool{}
	for _, i := range infos {
		if _, ok := idx[i.stage]; !ok {
			p := s.lines[i.name]
			return fmt.Errorf("%s:%d: job %q uses stage %q, which is not in stages", p.file, p.line, i.name, i.stage)
		}
		artifacts[i.name] = declaresArtifacts(i.job)
	}
	for _, i := range infos {
		s.jobEdges(i, infos, idx, artifacts)
		if i.finally {
			pl.Finally = append(pl.Finally, i.job)
		} else {
			pl.Jobs = append(pl.Jobs, i.job)
		}
	}
	return nil
}

func declaresArtifacts(j *ir.Job) bool {
	if len(j.Artifacts) > 0 {
		return true
	}
	x, _ := j.Ext[dialect].(map[string]any)
	a, _ := x[extArtifacts].(map[string]any)
	if u, _ := a["untracked"].(bool); u {
		return true
	}
	// A dotenv report reaches later jobs through needs and dependencies, so it counts as an artifact.
	reports, _ := a["reports"].(map[string]any)
	_, dotenv := reports["dotenv"]
	return dotenv
}

func (s *state) jobEdges(i *jobInfo, all []*jobInfo, idx map[string]int, artifacts map[string]bool) {
	passes := func(id string, wanted bool) bool {
		if !wanted || !artifacts[id] {
			return false
		}
		return !i.hasDeps || containsStr(i.deps, id)
	}
	j := i.job
	if i.hasNeeds {
		for _, n := range i.needs {
			j.Needs = append(j.Needs, ir.Need{JobID: n.job, Artifacts: passes(n.job, !n.noArtifact), Optional: n.optional})
		}
		return
	}
	for _, o := range all {
		if o != i && !o.finally && idx[o.stage] < idx[i.stage] {
			j.Needs = append(j.Needs, ir.Need{JobID: o.name, Artifacts: passes(o.name, true)})
		}
	}
	if len(j.Needs) > 0 {
		setExt(&j.Ext, ExtDerivedNeeds, true)
	}
}

func containsStr(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

// setExt writes one key under Ext[gitlab].
func setExt(e *ir.Ext, key string, v any) {
	if *e == nil {
		*e = ir.Ext{}
	}
	m, _ := (*e)[dialect].(map[string]any)
	if m == nil {
		m = map[string]any{}
		(*e)[dialect] = m
	}
	m[key] = v
}
