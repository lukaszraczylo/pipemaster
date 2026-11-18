package gitlab

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	dotenvFile   = "outputs.env"
	maxRetries   = 2
	reportsKey   = "reports"
	dotenvKey    = "dotenv"
	secondsLabel = " seconds"
)

// retryWhen is the failure list of retry:when in the GitLab docs.
var retryWhen = []string{
	"always", "unknown_failure", "script_failure", "api_failure", "stuck_or_timeout_failure",
	"runner_system_failure", "runner_unsupported", "stale_schedule", "job_execution_timeout",
	"archived_failure", "unmet_prerequisites", "scheduler_failure", "data_integrity_failure",
}

func (e *emitter) imageNode(path string, img ir.Image, extra any) (*yaml.Node, error) {
	ref, err := e.field(path, caps.ScriptInterpolation, img.Ref)
	if err != nil {
		return nil, err
	}
	em, _ := extra.(map[string]any)
	if len(img.Entrypoint) == 0 && len(em) == 0 {
		return str(ref), nil
	}
	m := newMap()
	m.set("name", str(ref))
	if len(img.Entrypoint) > 0 {
		m.set("entrypoint", strSeq(img.Entrypoint))
	}
	return m.n, m.addExtra(em, nil)
}

func (e *emitter) servicesNode(path string, svcs []ir.Service, extra any) (*yaml.Node, error) {
	extras, _ := extra.([]any)
	out := seqOf()
	for i, sv := range svcs {
		sp := path
		ref, err := e.field(sp, caps.ScriptInterpolation, sv.Image.Ref)
		if err != nil {
			return nil, err
		}
		if len(sv.Ports) > 0 {
			e.lose(sp, caps.JobServices, "service ports have no GitLab keyword; services are reachable by alias")
		}
		var rest map[string]any
		if i < len(extras) {
			rest, _ = extras[i].(map[string]any)
		}
		alias := sv.Name != DerivedServiceName(sv.Image.Ref)
		if !alias && len(sv.Image.Entrypoint) == 0 && len(sv.Env) == 0 && len(rest) == 0 {
			out.Content = append(out.Content, str(ref))
			continue
		}
		m := newMap()
		m.set("name", str(ref))
		if alias {
			t, err := e.field(sp, caps.ScriptInterpolation, sv.Name)
			if err != nil {
				return nil, err
			}
			m.set("alias", str(t))
		}
		if len(sv.Image.Entrypoint) > 0 {
			m.set("entrypoint", strSeq(sv.Image.Entrypoint))
		}
		if len(sv.Env) > 0 {
			v, err := e.vars(sp, caps.JobServices, sv.Env, rest[extVariables])
			if err != nil {
				return nil, err
			}
			m.set("variables", v)
		}
		if err := m.addExtra(rest, map[string]bool{extVariables: len(sv.Env) > 0}); err != nil {
			return nil, err
		}
		out.Content = append(out.Content, m.n)
	}
	return out, nil
}

func (e *emitter) environmentNode(path string, env *ir.Environment, extra any) (*yaml.Node, error) {
	name, err := e.field(path, caps.ScriptInterpolation, env.Name)
	if err != nil {
		return nil, err
	}
	em, _ := extra.(map[string]any)
	if env.URL == "" && len(em) == 0 {
		return str(name), nil
	}
	m := newMap()
	m.set("name", str(name))
	if env.URL != "" {
		u, err := e.field(path, caps.ScriptInterpolation, env.URL)
		if err != nil {
			return nil, err
		}
		m.set("url", str(u))
	}
	return m.n, m.addExtra(em, nil)
}

func (e *emitter) tags(path string, r ir.Runner) (*yaml.Node, error) {
	if r.Platform != "" || len(r.Selector) > 0 {
		e.lose(path, caps.JobRunner, "runner platform and selector have no GitLab form; only labels become tags")
	}
	if len(r.Labels) == 0 {
		return nil, nil
	}
	out := seqOf()
	for _, l := range r.Labels {
		t, err := e.field(path, caps.ScriptInterpolation, l)
		if err != nil {
			return nil, err
		}
		out.Content = append(out.Content, str(t))
	}
	return out, nil
}

