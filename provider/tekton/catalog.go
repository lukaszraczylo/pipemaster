package tekton

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
)

//go:embed catalog.yaml
var catalogData []byte

const (
	// ExtCatalogRef is the job Ext key that keeps the taskRef of a job that the catalog mapping replaced.
	ExtCatalogRef = "catalog_ref"

	kindCatalogTask     = "Task"
	kindCatalogCheckout = "Checkout"
	annotationCaveat    = "pipemaster.dev/caveat"
	actionCheckout      = "checkout"
	checkoutSubmodules  = "submodules"

	resolverGit      = "git"
	paramName        = "name"
	paramKind        = "kind"
	paramPathInRepo  = "pathInRepo"
	kindClusterTask  = "ClusterTask"
	kindTaskLower    = "task"
	catalogStrategy  = "mapped to the catalog task"
	droppedStrategy  = "dropped"
	catalogStepIndex = 1
)

// checkoutInput maps a catalog param to an input of the checkout action.
type checkoutInput struct {
	To      string `yaml:"to"`
	Default string `yaml:"default"`
}

// UnmarshalYAML reads the short form, a plain input name, or the long form.
func (c *checkoutInput) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		c.To = n.Value
		return nil
	}
	type long checkoutInput
	return n.Decode((*long)(c))
}

// catalogTask is one entry of catalog.yaml. A Checkout entry has inputs and no spec.
type catalogTask struct {
	spec       *omap
	inputs     map[string]checkoutInput
	same       map[string]string
	params     map[string]bool
	name       string
	caveat     string
	external   []string
	entrypoint []string
	isCheckout bool
	source     bool
}

type catalogDoc struct {
	Inputs   map[string]checkoutInput `yaml:"inputs"`
	Same     map[string]string        `yaml:"same"`
	Metadata struct {
		Annotations map[string]string `yaml:"annotations"`
		Name        string            `yaml:"name"`
	} `yaml:"metadata"`
	Kind       string    `yaml:"kind"`
	External   []string  `yaml:"external"`
	Entrypoint []string  `yaml:"entrypoint"`
	Spec       yaml.Node `yaml:"spec"`
	Source     bool      `yaml:"source"`
}

var loadCatalog = sync.OnceValues(func() (map[string]*catalogTask, error) {
	return parseCatalog(catalogData)
})

func parseCatalog(data []byte) (map[string]*catalogTask, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	out := map[string]*catalogTask{}
	for {
		var d catalogDoc
		err := dec.Decode(&d)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("catalog: %w", err)
		}
		t, err := d.task()
		if err != nil {
			return nil, err
		}
		out[t.name] = t
	}
}

func (d *catalogDoc) task() (*catalogTask, error) {
	t := &catalogTask{name: d.Metadata.Name, caveat: d.Metadata.Annotations[annotationCaveat]}
	t.external, t.entrypoint, t.source = d.External, d.Entrypoint, d.Source
	if t.name == "" || t.caveat == "" {
		return nil, fmt.Errorf("catalog: an entry needs a name and the %s annotation", annotationCaveat)
	}
	switch d.Kind {
	case kindCatalogCheckout:
		t.isCheckout = true
		t.inputs = map[string]checkoutInput{}
		for k, v := range d.Inputs {
			t.inputs[paramKey(k)] = v
		}
		t.same = map[string]string{}
		for k, v := range d.Same {
			t.same[paramKey(k)] = v
		}
	case kindCatalogTask:
		v, err := (&ynode.Builder{}).Build(&d.Spec, 0)
		if err != nil {
			return nil, fmt.Errorf("catalog %s: %w", t.name, err)
		}
		spec, ok := asMap(v)
		if !ok {
			return nil, fmt.Errorf("catalog %s: spec must be a mapping", t.name)
		}
		t.spec = spec
		t.params = specParams(spec)
	default:
		return nil, fmt.Errorf("catalog %s: unknown kind %q", t.name, d.Kind)
	}
	return t, nil
}

func specParams(spec *omap) map[string]bool {
	out := map[string]bool{}
	list, _ := spec.Vals["params"].([]any)
	for _, e := range list {
		if n, ok := text(valueOf(asMapOrNil(e), "name")); ok {
			out[n] = true
		}
	}
	return out
}

func asMapOrNil(v any) *omap {
	m, _ := asMap(v)
	return m
}

// refParam reads one param of a resolver taskRef.
func refParam(rm *omap, name string) (string, bool) {
	list, _ := rm.Vals["params"].([]any)
	for _, e := range list {
		m := asMapOrNil(e)
		if n, _ := text(valueOf(m, "name")); n == name {
			return text(valueOf(m, "value"))
		}
	}
	return "", false
}

