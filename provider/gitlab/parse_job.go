package gitlab

import (
	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// Job `when:` values.
const (
	whenOnSuccess = "on_success"
	whenManual    = "manual"
	whenAlways    = "always"
	whenOnFailure = "on_failure"
	whenNever     = "never"
	whenDelayed   = "delayed"

	defaultStage = "test"
	stageKey     = "stage"
)

// jobInfo carries what the needs pass needs beyond the IR job.
type jobInfo struct {
	job      *ir.Job
	name     string
	stage    string
	needs    []needSpec
	deps     []string
	hasNeeds bool
	hasDeps  bool
	finally  bool
}

func (s *state) jobPath(id string) string { return "pipeline/" + pipelineID + "/job/" + id }

func (s *state) note(path string, f caps.Feature, strategy, detail string, n *yaml.Node) {
	s.rep.Add(report.Entry{IRPath: path, Feature: f, Level: caps.Lost, Strategy: strategy, Detail: detail, SourceLoc: s.loc(n)})
}

// parseJob reads one resolved job. raw is the job as written, which holds the extends list.
func (s *state) parseJob(name string, merged, raw *yaml.Node, def []entry) (*jobInfo, error) {
	node, err := s.applyDefaults(merged, def)
	if err != nil {
		return nil, err
	}
	r, err := s.reader(node, "job "+name)
	if err != nil {
		return nil, err
	}
	j := &ir.Job{ID: name}
	info := &jobInfo{job: j, name: name, stage: defaultStage}
	if sn := r.peek(stageKey); sn != nil {
		if info.stage, err = s.scalar(sn, "stage"); err != nil {
			return nil, err
		}
	}
	steps := []func(*ir.Job, *reader) error{
		s.jobScripts, s.jobImage, s.jobServices, s.jobTags, s.jobLimits, s.jobEnv,
		s.jobEnvironment, s.jobCache, s.jobArtifacts, s.jobParallel,
	}
	for _, f := range steps {
		if err := f(j, r); err != nil {
			return nil, err
		}
	}
	if err := s.jobWhenRules(info, r); err != nil {
		return nil, err
	}
	if err := s.jobNeeds(info, r); err != nil {
		return nil, err
	}
	if names, err := s.extendsNames(raw); err != nil {
		return nil, err
	} else if len(names) > 0 {
		list := make([]any, len(names))
		for i, n := range names {
			list[i] = n
		}
		r.keep(extExtends, list)
	}
	s.noteExtKeys(j, r)
	j.Ext, err = r.ext()
	return info, err
}

// structuralKeys stay in Ext because the emitter restores them, not because the IR lost them.
var structuralKeys = map[string]bool{stageKey: true, "inherit": true, "dependencies": true, extendsKey: true}

// extNotes explains the keys the IR has no field for; any other leftover key gets a generic note.
var extNotes = map[string]struct {
	feature caps.Feature
	detail  string
}{
	"trigger":        {caps.ExtPassthrough, "trigger jobs start child or multi-project pipelines"},
	"parallel":       {caps.JobMatrix, "parallel: N has no IR field; the job count is kept in Ext"},
	"resource_group": {caps.PipelineConcurrency, "IR has no per-job concurrency; resource_group is kept in Ext"},
	"interruptible":  {caps.PipelineConcurrency, "interruptible cancels redundant pipelines, which is not cancel-in-progress; kept in Ext"},
	"only":           {caps.JobIf, "only is kept in Ext and not evaluated"},
	"except":         {caps.JobIf, "except is kept in Ext and not evaluated"},
	"coverage":       {caps.ExtPassthrough, "coverage regex is kept in Ext"},
	"pages":          {caps.ExtPassthrough, "pages deployment is kept in Ext"},
}

func (s *state) noteExtKeys(j *ir.Job, r *reader) {
	for _, e := range r.ents {
		if r.used[e.name] || structuralKeys[e.name] {
			continue
		}
		f, detail := caps.ExtPassthrough, "key "+e.name+" has no IR field; kept in Ext"
		if n, ok := extNotes[e.name]; ok {
			f, detail = n.feature, n.detail
		}
		s.note(s.jobPath(j.ID), f, "kept in Ext[gitlab]", detail, e.key)
	}
}

func (s *state) jobScripts(j *ir.Job, r *reader) error {
	for _, sec := range []string{"before_script", "script", "after_script"} {
		n := r.take(sec)
		if n == nil {
			continue
		}
		lines, err := s.scriptLines(n, sec)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			continue
		}
		st := &ir.Step{
			ID: sec, Kind: ir.StepScript, Script: lines,
			Ext: ir.Ext{dialect: map[string]any{ExtSection: sec}},
		}
		if sec == "after_script" {
			st.If = expr.Call{Name: "always"}
		}
		j.Steps = append(j.Steps, st)
	}
	return nil
}

func (s *state) jobEnv(j *ir.Job, r *reader) error {
	n := r.take("variables")
	if n == nil {
		return nil
	}
	vars, extras, err := s.variables(n)
	if err != nil {
		return err
	}
	j.Env = vars
	if extras != nil {
		r.keep(extVariables, extras)
	}
	return nil
}

func (s *state) jobTags(j *ir.Job, r *reader) error {
	n := r.take("tags")
	if n == nil {
		return nil
	}
	l, ok := stringList(n)
	if !ok {
		r.release("tags")
		return nil
	}
	for _, t := range l {
		c, err := canonText(t)
		if err != nil {
			return s.errf(n, "%v", err)
		}
		j.Runner.Labels = append(j.Runner.Labels, c)
	}
	return nil
}

// jobLimits maps timeout, retry and allow_failure.
func (s *state) jobLimits(j *ir.Job, r *reader) error {
	if n := r.take("timeout"); n != nil {
		t, _ := scalarText(n)
		d, ok := parseDuration(t)
		if !ok {
			r.release("timeout")
			s.note(s.jobPath(j.ID), caps.JobTimeout, "kept in Ext[gitlab]", "timeout "+t+" is not a duration the parser reads", n)
		} else {
			j.Timeout = &d
		}
	}
	if err := s.jobRetry(j, r); err != nil {
		return err
	}
	if n := r.take("allow_failure"); n != nil {
		b, ok := boolNode(n)
		if !ok {
			r.release("allow_failure")
			s.note(s.jobPath(j.ID), caps.JobAllowFailure, "kept in Ext[gitlab]", "allow_failure:exit_codes has no IR field", n)
		}
		j.AllowFailure = b
	}
	return nil
}

func (s *state) jobRetry(j *ir.Job, r *reader) error {
	n := r.take("retry")
	if n == nil {
		return nil
	}
	if retries, ok := intNode(n); ok {
		if retries > 0 {
			j.Retry = &ir.Retry{Attempts: retries + 1}
		}
		return nil
	}
	if n.Kind != yaml.MappingNode {
		r.release("retry")
		return nil
	}
	rr, err := s.reader(n, "retry")
	if err != nil {
		return err
	}
	retry := &ir.Retry{Attempts: 1}
	if m := rr.take("max"); m != nil {
		v, ok := intNode(m)
		if !ok {
			return s.errf(m, "retry:max must be a number")
		}
		retry.Attempts = v + 1
	}
	if w := rr.take("when"); w != nil {
		l, ok := stringList(w)
		if !ok {
			return s.errf(w, "retry:when must be a string or a list of strings")
		}
		retry.On = l
	}
	j.Retry = retry
	x, err := rr.ext()
	if x != nil {
		r.keep("retry", x[dialect])
	}
	return err
}
