package gitlab

import (
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	extImage       = "image"
	extServices    = "services"
	extEnvironment = "environment"
	extCache       = "cache"
	extArtifacts   = "artifacts"
	extParallel    = "parallel"

	maxMatrixCombos = 1000
)

// image reads a string or an {name, entrypoint, ...} mapping; rest holds the keys the IR has no field for.
func (s *state) image(n *yaml.Node, what string) (img ir.Image, rest map[string]any, ok bool, err error) {
	if n.Kind == yaml.ScalarNode {
		img.Ref, err = s.text(n, what)
		return img, nil, err == nil, err
	}
	if n.Kind != yaml.MappingNode {
		return img, nil, false, nil
	}
	r, err := s.reader(n, what)
	if err != nil {
		return img, nil, false, err
	}
	name := r.take("name")
	if name == nil {
		return img, nil, false, nil
	}
	if img.Ref, err = s.text(name, what+".name"); err != nil {
		return img, nil, false, err
	}
	if e := r.take("entrypoint"); e != nil {
		l, lok := stringList(e)
		if !lok {
			r.release("entrypoint")
		}
		img.Entrypoint = l
	}
	x, err := r.ext()
	if x != nil {
		rest = x[dialect].(map[string]any)
	}
	return img, rest, err == nil, err
}

func (s *state) jobImage(j *ir.Job, r *reader) error {
	n := r.take("image")
	if n == nil {
		return nil
	}
	img, rest, ok, err := s.image(n, "image")
	if err != nil {
		return err
	}
	if !ok {
		r.release("image")
		return nil
	}
	j.Container = &img
	if rest != nil {
		r.keep(extImage, rest)
	}
	return nil
}

// DerivedServiceName is the hostname a service gets when it has no alias: the image's last path segment without tag or digest.
func DerivedServiceName(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	ref, _, _ = strings.Cut(ref, ":")
	return ref
}

func (s *state) jobServices(j *ir.Job, r *reader) error {
	n := r.take("services")
	if n == nil {
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		r.release("services")
		return nil
	}
	extras := make([]any, 0, len(n.Content))
	anyExtra := false
	for _, it := range n.Content {
		sv, rest, err := s.service(it)
		if err != nil {
			return err
		}
		j.Services = append(j.Services, sv)
		if rest == nil {
			rest = map[string]any{}
		} else {
			anyExtra = true
		}
		extras = append(extras, rest)
	}
	if anyExtra {
		r.keep(extServices, extras)
	}
	return nil
}

func (s *state) service(n *yaml.Node) (sv ir.Service, rest map[string]any, err error) {
	if n.Kind == yaml.ScalarNode {
		if sv.Image.Ref, err = s.text(n, "service"); err != nil {
			return sv, nil, err
		}
		sv.Name = DerivedServiceName(sv.Image.Ref)
		return sv, nil, nil
	}
	r, err := s.reader(n, "service")
	if err != nil {
		return sv, nil, err
	}
	name := r.take("name")
	if name == nil {
		return sv, nil, s.errf(n, "service needs a name")
	}
	if sv.Image.Ref, err = s.text(name, "service name"); err != nil {
		return sv, nil, err
	}
	sv.Name = DerivedServiceName(sv.Image.Ref)
	if a := r.take("alias"); a != nil {
		if sv.Name, err = s.scalar(a, "service alias"); err != nil {
			return sv, nil, err
		}
	}
	if e := r.take("entrypoint"); e != nil {
		l, ok := stringList(e)
		if !ok {
			r.release("entrypoint")
		}
		sv.Image.Entrypoint = l
	}
	if v := r.take("variables"); v != nil {
		vars, extras, err := s.variables("pipeline/"+pipelineID, v)
		if err != nil {
			return sv, nil, err
		}
		sv.Env = vars
		if extras != nil {
			r.keep(extVariables, extras)
		}
	}
	x, err := r.ext()
	if x != nil {
		rest = x[dialect].(map[string]any)
	}
	return sv, rest, err
}

func (s *state) jobEnvironment(j *ir.Job, r *reader) error {
	n := r.take("environment")
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		name, err := s.text(n, "environment")
		j.Environment = &ir.Environment{Name: name}
		return err
	}
	er, err := s.reader(n, "environment")
	if err != nil {
		return err
	}
	env := &ir.Environment{}
	if v := er.take("name"); v != nil {
		if env.Name, err = s.text(v, "environment name"); err != nil {
			return err
		}
	}
	if v := er.take("url"); v != nil {
		if env.URL, err = s.text(v, "environment url"); err != nil {
			return err
		}
	}
	j.Environment = env
	x, err := er.ext()
	if x != nil {
		r.keep(extEnvironment, x[dialect])
	}
	return err
}

func (s *state) jobCache(j *ir.Job, r *reader) error {
	n := r.take("cache")
	if n == nil {
		return nil
	}
	var items []*yaml.Node
	switch n.Kind {
	case yaml.MappingNode:
		items = []*yaml.Node{n}
	case yaml.SequenceNode:
		items = n.Content
	default:
		r.release("cache")
		return nil
	}
	extras := make([]any, 0, len(items))
	anyExtra := false
	for _, it := range items {
		c, rest, err := s.cacheEntry(it)
		if err != nil {
			return err
		}
		j.Cache = append(j.Cache, c)
		if rest == nil {
			rest = map[string]any{}
		} else {
			anyExtra = true
		}
		extras = append(extras, rest)
	}
	if anyExtra {
		r.keep(extCache, extras)
	}
	return nil
}

