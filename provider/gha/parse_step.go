package gha

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// ExtDerivedID marks a step whose ID the parser synthesised, so an emitter does not write it back.
const ExtDerivedID = "_derived_id"

const dockerPrefix = "docker://"

func (fp *fileParser) jobSteps(j *ir.Job, m *mapping) error {
	uses := m.take("uses")
	if uses != nil {
		call, err := fp.reusableCall(j, m, uses)
		if err != nil {
			return err
		}
		if m.take("steps") != nil {
			return fp.errf(uses, "job %s has both uses and steps", j.ID)
		}
		j.Steps = []*ir.Step{call}
		return nil
	}
	n := m.take("steps")
	if n == nil {
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		return fp.errf(n, "steps must be a list")
	}
	for i, c := range n.Content {
		s, err := fp.parseStep(j.ID, i+1, c)
		if err != nil {
			return err
		}
		j.Steps = append(j.Steps, s)
	}
	return nil
}

// reusableCall models a job-level uses: as one PipelineCall step; its with and secrets ride on that step.
func (fp *fileParser) reusableCall(j *ir.Job, m *mapping, uses *yaml.Node) (*ir.Step, error) {
	s := &ir.Step{ID: derivedID(j.ID, 1), Kind: ir.StepPipelineCall, Name: j.Name}
	ref, err := fp.action(uses)
	if err != nil {
		return nil, err
	}
	s.Action = &ref
	if w := m.take("with"); w != nil {
		if s.With, err = fp.with(w); err != nil {
			return nil, err
		}
	}
	if sec := m.take("secrets"); sec != nil {
		v, err := fp.plain(sec)
		if err != nil {
			return nil, err
		}
		s.Ext = markDerived(fp.dialect, s.Ext)
		s.Ext[fp.dialect].(map[string]any)["secrets"] = v
	} else {
		s.Ext = markDerived(fp.dialect, s.Ext)
	}
	j.Name = ""
	return s, nil
}

func derivedID(job string, n int) string { return fmt.Sprintf("%s-step-%d", job, n) }

func markDerived(id string, e ir.Ext) ir.Ext {
	if e == nil {
		e = ir.Ext{}
	}
	inner, _ := e[id].(map[string]any)
	if inner == nil {
		inner = map[string]any{}
		e[id] = inner
	}
	inner[ExtDerivedID] = true
	return e
}

func (fp *fileParser) parseStep(job string, n int, node *yaml.Node) (*ir.Step, error) {
	m, err := fp.mapping(node, fmt.Sprintf("step %d of job %s", n, job))
	if err != nil {
		return nil, err
	}
	s := &ir.Step{}
	if v := m.take("id"); v != nil {
		if s.ID, err = fp.scalar(v, "id"); err != nil {
			return nil, err
		}
	}
	derived := s.ID == ""
	if derived {
		s.ID = derivedID(job, n)
	}
	for _, f := range []func(*ir.Step, *mapping) error{fp.stepHead, fp.stepRun, fp.stepFields} {
		if err := f(s, m); err != nil {
			return nil, err
		}
	}
	if s.Kind == "" {
		return nil, fp.errf(node, "step %d of job %s needs run or uses", n, job)
	}
	foldRefEnv(s)
	if s.Ext, err = m.ext(fp.d.Extra[NodeStep]); err != nil {
		return nil, err
	}
	if derived {
		s.Ext = markDerived(fp.dialect, s.Ext)
	}
	return s, nil
}

func (fp *fileParser) stepHead(s *ir.Step, m *mapping) (err error) {
	if v := m.take("name"); v != nil {
		if s.Name, err = fp.template(v, "name"); err != nil {
			return err
		}
	}
	if v := m.take("if"); v != nil {
		s.If, err = fp.cond(v)
	}
	return err
}

// stepRun reads run or uses; a step with both is an error because no IR step kind holds both.
func (fp *fileParser) stepRun(s *ir.Step, m *mapping) error {
	run, uses := m.take("run"), m.take("uses")
	if run != nil && uses != nil {
		return fp.errf(run, "step has both run and uses")
	}
	if run != nil {
		text, err := fp.scalar(run, "run")
		if err != nil {
			return err
		}
		s.Kind = ir.StepScript
		s.Script, err = fp.script(run, text)
		return err
	}
	if uses != nil {
		ref, err := fp.action(uses)
		if err != nil {
			return err
		}
		s.Kind, s.Action = ir.StepAction, &ref
	}
	return nil
}

