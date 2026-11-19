package gha

import (
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
)

// Ext keys the job parser writes under the dialect's Ext entry.
const (
	extContainer   = "container"
	extServices    = "services"
	extEnvironment = "environment"
	extRunsOn      = "runs-on"
)

func (fp *fileParser) parseJob(id string, n *yaml.Node, defaults runDefaults) (*ir.Job, error) {
	m, err := fp.mapping(n, "job "+id)
	if err != nil {
		return nil, err
	}
	j := &ir.Job{ID: id}
	for _, f := range []func(*ir.Job, *mapping) error{
		fp.jobHead, fp.jobRunsOn, fp.jobEnv, fp.jobLimits, fp.jobContainer, fp.jobServices,
		fp.jobEnvironment, fp.jobOutputs, fp.jobStrategy, fp.jobSteps,
	} {
		if err := f(j, m); err != nil {
			return nil, err
		}
	}
	jobDefaults, err := fp.takeRunDefaults(m)
	if err != nil {
		return nil, err
	}
	defaults.over(jobDefaults).apply(j.Steps)
	j.Ext, err = m.ext(fp.d.Extra[NodeJob])
	return j, err
}

func (fp *fileParser) jobHead(j *ir.Job, m *mapping) (err error) {
	if v := m.take("name"); v != nil {
		if j.Name, err = fp.template(v, "name"); err != nil {
			return err
		}
	}
	if v := m.take("needs"); v != nil {
		l, ok := stringList(v)
		if !ok {
			m.release("needs")
		}
		for _, id := range l {
			j.Needs = append(j.Needs, ir.Need{JobID: id})
		}
	}
	if v := m.take("if"); v != nil {
		j.If, err = fp.cond(v)
	}
	return err
}

func (fp *fileParser) jobRunsOn(j *ir.Job, m *mapping) error {
	n := m.take("runs-on")
	if n == nil {
		return nil
	}
	if n.Kind == yaml.MappingNode {
		return fp.runsOnMap(j, m, n)
	}
	l, ok := stringList(n)
	if !ok {
		m.release("runs-on")
		return nil
	}
	var err error
	j.Runner.Labels, err = fp.templates(n, l)
	return err
}

