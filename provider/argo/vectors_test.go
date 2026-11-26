package argo_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/internal/vectors"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/argo"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// The abstract documents below mirror lean4/Pipemaster/Argo.lean; their JSON keys are the ones GenVectors writes.
type (
	vStep struct {
		Name   string        `json:"name"`
		Disp   string        `json:"disp"`
		Script []string      `json:"script"`
		Env    []vectors.Var `json:"env"`
	}
	vWait struct {
		Deps    *[]string `json:"deps,omitempty"`
		Depends any       `json:"depends,omitempty"`
	}
	vTask struct {
		Retries    *int          `json:"retries"`
		Name       string        `json:"name"`
		Disp       string        `json:"disp"`
		Wait       vWait         `json:"wait"`
		Steps      []vStep       `json:"steps"`
		JobEnv     []vectors.Var `json:"job_env"`
		Timeout    int           `json:"timeout"`
		OnError    bool          `json:"on_error"`
		FailedOnly bool          `json:"failed_only"`
		Noop       bool          `json:"noop"`
	}
	vTrig struct {
		Kind    string   `json:"kind"`
		Include []string `json:"include"`
	}
	vDoc struct {
		Name    string  `json:"name"`
		Disp    string  `json:"disp"`
		Tasks   []vTask `json:"tasks"`
		Finally []vTask `json:"finally"`
		Trigs   []vTrig `json:"trigs"`
	}
)

const (
	vectorOrigin  = "gha"
	workflowsDir  = ".argo/workflows/"
	sensorFile    = ".argo/events/sensor.yaml"
	wantAPI       = "argoproj.io/v1alpha1"
	wantImage     = "alpine:3.22"
	wantWorkDir   = "/workspace"
	entryName     = "main"
	exitName      = "exit-handler"
	noopName      = "main"
	noopCmd       = "true"
	failedWhen    = "{{= workflow.status == 'Failed' }}"
	titleKey      = "workflows.argoproj.io/title"
	kindPR        = "pr"
	kindPush      = "push"
	kindTag       = "tag"
	featName      = "display.name"
	featEnv       = "job.env"
	featNoop      = "step.script"
	featShell     = "step.shell"
	featRunner    = "job.runner"
	featPrefix    = "trigger."
	jobSegment    = "/job/"
	stepSegment   = "/step/"
	refHeads      = "^refs/heads/"
	refTags       = "^refs/tags/"
	anyTail       = ".*"
	segmentStar   = "[^/]*"
	forkGate      = "head == base"
	pathRef       = "body.ref"
	pathBase      = "body.pull_request.base.ref"
	pathAction    = "body.action"
	wantDocsInSet = 1
)

var (
	prActions      = []string{"opened", "reopened", "synchronize"}
	generatedFiles = []string{
		".argo/events/eventsource.yaml", sensorFile, ".argo/rbac/sensor.yaml", ".argo/rbac/workflow.yaml",
	}
)

// onlyKeys fails when a mapping holds a key the model leaves out.
func onlyKeys(es []vectors.Entry, what string, keys ...string) error {
	for _, e := range es {
		known := false
		for _, k := range keys {
			known = known || e.Key == k
		}
		if !known {
			return fmt.Errorf("%s key %q is not modelled", what, e.Key)
		}
	}
	return nil
}

func lookup(es []vectors.Entry, key string) *yaml.Node {
	for _, e := range es {
		if e.Key == key {
			return e.Val
		}
	}
	return nil
}

func scalarIs(es []vectors.Entry, what, key, want string) error {
	if v := lookup(es, key); v == nil || v.Value != want {
		return fmt.Errorf("%s %s is not %q", what, key, want)
	}
	return nil
}