// retryNode writes retry: N, or the max/when form. Attempts counts runs, so max is one fewer.
func (e *emitter) retryNode(path string, r *ir.Retry, extra any) (*yaml.Node, error) {
	retries := max(r.Attempts-1, 0)
	if retries > maxRetries {
		e.lose(path, caps.JobRetry, fmt.Sprintf("GitLab allows at most %d retries, so %d attempts become %d", maxRetries, r.Attempts, maxRetries+1))
		retries = maxRetries
	}
	var on []string
	for _, w := range r.On {
		if slices.Contains(retryWhen, w) {
			on = append(on, w)
			continue
		}
		e.lose(path, caps.JobRetry, "retry condition "+w+" has no GitLab failure type")
	}
	em, _ := extra.(map[string]any)
	if len(on) == 0 && len(em) == 0 && retries > 0 {
		return intNodeOf(retries), nil
	}
	m := newMap()
	m.set("max", intNodeOf(retries))
	if len(on) > 0 {
		m.set("when", strSeq(on))
	}
	return m.n, m.addExtra(em, nil)
}

// durationText spells d in units GitLab's duration parser reads and the parser here reads back.
func durationText(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + " hours"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + " minutes"
	}
	return strconv.FormatInt(int64(d/time.Second), 10) + secondsLabel
}

func (e *emitter) timeoutNode(path string, d ir.Duration) *yaml.Node {
	if time.Duration(d)%time.Second != 0 {
		e.lose(path, caps.JobTimeout, "GitLab timeouts have whole-second precision")
	}
	return str(durationText(time.Duration(d)))
}

// matrixArg is the part of a matrix the explicit expansion can restore.
type matrixArg struct {
	Axes    []ir.Axis
	Include []ir.Combo
	Exclude []ir.Combo
	Lines   []string `json:"lines,omitempty"`
}

// parallelNode writes parallel:matrix. Include and exclude have no GitLab form, so such a matrix is expanded
// into one entry per combination, with the original in a marker argument so Parse can collapse it.
func (e *emitter) parallelNode(path string, m *ir.Matrix, lines []string, extra any) (node *yaml.Node, marks []string, err error) {
	if m.FailFast != nil || m.MaxParallel > 0 {
		e.lose(path, caps.JobMatrix, "fail-fast and max-parallel have no GitLab equivalent")
	}
	entries := seqOf()
	vals := func(in []string) ([]string, error) {
		out := make([]string, len(in))
		for i, v := range in {
			t, err := e.field(path, caps.JobMatrix, v)
			if err != nil {
				return nil, err
			}
			out[i] = t
		}
		return out, nil
	}
	combos := func(cs []ir.Combo) error {
		for _, c := range cs {
			en := newMap()
			for _, kv := range c {
				v, err := vals([]string{kv.Value})
				if err != nil {
					return err
				}
				en.set(kv.Key, strSeq(v))
			}
			entries.Content = append(entries.Content, en.n)
		}
		return nil
	}
	switch {
	case len(m.Include) == 0 && len(m.Exclude) == 0:
		en := newMap()
		for _, a := range m.Axes {
			v, err := vals(a.Values)
			if err != nil {
				return nil, nil, err
			}
			en.set(a.Name, strSeq(v))
		}
		entries.Content = append(entries.Content, en.n)
	case len(m.Axes) == 0 && len(m.Exclude) == 0:
		err = combos(m.Include)
	default:
		err = combos(ir.Expand(m))
		if err != nil {
			break
		}
		var arg string
		if arg, err = markerArg(matrixArg{Axes: m.Axes, Include: m.Include, Exclude: m.Exclude, Lines: lines}); err != nil {
			break
		}
		e.emulate(path, caps.JobMatrix, "expanded into one parallel:matrix entry per combination", "include and exclude have no GitLab form")
		marks = []string{emitkit.Marker(caps.JobMatrix, path), arg}
	}
	if err != nil {
		return nil, nil, err
	}
	if len(entries.Content) == 0 {
		e.lose(path, caps.JobMatrix, "every matrix combination is excluded")
		return nil, nil, nil
	}
	pm := newMap()
	pm.set("matrix", entries)
	if err := pm.mergeExtras(extra); err != nil {
		return nil, nil, err
	}
	return pm.n, marks, nil
}

