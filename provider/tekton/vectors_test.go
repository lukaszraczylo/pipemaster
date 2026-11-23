package tekton_test

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
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
)

// The abstract documents below mirror lean4/Pipemaster/Tekton.lean; their JSON keys are the ones GenVectors writes.
type (
	vStep struct {
		Name   string        `json:"name"`
		Disp   string        `json:"disp"`
		Script []string      `json:"script"`
		Env    []vectors.Var `json:"env"`
	}
	vNeed struct {
		Job       string `json:"job"`
		Artifacts bool   `json:"artifacts"`
	}
	vTask struct {
		Needs *[]vNeed      `json:"needs"`
		Name  string        `json:"name"`
		Env   []vectors.Var `json:"env"`
		Steps []vStep       `json:"steps"`
		Noop  bool          `json:"noop"`
	}
	vPTask struct {
		Retries    *int     `json:"retries"`
		Name       string   `json:"name"`
		Disp       string   `json:"disp"`
		RunAfter   []string `json:"run_after"`
		Task       vTask    `json:"task"`
		Timeout    int      `json:"timeout"`
		OnError    bool     `json:"on_error"`
		FailedOnly bool     `json:"failed_only"`
	}
	vTrig struct {
		Kind    string   `json:"kind"`
		Include []string `json:"include"`
	}
	vDoc struct {
		Name    string   `json:"name"`
		Disp    string   `json:"disp"`
		Tasks   []vPTask `json:"tasks"`
		Finally []vPTask `json:"finally"`
		Trigs   []vTrig  `json:"trigs"`
	}
)

const (
	vectorOrigin    = "gha"
	pipelineFile    = ".tekton/pipeline.yaml"
	tasksPrefix     = ".tekton/tasks/"
	vecListenerFile = ".tekton/triggers/eventlistener.yaml"
	wantAPI         = "tekton.dev/v1"
	wantImage       = "alpine:3.22"
	statusInput     = "$(tasks.status)"
	statusFailed    = "Failed"
	kindPR          = "pr"
	kindPush        = "push"
	kindTag         = "tag"
	featPR          = "trigger.pull_request"
	featPush        = "trigger.push"
	featTag         = "trigger.tag"
	featImage       = "step.image"
	featNoop        = "step.script"
	featShell       = "step.shell"
	featNeeds       = "job.needs"
	minutes         = time.Minute
	triggerSegment  = "/trigger/"
)

var generatedFiles = []string{
	".tekton/kustomization.yaml", vecListenerFile, ".tekton/triggers/triggerbinding.yaml",
	".tekton/triggers/triggertemplate.yaml", ".tekton/rbac/triggers.yaml",
}

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

func decodeStep(n *yaml.Node) (vStep, error) {
	s := vStep{Script: []string{}, Env: []vectors.Var{}}
	es, err := vectors.Entries(n)
	if err != nil {
		return s, err
	}
	for _, e := range es {
		switch e.Key {
		case "name":
			s.Name = e.Val.Value
		case "displayName":
			s.Disp = e.Val.Value
		case "image":
			if e.Val.Value != wantImage {
				err = fmt.Errorf("image %q, want %q", e.Val.Value, wantImage)
			}
		case "script":
			s.Script = strings.Split(strings.TrimSuffix(e.Val.Value, "\n"), "\n")
			if len(s.Script) >= len(bashPreamble) && slices.Equal(s.Script[:len(bashPreamble)], bashPreamble) {
				s.Script = s.Script[len(bashPreamble):]
			}
		case "env":
			s.Env, err = decodeEnv(e.Val)
		default:
			err = fmt.Errorf("step key %q is not modelled", e.Key)
		}
		if err != nil {
			return s, fmt.Errorf("step %s: %w", s.Name, err)
		}
	}
	return s, nil
}

type needArg struct {
	Needs []struct {
		JobID     string
		Artifacts bool
	} `json:"needs"`
}

type imageArg struct {
	Image string `json:"image"`
}