// decodeEnv reads a Kubernetes env list of name and literal value pairs.
func decodeEnv(n *yaml.Node) ([]vectors.Var, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("line %d: env must be a list", n.Line)
	}
	out := []vectors.Var{}
	for _, c := range n.Content {
		es, err := vectors.Entries(c)
		if err != nil {
			return nil, err
		}
		if err := onlyKeys(es, "env", "name", "value"); err != nil {
			return nil, err
		}
		name, val := lookup(es, "name"), lookup(es, "value")
		if name == nil || val == nil {
			return nil, fmt.Errorf("line %d: env entry needs a name and a value", c.Line)
		}
		out = append(out, vectors.Var{Name: name.Value, Value: val.Value})
	}
	return out, nil
}

// decodeContainer reads one container of a containerSet. prev is the container before it, which the
// `dependencies` chain must name.
func decodeContainer(n *yaml.Node, prev string) (vStep, error) {
	s := vStep{Script: []string{}, Env: []vectors.Var{}}
	es, err := vectors.Entries(n)
	if err != nil {
		return s, err
	}
	var deps []string
	wrapped := false
	for _, e := range es {
		switch e.Key {
		case "name":
			s.Name = e.Val.Value
		case "image":
			if e.Val.Value != wantImage {
				err = fmt.Errorf("image %q, want %q", e.Val.Value, wantImage)
			}
		case "command":
			var cmd []string
			if cmd, err = vectors.Strings(e.Val); err == nil {
				// A step from a bash-default source runs the script through the bash-if-present wrapper.
				switch wrapped = strings.Join(cmd, " ") == "sh -c"; {
				case wrapped:
				case strings.Join(cmd, " ") != "sh -ec":
					err = fmt.Errorf("command %v is not sh -ec", cmd)
				}
			}
		case "args":
			var args []string
			if args, err = vectors.Strings(e.Val); err == nil {
				switch {
				case wrapped && (len(args) != 3 || args[0] != provider.BashOrShScript || args[1] != provider.BashOrShArgv0):
					err = fmt.Errorf("args %v are not the bash-if-present wrapper and one script", args)
				case wrapped:
					s.Script = strings.Split(strings.TrimSuffix(args[2], "\n"), "\n")
				case len(args) != 1:
					err = fmt.Errorf("%d args, want one script", len(args))
				default:
					s.Script = strings.Split(strings.TrimSuffix(args[0], "\n"), "\n")
				}
			}
		case "workingDir":
			if e.Val.Value != wantWorkDir {
				err = fmt.Errorf("workingDir %q, want %q", e.Val.Value, wantWorkDir)
			}
		case "env":
			s.Env, err = decodeEnv(e.Val)
		case "dependencies":
			deps, err = vectors.Strings(e.Val)
		default:
			err = fmt.Errorf("container key %q is not modelled", e.Key)
		}
		if err != nil {
			return s, fmt.Errorf("container %s: %w", s.Name, err)
		}
	}
	switch {
	case prev == "" && len(deps) != 0:
		return s, fmt.Errorf("container %s: first container has dependencies %v", s.Name, deps)
	case prev != "" && (len(deps) != 1 || deps[0] != prev):
		return s, fmt.Errorf("container %s: dependencies %v, want [%s]", s.Name, deps, prev)
	}
	return s, nil
}

var volumeMounts = "workspace:/workspace"

// decodeVolumes checks the constant workspace volume of a template.
func decodeVolumes(vol *yaml.Node, mounts *yaml.Node) error {
	if vol == nil || mounts == nil {
		return fmt.Errorf("template has no workspace volume")
	}
	ves, err := vectors.Entries(vol.Content[0])
	if err != nil {
		return err
	}
	if err := onlyKeys(ves, "volume", "name", "emptyDir"); err != nil {
		return err
	}
	if err := scalarIs(ves, "volume", "name", "workspace"); err != nil {
		return err
	}
	mes, err := vectors.Entries(mounts.Content[0])
	if err != nil {
		return err
	}
	if err := onlyKeys(mes, "volumeMount", "name", "mountPath"); err != nil {
		return err
	}
	if got := lookup(mes, "name").Value + ":" + lookup(mes, "mountPath").Value; got != volumeMounts {
		return fmt.Errorf("volumeMount %q, want %q", got, volumeMounts)
	}
	return nil
}

