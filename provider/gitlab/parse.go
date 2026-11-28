package gitlab

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/vars"
)

// Ext keys the parser writes under Ext[gitlab]. The underscore keys are derived, not source keys.
const (
	ExtDerivedNeeds = "_derived_needs"
	ExtSection      = "section"
	ExtNoScript     = "_no_script"
	ExtRulesAlways  = "_rules_always"
	// ExtRulesFP names the condition the kept rules compiled to; the emitter restores the text only while it still holds.
	ExtRulesFP = "_rules_fp"
	// ExtRulesWhen is the job-level when that the kept rules take as their default.
	ExtRulesWhen = "_rules_when"

	extHidden    = "hidden"
	extInclude   = "include"
	extExtends   = "extends"
	extVariables = "variables"
)

var (
	irPathRE = regexp.MustCompile(`^pipeline/[^/]+/job/(.+)$`)

	// reservedKeys are the top-level keywords that are not jobs.
	reservedKeys = map[string]bool{
		"stages": true, "variables": true, "default": true, "workflow": true, "include": true,
		"image": true, "services": true, "cache": true, "before_script": true, "after_script": true, "spec": true,
	}
)

// Parse reads .gitlab-ci.yml and every local include into one pipeline with ID gitlab-ci.
func (a adapter) Parse(fsys fs.FS) (*ir.Project, *report.Report, error) {
	proj, rep, _, err := a.parseRoot(fsys, rootFile, []string{rootFile})
	return proj, rep, err
}

// parseRoot parses a root file under any name. chain holds the files of the pipelines being read, outermost
// first, so a child that includes an ancestor is not read again.
// It also returns the gate of a child root file: its workflow:rules, when they map to triggers.
func (a adapter) parseRoot(fsys fs.FS, root string, chain []string) (*ir.Project, *report.Report, expr.Expr, error) {
	proj, rep, gate, err := a.parseSource(fsys, root)
	if err != nil {
		return proj, rep, nil, err
	}
	vars.Strip(proj)
	if err := a.attachChildren(fsys, proj, rep, chain); err != nil {
		return nil, nil, nil, err
	}
	ir.Normalize(proj)
	return proj, rep, gate, ir.Validate(proj)
}