// catalogName reads the catalog task name that a taskRef that is not a local Task names. It reads the
// reference only and never a local file.
func catalogName(rm *omap) (string, bool) {
	resolver, hasResolver := text(rm.Vals["resolver"])
	var name string
	switch {
	case hasResolver && resolver == resolverGit:
		p, ok := refParam(rm, paramPathInRepo)
		if !ok {
			return "", false
		}
		name = strings.TrimSuffix(strings.TrimSuffix(path.Base(p), ".yaml"), ".yml")
	case hasResolver:
		if k, ok := refParam(rm, paramKind); ok && !strings.EqualFold(k, kindTaskLower) {
			return "", false
		}
		name, _ = refParam(rm, paramName)
	default:
		if k, _ := text(rm.Vals["kind"]); k != "" && k != kindTaskName && k != kindClusterTask {
			return "", false
		}
		name, _ = text(rm.Vals["name"])
	}
	return name, name != "" && !strings.Contains(name, varOpen)
}

// catalogEntry finds the catalog task that a taskRef names.
func catalogEntry(rm *omap) (*catalogTask, bool, error) {
	name, ok := catalogName(rm)
	if !ok {
		return nil, false, nil
	}
	all, err := loadCatalog()
	if err != nil {
		return nil, false, err
	}
	t, ok := all[name]
	return t, ok, nil
}

// catalog replaces a remote taskRef with the steps of its catalog entry. The taskRef and the params stay in
// the job Ext, so a Tekton file gets the same reference back. A binding the mapping cannot carry leaves the
// reference as a remote one.
func (b *jobBuilder) catalog(rm *omap, line int, t *catalogTask) error {
	if len(b.opaque) > 0 {
		names := make([]string, 0, len(b.opaque))
		for n := range b.opaque {
			names = append(names, n)
		}
		sort.Strings(names)
		b.remote(rm, line, "catalog task "+t.name+" has no mapping while param "+names[0]+" binds a Tekton variable with no IR form")
		return nil
	}
	list, _ := b.rawParams.([]any)
	if t.isCheckout {
		return b.checkout(rm, t, list)
	}
	words, why := b.arrayWords(t, list)
	if why != "" {
		b.remote(rm, line, "catalog task "+t.name+" has no mapping: "+why)
		return nil
	}
	for name, w := range words {
		b.tx.bind[name] = []expr.Part{{Text: w}}
		b.raw[name] = w
	}
	b.keepCatalog(rm, t)
	if err := b.body(t.spec, nil); err != nil {
		return err
	}
	b.hoistImage(t)
	b.addSource(t)
	return nil
}

// checkoutStrategy is the strategy of the report entry for the checkout step that the parser adds to a job.
const checkoutStrategy = "checkout step in the job"

// addSource gives a job that reads the workspace its own checkout. The Tekton workspace that git-clone filled
// is not shared between jobs, so a target with one runner per job needs the clone in each job.
func (b *jobBuilder) addSource(t *catalogTask) {
	if !t.source {
		return
	}
	if b.srcClone != nil {
		b.sourceCloneStep(b.job.ID + "-checkout")
		return
	}
	ref, ok := actions.Reverse(actionCheckout)
	if !ok {
		return
	}
	b.job.Steps = append([]*ir.Step{{ID: b.job.ID + "-checkout", Kind: ir.StepAction, Action: &ref}}, b.job.Steps...)
	b.c.p.rep.Add(report.Entry{
		IRPath: b.path, Feature: caps.StepAction, Level: caps.Emulated, Strategy: checkoutStrategy,
		Detail: "the Tekton workspace is not shared between jobs, so the job clones the pipeline repository itself; a revision that an earlier git-clone task set is not carried over", SourceLoc: b.c.file,
	})
}

// hoistImage makes the image of a one-step task the job image. A target that sets the image per job, such as
// GitLab, then keeps it, and a one-step Task runs in that image either way.
func (b *jobBuilder) hoistImage(t *catalogTask) {
	if len(b.job.Steps) != 1 || b.job.Steps[0].Image == nil || b.job.Container != nil {
		return
	}
	b.job.Container, b.job.Steps[0].Image = b.job.Steps[0].Image, nil
	b.job.Container.Entrypoint = t.entrypoint
}

// keepCatalog stores the reference and the bindings, and states the verdict.
func (b *jobBuilder) keepCatalog(rm *omap, t *catalogTask) {
	b.k.Out[ExtCatalogRef] = plain(rm)
	if b.rawParams != nil {
		b.k.Out["params"] = plain(b.rawParams)
	}
	b.c.p.rep.Add(report.Entry{
		IRPath: b.path, Feature: caps.StepAction, Level: caps.Emulated,
		Strategy: catalogStrategy + " " + t.name, Detail: t.caveat, SourceLoc: b.c.file,
	})
	for _, need := range t.external {
		b.c.p.rep.Add(report.Entry{
			IRPath: b.path, Feature: caps.StepAction, Level: caps.External, Strategy: "set up outside the file",
			Detail: need, SourceLoc: b.c.file,
		})
	}
}