// vTmpl is one job template: its steps, timeout and retry limit.
type vTmpl struct {
	retries *int
	steps   []vStep
	timeout int
}

// checkNodeSelector accepts the template nodeSelector that the emitter writes for runner tags: every key is a tag
// under the pipemaster prefix with the value true. The Lean model leaves the tags out of the file.
func checkNodeSelector(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	es, err := vectors.Entries(n)
	if err != nil {
		return err
	}
	for _, kv := range es {
		if !strings.HasPrefix(kv.Key, emitkit.NodeKeyPrefix) || kv.Val.Value != emitkit.TagValue {
			return fmt.Errorf("nodeSelector %q: %q is not a runner tag", kv.Key, kv.Val.Value)
		}
	}
	return nil
}

func decodeTemplate(es []vectors.Entry) (vTmpl, error) {
	t := vTmpl{steps: []vStep{}}
	if err := onlyKeys(es, "template", "name", "containerSet", "timeout", "retryStrategy", "volumes", "nodeSelector"); err != nil {
		return t, err
	}
	if err := checkNodeSelector(lookup(es, "nodeSelector")); err != nil {
		return t, err
	}
	cs, err := vectors.Entries(lookup(es, "containerSet"))
	if err != nil {
		return t, err
	}
	if err := onlyKeys(cs, "containerSet", "containers", "volumeMounts"); err != nil {
		return t, err
	}
	prev := ""
	for _, c := range lookup(cs, "containers").Content {
		s, err := decodeContainer(c, prev)
		if err != nil {
			return t, err
		}
		prev = s.Name
		t.steps = append(t.steps, s)
	}
	if err := decodeVolumes(lookup(es, "volumes"), lookup(cs, "volumeMounts")); err != nil {
		return t, err
	}
	if v := lookup(es, "timeout"); v != nil {
		d, err := time.ParseDuration(v.Value)
		if err != nil || d%time.Minute != 0 {
			return t, fmt.Errorf("timeout %q is not whole minutes", v.Value)
		}
		t.timeout = int(d / time.Minute)
	}
	if v := lookup(es, "retryStrategy"); v != nil {
		rs, err := vectors.Entries(v)
		if err != nil {
			return t, err
		}
		if err := onlyKeys(rs, "retryStrategy", "limit"); err != nil {
			return t, err
		}
		limit, err := strconv.Atoi(lookup(rs, "limit").Value)
		if err != nil {
			return t, err
		}
		t.retries = &limit
	}
	return t, nil
}

// decodeTask reads one task of a DAG and joins the template it names.
func decodeTask(n *yaml.Node, tmpls map[string]vTmpl, used map[string]bool, finally bool) (vTask, error) {
	t := vTask{Steps: []vStep{}, JobEnv: []vectors.Var{}}
	es, err := vectors.Entries(n)
	if err != nil {
		return t, err
	}
	var ref string
	for _, e := range es {
		switch e.Key {
		case "name":
			t.Name = e.Val.Value
		case "template":
			ref = e.Val.Value
		case "dependencies":
			var deps []string
			if deps, err = vectors.Strings(e.Val); err == nil {
				t.Wait.Deps = &deps
			}
		case "continueOn":
			var co []vectors.Entry
			if co, err = vectors.Entries(e.Val); err == nil {
				if err = onlyKeys(co, "continueOn", "failed"); err == nil {
					t.OnError, err = vectors.Bool(lookup(co, "failed"))
				}
			}
		case "when":
			if !finally || e.Val.Value != failedWhen {
				err = fmt.Errorf("when %q is not the status test of an exit task", e.Val.Value)
			}
			t.FailedOnly = true
		default:
			err = fmt.Errorf("task key %q is not modelled", e.Key)
		}
		if err != nil {
			return t, fmt.Errorf("task %s: %w", t.Name, err)
		}
	}
	if t.Wait.Deps == nil {
		t.Wait.Deps = &[]string{}
	}
	tm, ok := tmpls[ref]
	if !ok || ref != t.Name {
		return t, fmt.Errorf("task %s names template %q", t.Name, ref)
	}
	used[ref] = true
	t.Steps, t.Timeout, t.Retries = tm.steps, tm.timeout, tm.retries
	if t.Retries != nil && *t.Retries < 0 {
		return t, fmt.Errorf("task %s: retry limit %d", t.Name, *t.Retries)
	}
	return t, nil
}