func (e *emitter) cacheNodes(path string, p *jobPrep, extra any) (*yaml.Node, error) {
	extras, _ := extra.([]any)
	out := seqOf()
	for i, c := range p.j.Cache {
		var rest map[string]any
		if i < len(extras) {
			rest, _ = extras[i].(map[string]any)
			rest = maps.Clone(rest)
		}
		m := newMap()
		if err := e.cacheEntryKey(path, m, c, rest); err != nil {
			return nil, err
		}
		if len(c.Paths) > 0 {
			paths := make([]string, len(c.Paths))
			for k, pa := range c.Paths {
				t, err := e.field(path, caps.ScriptInterpolation, pa)
				if err != nil {
					return nil, err
				}
				paths[k] = t
			}
			m.set("paths", strSeq(paths))
		}
		if c.Policy != "" {
			m.set("policy", str(c.Policy))
		}
		if err := m.addExtra(rest, nil); err != nil {
			return nil, err
		}
		out.Content = append(out.Content, m.n)
	}
	for _, m := range p.caches {
		out.Content = append(out.Content, m.n)
	}
	if len(out.Content) == 0 {
		return nil, nil
	}
	return out, nil
}

// cacheEntryKey restores the raw key mapping when it still produces the IR key, and otherwise writes the IR key.
func (e *emitter) cacheEntryKey(path string, m *omap, c ir.Cache, rest map[string]any) error {
	if raw, ok := rest["key"].(map[string]any); ok {
		n, err := plainNode(raw)
		if err != nil {
			return err
		}
		if derived, err := newState(nil).cacheKeyFiles(n); err == nil && derived == c.Key {
			m.set("key", n)
			delete(rest, "key")
			return nil
		}
	}
	return e.cacheKey(path, m, c.Key)
}

// artifactsNode merges the job's artifacts, the uploads from actions and the dotenv report for outputs into one block.
func (e *emitter) artifactsNode(path string, p *jobPrep, extra any) (*yaml.Node, error) {
	acc := p.art
	for _, a := range p.j.Artifacts {
		name, err := e.field(path, caps.ScriptInterpolation, a.Name)
		if err != nil {
			return nil, err
		}
		if acc.seen && name != acc.name && name != "" {
			e.lose(path, caps.JobArtifacts, "a GitLab job has one artifacts block; artifact "+name+" is merged into "+acc.name)
		}
		if !acc.seen {
			acc.name, acc.expire = name, a.Expire
		}
		acc.seen = true
		for _, pa := range a.Paths {
			t, err := e.field(path, caps.ScriptInterpolation, pa)
			if err != nil {
				return nil, err
			}
			acc.paths = appendNew(acc.paths, []string{t})
		}
	}
	em, _ := extra.(map[string]any)
	dotenv := e.hasDotenv(p.j)
	if !acc.seen && len(em) == 0 && !dotenv {
		return nil, nil
	}
	m := newMap()
	if acc.name != "" {
		m.set("name", str(acc.name))
	}
	if acc.expire != "" {
		m.set("expire_in", str(acc.expire))
	}
	if len(acc.paths) > 0 {
		m.set("paths", strSeq(acc.paths))
	}
	if len(acc.exclude) > 0 {
		m.set("exclude", strSeq(acc.exclude))
	}
	if acc.when != "" {
		m.set("when", str(acc.when))
	}
	return m.n, e.artifactExtras(m, em, dotenv)
}

func (e *emitter) artifactExtras(m *omap, em map[string]any, dotenv bool) error {
	if dotenv {
		rep := newMap()
		if old, ok := em[reportsKey].(map[string]any); ok {
			if err := rep.addExtra(old, nil); err != nil {
				return err
			}
		}
		rep.set(dotenvKey, str(dotenvFile))
		m.set(reportsKey, rep.n)
	}
	return m.addExtra(em, nil)
}

// outputLines is the dotenv writer for job outputs, appended to the script.
func (e *emitter) outputLines(p *jobPrep) ([]string, error) {
	var out []string
	for _, o := range p.j.Outputs {
		v, ok, why, err := e.outputText(o)
		if err != nil {
			return nil, err
		}
		if !ok {
			e.lose(p.path, caps.JobOutputs, why)
			e.comment("job.outputs " + p.path + ": output " + o.Name + " is not written")
			continue
		}
		out = append(out, "echo "+dq(o.Name+"="+v)+" >> "+dotenvFile)
	}
	return out, nil
}

// hasDotenv is true when at least one output of j can be written to the dotenv file.
func (e *emitter) hasDotenv(j *ir.Job) bool {
	for _, o := range j.Outputs {
		if _, ok, _, err := e.outputText(o); ok && err == nil {
			return true
		}
	}
	return false
}
