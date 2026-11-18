package gitlab

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// Ext keys the parser writes under Ext[gitlab]. The underscore keys are derived, not source keys.
const (
	ExtDerivedNeeds = "_derived_needs"
	ExtSection      = "section"

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
func (adapter) Parse(fsys fs.FS) (*ir.Project, *report.Report, error) {
	rep := &report.Report{Source: dialect}
	s := newState(rep)
	ld := newLoader(fsys, s)
	root, err := ld.load(rootFile, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	if root, err = s.resolveRefs(root); err != nil {
		return nil, nil, err
	}
	pl, infos, err := s.parsePipeline(root, ld.remote)
	if err != nil {
		return nil, nil, err
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
	if err := ir.Validate(proj); err != nil {
		return nil, nil, s.locate(err, infos)
	}
	ir.Normalize(proj)
	return proj, rep, nil
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
	s.takeDefaults(r)
	if len(remote) > 0 {
		r.keep(extInclude, remote)
	}
	stages, err := s.stageList(root)
	if err != nil {
		return nil, nil, err
	}
	if err := s.workflow(pl, r); err != nil {
		return nil, nil, err
	}
	jobs, hidden, err := s.splitJobs(r)
	if err != nil {
		return nil, nil, err
	}
	if len(hidden) > 0 {
		r.keep(extHidden, hidden)
	}
	def, err := s.defaults(root)
	if err != nil {
		return nil, nil, err
	}
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