// arrayWords joins each bound array param that the entry declares into one quoted word list. An element with
// a variable has no static word, so the mapping stops.
func (b *jobBuilder) arrayWords(t *catalogTask, list []any) (map[string]string, string) {
	out := map[string]string{}
	for _, e := range list {
		m := asMapOrNil(e)
		name, _ := text(valueOf(m, "name"))
		arr, isArr := m.Vals["value"].([]any)
		if !isArr || !t.params[name] {
			continue
		}
		words := make([]string, 0, len(arr))
		for _, el := range arr {
			s, ok := text(el)
			if !ok || strings.Contains(s, varOpen) {
				return nil, "array param " + name + " holds an element with no static text"
			}
			q, err := b.quote(s)
			if err != nil {
				return nil, err.Error()
			}
			words = append(words, q)
		}
		out[name] = strings.Join(words, " ")
	}
	return out, ""
}

// checkout reads a git-clone style task as one checkout step.
func (b *jobBuilder) checkout(rm *omap, t *catalogTask, list []any) error {
	ref, ok := actions.Reverse(actionCheckout)
	if !ok {
		return fmt.Errorf("the action registry has no checkout")
	}
	foreignURL, foreign := foreignClone(list)
	with, unmapped := b.checkoutWith(t, list, foreign)
	b.keepCatalog(rm, t)
	sort.Strings(unmapped)
	for _, name := range unmapped {
		b.c.p.rep.Add(report.Entry{
			IRPath: b.path, Feature: caps.StepAction, Level: caps.Lost, Strategy: droppedStrategy,
			Detail:    "param " + name + " of the catalog task " + t.name + " has no checkout input and is not applied",
			SourceLoc: b.c.file,
		})
	}
	if len(with) == 0 {
		with = nil
	}
	id := fmt.Sprintf("%s-step-%d", b.job.ID, catalogStepIndex)
	if foreign {
		return b.foreignCloneStep(id, foreignURL, with, list)
	}
	b.job.Steps = append(b.job.Steps, &ir.Step{ID: id, Kind: ir.StepAction, Action: &ref, With: with})
	return nil
}

// checkoutWith maps the params of a git-clone task to the inputs of the checkout. The params with no input are
// returned by name. skipURL leaves the url out, for a clone that a script runs.
func (b *jobBuilder) checkoutWith(t *catalogTask, list []any, skipURL bool) (map[string]ir.Value, []string) {
	with := map[string]ir.Value{}
	bound := map[string]bool{}
	var unmapped []string
	for _, e := range list {
		m := asMapOrNil(e)
		name, _ := text(valueOf(m, "name"))
		key := paramKey(name)
		val, isText := text(m.Vals["value"])
		if skipURL && key == "url" {
			continue
		}
		if in, mapped := t.inputs[key]; mapped && isText {
			if v, ok := b.checkoutValue(in.To, val); ok {
				if v != nil {
					with[in.To] = *v
				}
				bound[key] = true
				continue
			}
			unmapped = append(unmapped, name)
			continue
		}
		if want, same := t.same[key]; same && isText && val == want {
			continue
		}
		unmapped = append(unmapped, name)
	}
	for key, in := range t.inputs {
		if _, set := with[in.To]; !set && !bound[key] && in.Default != "" {
			with[in.To] = ir.Value{Lit: in.Default}
		}
	}
	return with, unmapped
}

// checkoutValue reads a bound param as the value of a checkout input. A nil value means the param has no effect.
// A param that has no spelling returns false.
func (b *jobBuilder) checkoutValue(input, val string) (*ir.Value, bool) {
	var v ir.Value
	switch input {
	case checkoutRepository:
		if val == "" {
			return nil, true
		}
		repo, own, ok := cloneRepo(b.px, val)
		if !ok || own {
			return nil, ok
		}
		v = repo
	case checkoutPath:
		p, ok := clonePath(b.px, val)
		if !ok {
			return nil, true
		}
		v = p
	case checkoutSparse:
		if val == "" {
			return nil, true
		}
		s, ok := cloneSparse(b.px, val)
		if !ok {
			return nil, false
		}
		v = s
	default:
		v = b.px.value(val)
	}
	return &v, true
}

// paramKey folds a param name, so the catalog spelling sslVerify and the OpenShift spelling SSL_VERIFY meet.
func paramKey(name string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(name))
}