type nameArg struct {
	Name string `json:"name"`
}

type envArg struct {
	Vars string `json:"vars"`
}

type envVar struct {
	Value struct {
		Lit string `yaml:"lit"`
	} `yaml:"value"`
	Name string `yaml:"name"`
}

// applyMarks reads the markers of the workflow file: display names, job variables and the placeholder script.
// Trigger markers are checked against the Sensor, so the other features are outside the model.
func applyMarks(d *vDoc, src []byte) error {
	jobs := map[string]*vTask{}
	for _, list := range []*[]vTask{&d.Tasks, &d.Finally} {
		for i := range *list {
			jobs[(*list)[i].Name] = &(*list)[i]
		}
	}
	prefix := "pipeline/" + d.Name + jobSegment
	for _, m := range vectors.Marks(src) {
		if strings.HasPrefix(m.Feature, featPrefix) {
			continue
		}
		rest := strings.TrimPrefix(m.Path, prefix)
		if rest == m.Path {
			return fmt.Errorf("marker %s %s is outside the pipeline", m.Feature, m.Path)
		}
		jobID, stepID, isStep := strings.Cut(rest, stepSegment)
		t := jobs[jobID]
		if t == nil {
			return fmt.Errorf("marker %s %s names no task", m.Feature, m.Path)
		}
		switch m.Feature {
		case featName:
			var a nameArg
			if err := m.DecodeArg(&a); err != nil {
				return err
			}
			if !isStep {
				t.Disp = a.Name
				continue
			}
			found := false
			for i := range t.Steps {
				if t.Steps[i].Name == stepID {
					t.Steps[i].Disp, found = a.Name, true
				}
			}
			if !found {
				return fmt.Errorf("marker %s %s names no container", m.Feature, m.Path)
			}
		case featEnv:
			var a envArg
			if err := m.DecodeArg(&a); err != nil {
				return err
			}
			var vars []envVar
			if err := yaml.Unmarshal([]byte(a.Vars), &vars); err != nil {
				return err
			}
			t.JobEnv = []vectors.Var{}
			for _, v := range vars {
				t.JobEnv = append(t.JobEnv, vectors.Var{Name: v.Name, Value: v.Value.Lit})
			}
		case featNoop:
			var a struct{}
			if err := m.DecodeArg(&a); err != nil {
				return err
			}
			t.Noop = true
			if len(t.Steps) != 1 || t.Steps[0].Name != noopName || strings.Join(t.Steps[0].Script, "\n") != noopCmd {
				return fmt.Errorf("marker %s %s: container is not the placeholder", m.Feature, m.Path)
			}
		case featRunner:
			// The note says the runner labels are template nodeSelector keys, which checkNodeSelector reads.
		case featShell:
			// The note says a step from a bash-default source runs through the wrapper; the decoder reads the wrapper itself.
			if !isStep || !slices.ContainsFunc(t.Steps, func(s vStep) bool { return s.Name == stepID }) {
				return fmt.Errorf("marker %s %s names no container", m.Feature, m.Path)
			}
		default:
			return fmt.Errorf("marker %s %s is not modelled", m.Feature, m.Path)
		}
	}
	return nil
}