// cacheEntry maps one cache block. A key given as {files, prefix} also becomes a derived hashFiles key,
// and the raw form stays in rest["key"] so the emitter can restore it.
func (s *state) cacheEntry(n *yaml.Node) (c ir.Cache, rest map[string]any, err error) {
	r, err := s.reader(n, "cache")
	if err != nil {
		return c, nil, err
	}
	if k := r.take("key"); k != nil {
		if k.Kind == yaml.MappingNode {
			var raw any
			if c.Key, err = s.cacheKeyFiles(k); err != nil {
				return c, nil, err
			}
			if raw, err = s.plain(k); err != nil {
				return c, nil, err
			}
			r.keep("key", raw)
		} else if c.Key, err = s.text(k, "cache key"); err != nil {
			return c, nil, err
		}
	}
	if p := r.take("paths"); p != nil {
		l, ok := stringList(p)
		if !ok {
			r.release("paths")
		}
		c.Paths = l
	}
	if p := r.take("policy"); p != nil {
		if c.Policy, err = s.scalar(p, "cache policy"); err != nil {
			return c, nil, err
		}
	}
	x, err := r.ext()
	if x != nil {
		rest = x[dialect].(map[string]any)
	}
	return c, rest, err
}

func (s *state) cacheKeyFiles(k *yaml.Node) (string, error) {
	prefix := ""
	if p := get(k, "prefix"); p != nil {
		var err error
		if prefix, err = s.text(p, "cache key prefix"); err != nil {
			return "", err
		}
	}
	files, _ := stringList(get(k, "files"))
	if len(files) == 0 {
		return prefix, nil
	}
	args := make([]expr.Expr, len(files))
	for i, f := range files {
		args[i] = expr.Lit{Kind: expr.LitStr, Str: f}
	}
	h, err := expr.PrintScriptTemplate([]expr.Part{{Expr: expr.Call{Name: "hashFiles", Args: args}}})
	if err != nil {
		return "", s.errf(k, "%v", err)
	}
	if prefix == "" {
		return h, nil
	}
	return prefix + "-" + h, nil
}

func (s *state) jobArtifacts(j *ir.Job, r *reader) error {
	n := r.take("artifacts")
	if n == nil {
		return nil
	}
	ar, err := s.reader(n, "artifacts")
	if err != nil {
		return err
	}
	var a ir.Artifact
	if p := ar.take("paths"); p != nil {
		l, ok := stringList(p)
		if !ok {
			ar.release("paths")
		}
		a.Paths = l
	}
	if v := ar.take("name"); v != nil {
		if a.Name, err = s.text(v, "artifacts name"); err != nil {
			return err
		}
	}
	if v := ar.take("expire_in"); v != nil {
		if a.Expire, err = s.scalar(v, "artifacts expire_in"); err != nil {
			return err
		}
	}
	if len(a.Paths) > 0 || a.Name != "" || a.Expire != "" {
		j.Artifacts = []ir.Artifact{a}
	}
	x, err := ar.ext()
	if x != nil {
		r.keep(extArtifacts, x[dialect])
	}
	return err
}

// jobParallel maps parallel:matrix. A count (parallel: N) has no IR field and stays in Ext.
func (s *state) jobParallel(j *ir.Job, r *reader) error {
	n := r.peek("parallel")
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	pr, err := s.reader(n, "parallel")
	if err != nil {
		return err
	}
	mx := pr.take("matrix")
	if mx == nil {
		return nil
	}
	m, ok, err := s.matrix(mx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	r.take("parallel")
	j.Matrix = m
	x, err := pr.ext()
	if x != nil {
		r.keep(extParallel, x[dialect])
	}
	return err
}

// matrix maps parallel:matrix. One entry becomes Axes. Several entries are a union of products, which IR
// axes cannot express, so every combination goes into Include with no Axes (Expand then yields exactly the union).
func (s *state) matrix(n *yaml.Node) (*ir.Matrix, bool, error) {
	if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		return nil, false, nil
	}
	var products [][]ir.Axis
	for _, e := range n.Content {
		if e.Kind != yaml.MappingNode {
			return nil, false, nil
		}
		var axes []ir.Axis
		for _, en := range entries(e) {
			vals, ok := stringList(en.val)
			if !ok || len(vals) == 0 {
				return nil, false, nil
			}
			axes = append(axes, ir.Axis{Name: en.name, Values: vals})
		}
		products = append(products, axes)
	}
	if len(products) == 1 {
		return &ir.Matrix{Axes: products[0]}, true, nil
	}
	m := &ir.Matrix{}
	for _, axes := range products {
		combos, err := s.product(n, axes, len(m.Include))
		if err != nil {
			return nil, false, err
		}
		m.Include = append(m.Include, combos...)
	}
	return m, true, nil
}

func (s *state) product(at *yaml.Node, axes []ir.Axis, have int) ([]ir.Combo, error) {
	res := []ir.Combo{{}}
	for _, a := range axes {
		if (len(res)+have)*len(a.Values) > maxMatrixCombos {
			return nil, s.errf(at, "matrix has more than %d combinations", maxMatrixCombos)
		}
		next := make([]ir.Combo, 0, len(res)*len(a.Values))
		for _, c := range res {
			for _, v := range a.Values {
				nc := append(append(ir.Combo{}, c...), ir.KV{Key: a.Name, Value: v})
				next = append(next, nc)
			}
		}
		res = next
	}
	for _, c := range res {
		sort.Slice(c, func(i, k int) bool { return c[i].Key < c[k].Key })
	}
	return res, nil
}