// decodeTask reads one Task file and its markers.
func decodeTask(src []byte) (vTask, error) {
	t := vTask{Env: []vectors.Var{}, Steps: []vStep{}}
	root, err := vectors.RootMapping(src)
	if err != nil {
		return t, err
	}
	if err := onlyKeys(root, "task", "apiVersion", "kind", "metadata", "spec"); err != nil {
		return t, err
	}
	if v := lookup(root, "apiVersion"); v == nil || v.Value != wantAPI {
		return t, fmt.Errorf("task apiVersion is not %s", wantAPI)
	}
	if v := lookup(root, "kind"); v == nil || v.Value != "Task" {
		return t, fmt.Errorf("task kind is not Task")
	}
	meta, err := vectors.Entries(lookup(root, "metadata"))
	if err != nil {
		return t, err
	}
	if err := onlyKeys(meta, "task metadata", "name"); err != nil {
		return t, err
	}
	t.Name = lookup(meta, "name").Value
	spec, err := vectors.Entries(lookup(root, "spec"))
	if err != nil {
		return t, err
	}
	if err := onlyKeys(spec, "task spec", "stepTemplate", "steps"); err != nil {
		return t, err
	}
	if tpl := lookup(spec, "stepTemplate"); tpl != nil {
		es, err := vectors.Entries(tpl)
		if err != nil {
			return t, err
		}
		if err := onlyKeys(es, "stepTemplate", "env"); err != nil {
			return t, err
		}
		if t.Env, err = decodeEnv(lookup(es, "env")); err != nil {
			return t, err
		}
	}
	for _, c := range lookup(spec, "steps").Content {
		s, err := decodeStep(c)
		if err != nil {
			return t, err
		}
		t.Steps = append(t.Steps, s)
	}
	return t, applyTaskMarks(&t, src)
}

// applyTaskMarks reads the markers of a Task file: one default-image marker per step, the placeholder marker and
// the needs marker of a finally task. Any other marker is outside the model.
func applyTaskMarks(t *vTask, src []byte) error {
	images := map[string]bool{}
	for _, m := range vectors.Marks(src) {
		switch m.Feature {
		case featImage:
			var a imageArg
			if err := m.DecodeArg(&a); err != nil {
				return err
			}
			if a.Image != wantImage {
				return fmt.Errorf("image marker %q, want %q", a.Image, wantImage)
			}
			images[m.Path[strings.LastIndex(m.Path, "/step/")+len("/step/"):]] = true
		case featNoop:
			var a imageArg
			if err := m.DecodeArg(&a); err != nil {
				return err
			}
			t.Noop = true
		case featShell:
			// The note says a step from a bash-default source starts with the bash-if-present preamble, which the decoder strips.
		case featNeeds:
			var a needArg
			if err := m.DecodeArg(&a); err != nil {
				return err
			}
			needs := []vNeed{}
			for _, n := range a.Needs {
				needs = append(needs, vNeed{Job: n.JobID, Artifacts: n.Artifacts})
			}
			t.Needs = &needs
		default:
			return fmt.Errorf("marker %s %s is not modelled", m.Feature, m.Path)
		}
	}
	want := len(t.Steps)
	if t.Noop {
		want = 0
	}
	for _, s := range t.Steps {
		if !t.Noop && !images[s.Name] {
			return fmt.Errorf("step %s: no image marker", s.Name)
		}
	}
	if len(images) != want {
		return fmt.Errorf("%d image markers for %d steps", len(images), want)
	}
	return nil
}

