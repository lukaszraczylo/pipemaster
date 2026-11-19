package ir

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
)

// RefSeg separates a pipeline path from the config reference that a config.name use names.
const RefSeg = "/ref/"

// ConfigRef splits a config.name use path into its reference family (secrets or vars) and name.
func ConfigRef(path string) (family, name string, ok bool) {
	_, rest, found := strings.Cut(path, RefSeg)
	if !found {
		return "", "", false
	}
	return strings.Cut(rest, ".")
}

// refScan collects the secrets.X and vars.X references of one pipeline, one use per distinct reference.
type refScan struct {
	s    *featureSet
	base string
}

func (r *refScan) add(family, name string) {
	r.s.add(r.base+RefSeg+family+"."+name, caps.ConfigName)
}

func (r *refScan) expr(x expr.Expr) {
	switch v := x.(type) {
	case expr.Ref:
		if len(v.Path) >= 2 && (v.Path[0] == expr.PrefixSecrets || v.Path[0] == expr.PrefixVars) {
			r.add(v.Path[0], v.Path[1])
		}
	case expr.Unary:
		r.expr(v.X)
	case expr.Binary:
		r.expr(v.L)
		r.expr(v.R)
	case expr.Call:
		for _, a := range v.Args {
			r.expr(a)
		}
	}
}

// text scans a script template; text that does not parse holds no reference the IR can name.
func (r *refScan) text(ss ...string) {
	for _, s := range ss {
		if !strings.Contains(s, "${{") {
			continue
		}
		parts, err := expr.ParseScriptTemplate(s)
		if err != nil {
			continue
		}
		for _, p := range parts {
			r.expr(p.Expr)
		}
	}
}

func (r *refScan) value(v Value) {
	switch v.Kind {
	case ValueSecret:
		r.add(expr.PrefixSecrets, v.Secret)
	case ValueExpr:
		r.expr(v.Expr)
	default:
		r.text(v.Lit)
	}
}

func (r *refScan) vars(vs []Var) {
	for _, v := range vs {
		r.value(v.Value)
	}
}

// ext scans the strings an Ext value holds, at any depth. Ext keeps provider keys the IR does not model, such as
// container credentials, and they can name a secret.
func (r *refScan) ext(v any) {
	switch x := v.(type) {
	case string:
		r.text(x)
	case Ext:
		for _, e := range x {
			r.ext(e)
		}
	case map[string]any:
		for _, e := range x {
			r.ext(e)
		}
	case []any:
		for _, e := range x {
			r.ext(e)
		}
	case []string:
		r.text(x...)
	case map[string]string:
		for _, e := range x {
			r.text(e)
		}
	}
}

func (r *refScan) image(img *Image) {
	if img != nil {
		r.text(img.Ref)
	}
}

func (r *refScan) pipeline(pl *Pipeline) {
	for _, n := range ExtNodes(&Project{Pipelines: []*Pipeline{pl}}) {
		r.ext(*n.Ext)
	}
	r.vars(pl.Env)
	if pl.Concurrency != nil {
		r.text(pl.Concurrency.Group)
	}
	for _, j := range append(append([]*Job{}, pl.Jobs...), pl.Finally...) {
		if j != nil {
			r.job(j)
		}
	}
}

func (r *refScan) job(j *Job) {
	r.expr(j.If)
	r.vars(j.Env)
	r.image(j.Container)
	for _, sv := range j.Services {
		r.image(&sv.Image)
		r.vars(sv.Env)
	}
	if j.Environment != nil {
		r.text(j.Environment.Name, j.Environment.URL)
	}
	for _, o := range j.Outputs {
		r.text(o.Value)
	}
	for _, c := range j.Cache {
		r.text(c.Key)
	}
	for _, a := range j.Artifacts {
		r.text(a.Name)
	}
	for _, st := range j.Steps {
		if st == nil {
			continue
		}
		r.expr(st.If)
		r.image(st.Image)
		r.vars(st.Env)
		for _, v := range st.With {
			r.value(v)
		}
		r.text(st.Script...)
		r.text(st.WorkDir)
	}
}