func (fp *fileParser) templates(n *yaml.Node, l []string) ([]string, error) {
	out := make([]string, 0, len(l))
	for _, s := range l {
		t, err := fp.templateString(n, s)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// runsOnMap maps the group/labels form; any other key stays in Ext under runs-on.
func (fp *fileParser) runsOnMap(j *ir.Job, jm *mapping, n *yaml.Node) error {
	m, err := fp.mapping(n, "runs-on")
	if err != nil {
		return err
	}
	if g := m.take("group"); g != nil {
		s, err := fp.template(g, "runs-on.group")
		if err != nil {
			return err
		}
		j.Runner.Selector = map[string]string{"group": s}
	}
	if l := m.take("labels"); l != nil {
		list, ok := stringList(l)
		if !ok {
			m.release("labels")
		} else if j.Runner.Labels, err = fp.templates(l, list); err != nil {
			return err
		}
	}
	rest, err := m.rest()
	if rest != nil {
		jm.keep(extRunsOn, rest)
	}
	return err
}

func (fp *fileParser) jobEnv(j *ir.Job, m *mapping) (err error) {
	return fp.takeVars(m, "env", &j.Env)
}

// takeVars consumes an env mapping; a non-mapping (an expression) stays in Ext.
func (fp *fileParser) takeVars(m *mapping, key string, dst *[]ir.Var) error {
	n := m.take(key)
	if n == nil {
		return nil
	}
	vs, ok, err := fp.vars(n, key)
	if err != nil {
		return err
	}
	if !ok {
		m.release(key)
		return nil
	}
	*dst = vs
	return nil
}

// minutes reads timeout-minutes; an expression is left unconsumed.
func (fp *fileParser) minutes(m *mapping, key string) (*ir.Duration, error) {
	n := m.take(key)
	if n == nil {
		return nil, nil
	}
	s, ok := scalarText(n)
	if !ok {
		return nil, fp.errf(n, "%s must be a number", key)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || f > float64(1<<31) {
		m.release(key)
		return nil, nil
	}
	d := ir.Duration(time.Duration(f * float64(time.Minute)))
	return &d, nil
}

func (fp *fileParser) jobLimits(j *ir.Job, m *mapping) (err error) {
	if j.Timeout, err = fp.minutes(m, "timeout-minutes"); err != nil {
		return err
	}
	if n := m.take("continue-on-error"); n != nil {
		b, ok := boolNode(n)
		if !ok {
			m.release("continue-on-error")
		}
		j.AllowFailure = b
	}
	return nil
}

// jobContainer maps container to Job.Container; its other keys (credentials, env, ports,
// volumes, options) stay in the job's Ext under container because Image has no Ext of its own.
func (fp *fileParser) jobContainer(j *ir.Job, m *mapping) error {
	n := m.take("container")
	if n == nil {
		return nil
	}
	img, rest, ok, err := fp.image(n, "container")
	if err != nil {
		return err
	}
	if !ok {
		m.release("container")
		return nil
	}
	j.Container = &img
	if rest != nil {
		m.keep(extContainer, rest)
	}
	return nil
}

// image reads a string or an {image: …} mapping; rest holds the mapping's other keys.
func (fp *fileParser) image(n *yaml.Node, what string) (img ir.Image, rest map[string]any, ok bool, err error) {
	n = deref(n)
	if n.Kind == yaml.ScalarNode {
		s, err := fp.template(n, what)
		return ir.Image{Ref: s}, nil, err == nil, err
	}
	if n.Kind != yaml.MappingNode {
		return img, nil, false, nil
	}
	m, err := fp.mapping(n, what)
	if err != nil {
		return img, nil, false, err
	}
	in := m.take("image")
	if in == nil {
		return img, nil, false, nil
	}
	if img.Ref, err = fp.template(in, what+".image"); err != nil {
		return img, nil, false, err
	}
	rest, err = m.rest()
	return img, rest, err == nil, err
}

func (fp *fileParser) jobServices(j *ir.Job, m *mapping) error {
	n := m.take("services")
	if n == nil {
		return nil
	}
	sm, err := fp.mapping(n, "services")
	if err != nil {
		return err
	}
	extras := map[string]any{}
	for _, e := range sm.entries {
		sv, rest, err := fp.service(e)
		if err != nil {
			return err
		}
		j.Services = append(j.Services, sv)
		if rest != nil {
			extras[e.key] = rest
		}
	}
	if len(extras) > 0 {
		m.keep(extServices, extras)
	}
	return nil
}

func (fp *fileParser) service(e entry) (sv ir.Service, rest map[string]any, err error) {
	sv.Name = e.key
	if deref(e.val).Kind == yaml.ScalarNode {
		sv.Image.Ref, err = fp.template(e.val, "service "+e.key)
		return sv, nil, err
	}
	m, err := fp.mapping(e.val, "service "+e.key)
	if err != nil {
		return sv, nil, err
	}
	in := m.take("image")
	if in == nil {
		return sv, nil, fp.errf(e.val, "service %s needs image", e.key)
	}
	if sv.Image.Ref, err = fp.template(in, "service "+e.key+" image"); err != nil {
		return sv, nil, err
	}
	if err = fp.takeVars(m, "env", &sv.Env); err != nil {
		return sv, nil, err
	}
	if pn := m.take("ports"); pn != nil {
		l, ok := stringList(pn)
		if !ok {
			m.release("ports")
		}
		sv.Ports = l
	}
	rest, err = m.rest()
	return sv, rest, err
}

func (fp *fileParser) jobEnvironment(j *ir.Job, m *mapping) error {
	n := m.take("environment")
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		s, err := fp.template(n, "environment")
		j.Environment = &ir.Environment{Name: s}
		return err
	}
	em, err := fp.mapping(n, "environment")
	if err != nil {
		return err
	}
	env := &ir.Environment{}
	if v := em.take("name"); v != nil {
		if env.Name, err = fp.template(v, "environment.name"); err != nil {
			return err
		}
	}
	if v := em.take("url"); v != nil {
		if env.URL, err = fp.template(v, "environment.url"); err != nil {
			return err
		}
	}
	j.Environment = env
	rest, err := em.rest()
	if rest != nil {
		m.keep(extEnvironment, rest)
	}
	return err
}

func (fp *fileParser) jobOutputs(j *ir.Job, m *mapping) error {
	n := m.take("outputs")
	if n == nil {
		return nil
	}
	om, err := fp.mapping(n, "outputs")
	if err != nil {
		return err
	}
	for _, e := range om.entries {
		v, err := fp.template(e.val, "outputs."+e.key)
		if err != nil {
			return err
		}
		j.Outputs = append(j.Outputs, ir.Output{Name: e.key, Value: v})
	}
	return nil
}