func decodePTask(n *yaml.Node, tasks map[string]vTask, used map[string]bool, finally bool) (vPTask, error) {
	pt := vPTask{RunAfter: []string{}}
	es, err := vectors.Entries(n)
	if err != nil {
		return pt, err
	}
	for _, e := range es {
		switch e.Key {
		case "name":
			pt.Name = e.Val.Value
		case "displayName":
			pt.Disp = e.Val.Value
		case "taskRef":
			ref, err := vectors.Entries(e.Val)
			if err != nil {
				return pt, err
			}
			if err = onlyKeys(ref, "taskRef", "name"); err != nil {
				return pt, err
			}
			name := lookup(ref, "name").Value
			t, ok := tasks[name]
			if !ok {
				return pt, fmt.Errorf("taskRef %q names no Task file", name)
			}
			used[name] = true
			pt.Task = t
		case "runAfter":
			pt.RunAfter, err = vectors.Strings(e.Val)
		case "retries":
			var r int
			if r, err = vectors.Int(e.Val); err == nil {
				pt.Retries = &r
			}
		case "timeout":
			var d time.Duration
			if d, err = time.ParseDuration(e.Val.Value); err == nil {
				pt.Timeout = int(d / minutes)
				if d%minutes != 0 {
					err = fmt.Errorf("timeout %s is not whole minutes", e.Val.Value)
				}
			}
		case "onError":
			pt.OnError = e.Val.Value == "continue"
			if !pt.OnError {
				err = fmt.Errorf("onError %q is not modelled", e.Val.Value)
			}
		case "when":
			pt.FailedOnly, err = decodeStatusWhen(e.Val, finally)
		default:
			err = fmt.Errorf("task key %q is not modelled", e.Key)
		}
		if err != nil {
			return pt, fmt.Errorf("task %s: %w", pt.Name, err)
		}
	}
	return pt, nil
}

// decodeStatusWhen reads the one `when` entry the emitter writes for a finally task that runs after a failure.
func decodeStatusWhen(n *yaml.Node, finally bool) (bool, error) {
	if !finally || len(n.Content) != 1 {
		return false, fmt.Errorf("when is not the status entry of a finally task")
	}
	es, err := vectors.Entries(n.Content[0])
	if err != nil {
		return false, err
	}
	if err := onlyKeys(es, "when", "input", "operator", "values"); err != nil {
		return false, err
	}
	vals, err := vectors.Strings(lookup(es, "values"))
	if err != nil {
		return false, err
	}
	if lookup(es, "input").Value != statusInput || lookup(es, "operator").Value != "in" ||
		len(vals) != 1 || vals[0] != statusFailed {
		return false, fmt.Errorf("when entry is not %s in [%s]", statusInput, statusFailed)
	}
	return true, nil
}

func decodePipeline(src []byte, tasks map[string]vTask) (vDoc, error) {
	d := vDoc{Tasks: []vPTask{}, Finally: []vPTask{}, Trigs: []vTrig{}}
	root, err := vectors.RootMapping(src)
	if err != nil {
		return d, err
	}
	if err := onlyKeys(root, "pipeline", "apiVersion", "kind", "metadata", "spec"); err != nil {
		return d, err
	}
	if v := lookup(root, "apiVersion"); v == nil || v.Value != wantAPI {
		return d, fmt.Errorf("pipeline apiVersion is not %s", wantAPI)
	}
	if v := lookup(root, "kind"); v == nil || v.Value != "Pipeline" {
		return d, fmt.Errorf("pipeline kind is not Pipeline")
	}
	meta, err := vectors.Entries(lookup(root, "metadata"))
	if err != nil {
		return d, err
	}
	if err := onlyKeys(meta, "pipeline metadata", "name"); err != nil {
		return d, err
	}
	d.Name = lookup(meta, "name").Value
	spec, err := vectors.Entries(lookup(root, "spec"))
	if err != nil {
		return d, err
	}
	if err := onlyKeys(spec, "pipeline spec", "displayName", "tasks", "finally"); err != nil {
		return d, err
	}
	if v := lookup(spec, "displayName"); v != nil {
		d.Disp = v.Value
	}
	used := map[string]bool{}
	for _, part := range []struct {
		dst     *[]vPTask
		key     string
		finally bool
	}{{&d.Tasks, "tasks", false}, {&d.Finally, "finally", true}} {
		list := lookup(spec, part.key)
		if list == nil {
			continue
		}
		for _, c := range list.Content {
			pt, err := decodePTask(c, tasks, used, part.finally)
			if err != nil {
				return d, err
			}
			*part.dst = append(*part.dst, pt)
		}
	}
	if len(used) != len(tasks) {
		return d, fmt.Errorf("%d Task files, %d referenced", len(tasks), len(used))
	}
	return d, nil
}