func (adapter) parseSource(fsys fs.FS, rootName string) (*ir.Project, *report.Report, expr.Expr, error) {
	rep := &report.Report{Source: dialect}
	s := newState(rep)
	s.child = rootName == childRoot
	ld := newLoader(fsys, s)
	root, err := ld.load(rootName, nil, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	s.remote = remoteNames(ld.remote)
	s.spec = ld.spec
	if root, err = s.resolveRefs(root); err != nil {
		return nil, nil, nil, err
	}
	pl, infos, err := s.parsePipeline(root, ld.remote)
	if err != nil {
		return nil, nil, nil, err
	}
	restoreJobIDs(pl, ld.ids)
	pls := splitPipelines(pl, ld.markers, ld.args)
	proj := &ir.Project{
		Origin:    ir.Origin{Provider: dialect, Dialect: dialect, Files: ld.files},
		Pipelines: pls,
	}
	uses, args := ld.markers, ld.args
	if len(pls) == 1 {
		uses, args = retargetMarkers(uses, args, pls[0].ID)
	}
	for _, p := range pls {
		s.collapseMarkers(p, marksFor(uses, p, len(pls) > 1), args)
	}
	if len(pls) == 1 {
		s.guardJobs(pls[0])
	}
	for _, id := range s.defaulted {
		proj.Origin.MarkDefaultTriggers(id)
	}
	if err := ir.Validate(proj); err != nil {
		return nil, nil, nil, s.locate(err, infos)
	}
	ir.Normalize(proj)
	return proj, rep, s.gate, nil
}

func (s *state) parsePipeline(root *yaml.Node, remote []any) (*ir.Pipeline, []*jobInfo, error) {
	r, err := s.reader(root, "the CI file")
	if err != nil {
		return nil, nil, err
	}
	pl := &ir.Pipeline{ID: pipelineID}
	if v := r.take("variables"); v != nil {
		vars, extras, err := s.variables("pipeline/"+pipelineID, v)
		if err != nil {
			return nil, nil, err
		}
		pl.Env = vars
		if extras != nil {
			r.keep(extVariables, extras)
		}
	}
	if err := s.specHeader(pl, r); err != nil {
		return nil, nil, err
	}
	s.takeDefaults(r)
	if len(remote) > 0 {
		r.keep(extInclude, remote)
	}
	stages, err := s.stageList(root)
	if err != nil {
		return nil, nil, err
	}
	if s.guard, err = s.workflow(pl, r); err != nil {
		return nil, nil, err
	}
	jobs, hidden, err := s.splitJobs(r)
	if err != nil {
		return nil, nil, err
	}
	if len(hidden) > 0 {
		r.keep(extHidden, hidden)
		r.carried = append(r.carried, extHidden)
	}
	def, err := s.defaults(root)
	if err != nil {
		return nil, nil, err
	}
	global := definedVars(nil, get(root, "variables"))
	s.noteVariableInputs("pipeline/"+pipelineID, get(root, "variables"))
	var infos []*jobInfo
	for _, name := range jobs.order {
		merged, err := s.extend(name, jobs.nodes, nil)
		if err != nil {
			return nil, nil, err
		}
		info, err := s.parseJob(name, merged, jobs.nodes[name], def)
		if err != nil {
			return nil, nil, err
		}
		s.noteUnmappedRefs(s.jobPath(name), merged, definedVars(global, get(merged, "variables")))
		infos = append(infos, info)
	}
	if err := s.wire(pl, infos, stages); err != nil {
		return nil, nil, err
	}
	pl.Ext, err = r.ext()
	return pl, infos, err
}

// jobList holds every non-reserved top-level mapping: extends can name any of them, hidden or not.
type jobList struct {
	nodes jobSet
	order []string
}

func (s *state) splitJobs(r *reader) (jobList, map[string]any, error) {
	jl := jobList{nodes: jobSet{}}
	hidden := map[string]any{}
	for _, e := range r.ents {
		if reservedKeys[e.name] {
			continue
		}
		r.used[e.name] = true
		jl.nodes[e.name] = e.val
		if strings.HasPrefix(e.name, ".") {
			v, err := s.plain(e.val)
			if err != nil {
				return jl, nil, err
			}
			hidden[e.name] = v
			continue
		}
		if e.val.Kind != yaml.MappingNode {
			return jl, nil, s.errf(e.key, "job %q must be a mapping", e.name)
		}
		s.lines[e.name] = position{file: s.org[e.key], line: e.key.Line}
		jl.order = append(jl.order, e.name)
	}
	return jl, hidden, nil
}

// locate prefixes each validation error with the file and line of the job it names.
func (s *state) locate(err error, infos []*jobInfo) error {
	var errs []error
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		errs = j.Unwrap()
	} else {
		errs = []error{err}
	}
	out := make([]error, 0, len(errs))
	for _, e := range errs {
		out = append(out, fmt.Errorf("%s: %w", s.where(e, infos), e))
	}
	return errors.Join(out...)
}

func (s *state) where(err error, infos []*jobInfo) string {
	var ie *ir.Error
	if !errors.As(err, &ie) {
		return dialect
	}
	id := ""
	if m := irPathRE.FindStringSubmatch(ie.Path); m != nil {
		id = m[1]
	} else if rest, ok := strings.CutPrefix(ie.Msg, "needs cycle: "); ok {
		id, _, _ = strings.Cut(rest, " -> ")
	}
	if p, ok := s.lines[id]; ok {
		return fmt.Sprintf("%s:%d", p.file, p.line)
	}
	if len(infos) > 0 {
		return rootFile
	}
	return dialect
}

// sortedKeys is a deterministic key order for map iteration.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// specHeader maps the inputs of a spec header document to pipeline inputs and keeps the header in Ext.
func (s *state) specHeader(pl *ir.Pipeline, r *reader) error {
	if s.spec == nil {
		return nil
	}
	raw, err := s.plain(s.spec.Content[0])
	if err != nil {
		return err
	}
	spec, _ := raw.(map[string]any)[extSpec].(map[string]any)
	if spec == nil {
		return s.errf(s.spec.Content[0], "spec must be a mapping")
	}
	pl.Inputs = specInputList(spec)
	s.reportStructuredDefaults(spec)
	r.keep(extSpec, spec)
	return nil
}

// remoteNames names each remote include as "kind: value", the way the file spells it.
func remoteNames(entries []any) []string {
	var out []string
	for _, e := range entries {
		switch v := e.(type) {
		case string:
			out = append(out, "remote: "+v)
		case map[string]any:
			name := "include"
			for _, k := range remoteKinds {
				if x, ok := v[k]; ok {
					name = fmt.Sprintf("%s: %v", k, x)
					break
				}
			}
			out = append(out, name)
		}
	}
	return out
}