// script canonicalises a run: value and splits it into lines; the final newline of a block scalar is dropped.
func (fp *fileParser) script(n *yaml.Node, text string) ([]string, error) {
	parts, err := fp.parts(n, text)
	if err != nil {
		return nil, err
	}
	canon, err := expr.PrintScriptTemplate(parts)
	if err != nil {
		return nil, fp.errf(n, "%v", err)
	}
	return strings.Split(strings.TrimSuffix(canon, "\n"), "\n"), nil
}

func (fp *fileParser) stepFields(s *ir.Step, m *mapping) (err error) {
	if v := m.take("shell"); v != nil {
		if s.Shell, err = fp.scalar(v, "shell"); err != nil {
			return err
		}
	}
	if v := m.take("working-directory"); v != nil {
		if s.WorkDir, err = fp.template(v, "working-directory"); err != nil {
			return err
		}
	}
	if v := m.take("with"); v != nil {
		if s.With, err = fp.with(v); err != nil {
			return err
		}
	}
	if err = fp.takeVars(m, "env", &s.Env); err != nil {
		return err
	}
	if v := m.take("continue-on-error"); v != nil {
		b, ok := boolNode(v)
		if !ok {
			m.release("continue-on-error")
		}
		s.ContinueOnError = b
	}
	s.Timeout, err = fp.minutes(m, "timeout-minutes")
	return err
}

// action parses a uses: reference: owner/repo[/path]@ref, docker://image, ./local/path, and for Gitea and
// Forgejo an absolute URL; Gitea adds self:owner/repo@ref and builtin:name.
func (fp *fileParser) action(n *yaml.Node) (ir.ActionRef, error) {
	s, err := fp.scalar(n, "uses")
	if err != nil {
		return ir.ActionRef{}, err
	}
	ref, ok := parseAction(fp.d, s)
	if !ok {
		return ir.ActionRef{}, fp.errf(n, "unsupported uses %q: want owner/repo@ref, docker://image or ./path", s)
	}
	return ref, nil
}

func parseAction(d Dialect, s string) (ir.ActionRef, bool) {
	switch {
	case strings.HasPrefix(s, dockerPrefix):
		img := strings.TrimPrefix(s, dockerPrefix)
		return ir.ActionRef{Kind: ir.ActionDocker, Image: img}, img != "" && !strings.ContainsAny(img, " \t\n")
	case strings.HasPrefix(s, "./"):
		return ir.ActionRef{Kind: ir.ActionLocal, Path: s}, !strings.ContainsAny(s, " \t\n")
	case strings.ContainsAny(s, " \t\n"):
		return ir.ActionRef{}, false
	}
	host := d.DefaultHost
	switch {
	case d.ID == provider.Gitea && strings.HasPrefix(s, ir.HostBuiltin):
		name := strings.TrimPrefix(s, ir.HostBuiltin)
		return ir.ActionRef{Kind: ir.ActionRepo, Host: ir.HostBuiltin, Repo: name}, name != "" && !strings.ContainsAny(name, "/@")
	case d.ID == provider.Gitea && strings.HasPrefix(s, ir.HostSelf):
		host, s = ir.HostSelf, strings.TrimPrefix(s, ir.HostSelf)
	case strings.Contains(s, "://"):
		if d.ID == provider.GHA {
			return ir.ActionRef{}, false
		}
		var ok bool
		if host, s, ok = splitURLHost(s); !ok {
			return ir.ActionRef{}, false
		}
	}
	ref, ok := parseRepoRef(s)
	ref.Host = host
	return ref, ok
}

// splitURLHost splits https://h/owner/repo@ref into the base URL and owner/repo@ref.
func splitURLHost(s string) (host, rest string, ok bool) {
	scheme, after, _ := strings.Cut(s, "://")
	if scheme != "https" && scheme != "http" {
		return "", "", false
	}
	h, rest, found := strings.Cut(after, "/")
	return scheme + "://" + h, rest, found && h != ""
}

func parseRepoRef(s string) (ir.ActionRef, bool) {
	at := strings.Index(s, "@")
	if at < 0 {
		return ir.ActionRef{}, false
	}
	segs := strings.Split(s[:at], "/")
	ref := s[at+1:]
	if len(segs) < 2 || ref == "" {
		return ir.ActionRef{}, false
	}
	for _, seg := range segs {
		if seg == "" || strings.Contains(seg, ":") {
			return ir.ActionRef{}, false
		}
	}
	return ir.ActionRef{
		Kind: ir.ActionRepo, Owner: segs[0], Repo: segs[1],
		Path: strings.Join(segs[2:], "/"), Ref: ref,
	}, true
}