type filterBody struct {
	Include []string
}

type trigArg struct {
	Kind     string     `json:"kind"`
	Name     string     `json:"name"`
	Branches filterBody `json:"branches"`
	Tags     filterBody `json:"tags"`
}

// decodeTriggers reads the filter markers of the EventListener in file order and checks the entry names.
func decodeTriggers(src []byte, pipeline string) ([]vTrig, error) {
	out := []vTrig{}
	for _, m := range vectors.Marks(src) {
		var kind string
		switch m.Feature {
		case featPR:
			kind = kindPR
		case featPush:
			kind = kindPush
		case featTag:
			kind = kindTag
		case "trigger.filter.branches", "trigger.filter.tags":
			continue
		default:
			return nil, fmt.Errorf("marker %s %s is not modelled", m.Feature, m.Path)
		}
		var a trigArg
		if err := m.DecodeArg(&a); err != nil {
			return nil, err
		}
		idx := m.Path[strings.LastIndex(m.Path, triggerSegment)+len(triggerSegment):]
		if a.Kind != kind || a.Name != pipeline+"-"+kind+"-"+idx {
			return nil, fmt.Errorf("marker %s %s: kind %q name %q", m.Feature, m.Path, a.Kind, a.Name)
		}
		inc := a.Branches.Include
		if kind == kindTag {
			inc = a.Tags.Include
		}
		out = append(out, vTrig{Kind: kind, Include: append([]string{}, inc...)})
	}
	return out, nil
}

// decodeFiles reads the emitted files into the model's abstract document and checks the file set.
func decodeFiles(files map[string][]byte) (vDoc, error) {
	tasks := map[string]vTask{}
	taskFiles := map[string]bool{}
	for name, src := range files {
		if !strings.HasPrefix(name, tasksPrefix) {
			continue
		}
		t, err := decodeTask(src)
		if err != nil {
			return vDoc{}, fmt.Errorf("%s: %w", name, err)
		}
		if name != tasksPrefix+t.Name+".yaml" {
			return vDoc{}, fmt.Errorf("%s holds the Task %s", name, t.Name)
		}
		tasks[t.Name] = t
		taskFiles[name] = true
	}
	pipe, ok := files[pipelineFile]
	if !ok {
		return vDoc{}, fmt.Errorf("no %s", pipelineFile)
	}
	d, err := decodePipeline(pipe, tasks)
	if err != nil {
		return d, err
	}
	if src, ok := files[vecListenerFile]; ok {
		if d.Trigs, err = decodeTriggers(src, d.Name); err != nil {
			return d, err
		}
	}
	want := map[string]bool{pipelineFile: true, generatedFiles[0]: true}
	if len(d.Trigs) > 0 {
		for _, f := range generatedFiles {
			want[f] = true
		}
	}
	for f := range taskFiles {
		want[f] = true
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

// TestTektonVectors checks the emitter and the parser against the Lean model: the emitted files decode to the
// model's abstract document, and parsing them returns the model's pipeline (theorem tekton_from_to).
func TestTektonVectors(t *testing.T) {
	cases := vectors.Load(t, "tekton")
	if len(cases) < vectors.MinAdapterCases {
		t.Fatalf("%d cases, want at least %d", len(cases), vectors.MinAdapterCases)
	}
	p := tekton.New()
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
			Tekton json.RawMessage `json:"tekton"`
			Raw    json.RawMessage `json:"raw"`
			Core   json.RawMessage `json:"core"`
		}
		if err := json.Unmarshal(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		got, err := decodeFiles(files)
		if err != nil {
			t.Fatalf("%s: decode: %v", label, err)
		}
		vectors.DiffAST(t, label, out.Tekton, got)
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