func decodeDAG(es []vectors.Entry, tmpls map[string]vTmpl, used map[string]bool, finally bool) ([]vTask, error) {
	dag, err := vectors.Entries(lookup(es, "dag"))
	if err != nil {
		return nil, err
	}
	if err := onlyKeys(dag, "dag", "tasks"); err != nil {
		return nil, err
	}
	out := []vTask{}
	for _, c := range lookup(dag, "tasks").Content {
		t, err := decodeTask(c, tmpls, used, finally)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// decodeWorkflow reads the WorkflowTemplate file into the model's abstract document.
func decodeWorkflow(src []byte) (vDoc, error) {
	d := vDoc{Tasks: []vTask{}, Finally: []vTask{}, Trigs: []vTrig{}}
	root, err := vectors.RootMapping(src)
	if err != nil {
		return d, err
	}
	if err := onlyKeys(root, "workflow", "apiVersion", "kind", "metadata", "spec"); err != nil {
		return d, err
	}
	if err := scalarIs(root, "workflow", "apiVersion", wantAPI); err != nil {
		return d, err
	}
	if err := scalarIs(root, "workflow", "kind", "WorkflowTemplate"); err != nil {
		return d, err
	}
	meta, err := vectors.Entries(lookup(root, "metadata"))
	if err != nil {
		return d, err
	}
	if err := onlyKeys(meta, "metadata", "name", "annotations"); err != nil {
		return d, err
	}
	d.Name = lookup(meta, "name").Value
	if an := lookup(meta, "annotations"); an != nil {
		aes, err := vectors.Entries(an)
		if err != nil {
			return d, err
		}
		if err := onlyKeys(aes, "annotations", titleKey); err != nil {
			return d, err
		}
		d.Disp = lookup(aes, titleKey).Value
	}
	spec, err := vectors.Entries(lookup(root, "spec"))
	if err != nil {
		return d, err
	}
	if err := onlyKeys(spec, "spec", "entrypoint", "hooks", "templates"); err != nil {
		return d, err
	}
	if err := scalarIs(spec, "spec", "entrypoint", entryName); err != nil {
		return d, err
	}
	tmpls := map[string]vTmpl{}
	var entry, exit []vectors.Entry
	for _, c := range lookup(spec, "templates").Content {
		es, err := vectors.Entries(c)
		if err != nil {
			return d, err
		}
		name := lookup(es, "name").Value
		switch {
		case lookup(es, "dag") != nil && name == entryName:
			entry = es
		case lookup(es, "dag") != nil && name == exitName:
			exit = es
		default:
			t, err := decodeTemplate(es)
			if err != nil {
				return d, fmt.Errorf("template %s: %w", name, err)
			}
			tmpls[name] = t
		}
	}
	if entry == nil {
		return d, fmt.Errorf("no %s template", entryName)
	}
	used := map[string]bool{}
	if d.Tasks, err = decodeDAG(entry, tmpls, used, false); err != nil {
		return d, err
	}
	if exit != nil {
		if d.Finally, err = decodeDAG(exit, tmpls, used, true); err != nil {
			return d, err
		}
	}
	if (lookup(spec, "hooks") != nil) != (exit != nil) {
		return d, fmt.Errorf("the exit hook and the %s template disagree", exitName)
	}
	if len(used) != len(tmpls) {
		return d, fmt.Errorf("%d job templates, %d referenced", len(tmpls), len(used))
	}
	return d, applyMarks(&d, src)
}

// globOf turns the body of an anchored regex into the glob it came from: `\.` is a dot, `[^/]*` is a star and a
// trailing `.*` is a double star.
func globOf(body string) string {
	body = strings.ReplaceAll(strings.ReplaceAll(body, `\.`, "."), segmentStar, "*")
	if p, ok := strings.CutSuffix(body, anyTail); ok {
		return p + "**"
	}
	return body
}

func anchoredValues(n *yaml.Node, prefix string) ([]string, error) {
	vals, err := vectors.Strings(n)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, v := range vals {
		body, ok := strings.CutPrefix(v, prefix)
		if !ok || !strings.HasSuffix(body, "$") {
			return nil, fmt.Errorf("value %q is not anchored at %q", v, prefix)
		}
		out = append(out, globOf(strings.TrimSuffix(body, "$")))
	}
	return out, nil
}

// decodeDependency reads one Sensor dependency into a model trigger.
func decodeDependency(n *yaml.Node, pipeline string) (vTrig, error) {
	tr := vTrig{Include: []string{}}
	es, err := vectors.Entries(n)
	if err != nil {
		return tr, err
	}
	if err := onlyKeys(es, "dependency", "eventName", "eventSourceName", "filters", "name"); err != nil {
		return tr, err
	}
	name := lookup(es, "name").Value
	kind, ok := strings.CutPrefix(name, pipeline+"-")
	if !ok || (kind != kindPR && kind != kindPush && kind != kindTag) {
		return tr, fmt.Errorf("dependency %q is not %s-<kind>", name, pipeline)
	}
	tr.Kind = kind
	filters := lookup(es, "filters")
	if filters == nil {
		return tr, nil
	}
	fes, err := vectors.Entries(filters)
	if err != nil {
		return tr, err
	}
	if err := onlyKeys(fes, "filters", "data", "exprs"); err != nil {
		return tr, err
	}
	if ex := lookup(fes, "exprs"); ex != nil {
		if kind != kindPR || len(ex.Content) != 1 {
			return tr, fmt.Errorf("dependency %s: exprs are outside the model", name)
		}
		xes, err := vectors.Entries(ex.Content[0])
		if err != nil {
			return tr, err
		}
		if lookup(xes, "expr").Value != forkGate {
			return tr, fmt.Errorf("dependency %s: expr is not the fork gate", name)
		}
	}
	if data := lookup(fes, "data"); data != nil {
		for _, c := range data.Content {
			des, err := vectors.Entries(c)
			if err != nil {
				return tr, err
			}
			if err := onlyKeys(des, "data filter", "path", "type", "value"); err != nil {
				return tr, err
			}
			if err := decodeData(&tr, des); err != nil {
				return tr, fmt.Errorf("dependency %s: %w", name, err)
			}
		}
	}
	return tr, nil
}

func decodeData(tr *vTrig, des []vectors.Entry) error {
	val := lookup(des, "value")
	var err error
	switch path := lookup(des, "path").Value; {
	case path == pathAction && tr.Kind == kindPR:
		var got []string
		if got, err = vectors.Strings(val); err == nil && strings.Join(got, ",") != strings.Join(prActions, ",") {
			err = fmt.Errorf("pull request actions %v", got)
		}
	case path == pathBase && tr.Kind == kindPR:
		tr.Include, err = anchoredValues(val, "^")
	case path == pathRef && tr.Kind == kindPush:
		// A push beside a tag trigger carries the filter of every branch ref, which the model holds as no filter.
		if tr.Include, err = anchoredValues(val, refHeads); err == nil && len(tr.Include) == 1 && tr.Include[0] == "**" {
			tr.Include = []string{}
		}
	case path == pathRef && tr.Kind == kindTag:
		if tr.Include, err = anchoredValues(val, refTags); err == nil && len(tr.Include) == 1 && tr.Include[0] == "**" {
			tr.Include = []string{}
		}
	default:
		err = fmt.Errorf("data path %q is not modelled for %s", path, tr.Kind)
	}
	return err
}

// decodeSensor reads the Sensor dependencies in file order and checks that each has one trigger.
func decodeSensor(src []byte, pipeline string) ([]vTrig, error) {
	out := []vTrig{}
	root, err := vectors.RootMapping(src)
	if err != nil {
		return nil, err
	}
	spec, err := vectors.Entries(lookup(root, "spec"))
	if err != nil {
		return nil, err
	}
	deps := lookup(spec, "dependencies")
	trigs := lookup(spec, "triggers")
	if deps == nil || trigs == nil || len(deps.Content) != len(trigs.Content) {
		return nil, fmt.Errorf("sensor needs one trigger per dependency")
	}
	for i, c := range deps.Content {
		t, err := decodeDependency(c, pipeline)
		if err != nil {
			return nil, err
		}
		tes, err := vectors.Entries(trigs.Content[i])
		if err != nil {
			return nil, err
		}
		tmpl, err := vectors.Entries(lookup(tes, "template"))
		if err != nil {
			return nil, err
		}
		if got := lookup(tmpl, "name").Value; got != pipeline+"-"+t.Kind {
			return nil, fmt.Errorf("trigger %q for dependency %s-%s", got, pipeline, t.Kind)
		}
		out = append(out, t)
	}
	return out, nil
}

// decodeFiles reads the emitted files into the model's abstract document and checks the file set.
func decodeFiles(files map[string][]byte) (vDoc, error) {
	var wfs []string
	for name := range files {
		if strings.HasPrefix(name, workflowsDir) {
			wfs = append(wfs, name)
		}
	}
	if len(wfs) != wantDocsInSet {
		return vDoc{}, fmt.Errorf("%d workflow files, want %d", len(wfs), wantDocsInSet)
	}
	d, err := decodeWorkflow(files[wfs[0]])
	if err != nil {
		return d, fmt.Errorf("%s: %w", wfs[0], err)
	}
	if wfs[0] != workflowsDir+d.Name+".yaml" {
		return d, fmt.Errorf("%s holds the workflow %s", wfs[0], d.Name)
	}
	if src, ok := files[sensorFile]; ok {
		if d.Trigs, err = decodeSensor(src, d.Name); err != nil {
			return d, err
		}
	}
	want := map[string]bool{wfs[0]: true}
	if len(d.Trigs) > 0 {
		for _, f := range generatedFiles {
			want[f] = true
		}
	}
	var got []string
	for f := range files {
		got = append(got, f)
	}
	sort.Strings(got)
	for _, f := range got {
		if !want[f] {
			return d, fmt.Errorf("file %s is not modelled", f)
		}
		delete(want, f)
	}
	if len(want) > 0 {
		return d, fmt.Errorf("files missing: %v", want)
	}
	return d, nil
}

// TestArgoVectors checks the emitter and the parser against the Lean model: the emitted files decode to the
// model's abstract document, and parsing them returns the model's pipeline (theorem argo_from_to).
func TestArgoVectors(t *testing.T) {
	cases := vectors.Load(t, "argo")
	if len(cases) < vectors.MinAdapterCases {
		t.Fatalf("%d cases, want at least %d", len(cases), vectors.MinAdapterCases)
	}
	p := argo.New()
	for i, c := range cases {
		label := "case " + strconv.Itoa(i) + " " + string(c.In)
		proj, err := ir.FromCore(c.In)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		proj.Origin.Provider, proj.Origin.Dialect = vectorOrigin, vectorOrigin
		files, rep, err := p.Emit(proj)
		if err != nil {
			t.Fatalf("%s: emit: %v", label, err)
		}
		var out struct {
			Argo json.RawMessage `json:"argo"`
			Raw  json.RawMessage `json:"raw"`
			Core json.RawMessage `json:"core"`
		}
		if err := json.Unmarshal(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		got, err := decodeFiles(files)
		if err != nil {
			t.Fatalf("%s: decode: %v", label, err)
		}
		vectors.DiffAST(t, label, out.Argo, got)
		fsys := fstest.MapFS{}
		for name, data := range files {
			fsys[name] = &fstest.MapFile{Data: data}
		}
		back, _, err := p.Parse(fsys)
		if err != nil {
			t.Fatalf("%s: parse: %v", label, err)
		}
		rawCore, err := ir.CoreOf(back)
		if err != nil {
			t.Fatalf("%s: raw parse: %v", label, err)
		}
		vectors.DiffCore(t, label+" raw parse", out.Raw, rawCore)
		roundtrip.Restore(back, roundtrip.NewMeta(vectorOrigin, proj, p, rep))
		core, err := ir.CoreOf(back)
		if err != nil {
			t.Fatalf("%s: parsed project: %v", label, err)
		}
		vectors.DiffCore(t, label, out.Core, core)
		if t.Failed() {
			return
		}
	}
}
