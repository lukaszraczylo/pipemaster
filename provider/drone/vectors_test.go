package drone_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/internal/vectors"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/drone"
)

// The abstract documents below mirror lean4/Pipemaster/Drone.lean; their JSON keys are the ones GenVectors writes.
type (
	vTrigger struct {
		Events []string `json:"events"`
		Branch []string `json:"branch"`
		Ref    []string `json:"ref"`
		Status []string `json:"status"`
	}
	vStep struct {
		Name   string        `json:"name"`
		Cmds   []string      `json:"cmds"`
		Env    []vectors.Var `json:"env"`
		Ignore bool          `json:"ignore"`
	}
	vTrig struct {
		Kind    string   `json:"kind"`
		Include []string `json:"include"`
	}
	vNeed struct {
		Job       string `json:"job"`
		Artifacts bool   `json:"artifacts"`
	}
	vFinally struct {
		Needs []vNeed `json:"needs"`
		NoIf  bool    `json:"no_if"`
	}
	vDoc struct {
		Trigger      *vTrigger     `json:"trigger"`
		Manual       *[]vTrig      `json:"manual"`
		Finally      *vFinally     `json:"finally"`
		IfTrigs      *[]vTrig      `json:"if_trigs"`
		Platform     *string       `json:"platform"`
		Name         string        `json:"name"`
		Deps         []string      `json:"deps"`
		Steps        []vStep       `json:"steps"`
		Env          []vectors.Var `json:"env"`
		Noop         bool          `json:"noop"`
		AllowFailure bool          `json:"allow_failure"`
	}
)

const (
	docSeparator = "---"
	tagRefPrefix = "refs/tags/"
	wantKind     = "pipeline"
	wantType     = "docker"
	wantImage    = "ubuntu:latest"
	// vectorOrigin is the provider the vector projects claim to come from; it only names the metadata layer.
	vectorOrigin = "gha"
)

// unescape reverses the emitter's `$` to `$$` escape.
func unescape(s string) string { return strings.ReplaceAll(s, "$$", "$") }

func unescapeAll(ss []string) []string {
	for i := range ss {
		ss[i] = unescape(ss[i])
	}
	return ss
}

func unescapeVars(vs []vectors.Var) []vectors.Var {
	for i := range vs {
		vs[i].Value = unescape(vs[i].Value)
	}
	return vs
}

// splitDocs cuts a multi-document file at the lines that hold only the separator.
func splitDocs(src []byte) [][]byte {
	var docs [][]byte
	var cur []byte
	for _, line := range bytes.SplitAfter(src, []byte("\n")) {
		if string(bytes.TrimRight(line, "\r\n")) == docSeparator {
			docs = append(docs, cur)
			cur = nil
			continue
		}
		cur = append(cur, line...)
	}
	return append(docs, cur)
}

// decodeFilter reads a condition value: a list, or a mapping with an include list only.
func decodeFilter(n *yaml.Node) ([]string, error) {
	if n.Kind != yaml.MappingNode {
		return vectors.Strings(n)
	}
	es, err := vectors.Entries(n)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, kv := range es {
		if kv.Key != "include" {
			return nil, fmt.Errorf("filter key %q is not modelled", kv.Key)
		}
		if out, err = vectors.Strings(kv.Val); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func decodeTrigger(n *yaml.Node) (*vTrigger, error) {
	es, err := vectors.Entries(n)
	if err != nil {
		return nil, err
	}
	t := &vTrigger{}
	for _, kv := range es {
		var vals []string
		if vals, err = decodeFilter(kv.Val); err != nil {
			return nil, fmt.Errorf("trigger %s: %w", kv.Key, err)
		}
		vals = unescapeAll(vals)
		switch kv.Key {
		case "event":
			t.Events = vals
		case "branch":
			t.Branch = vals
		case "ref":
			for _, r := range vals {
				t.Ref = append(t.Ref, strings.TrimPrefix(r, tagRefPrefix))
			}
		case "status":
			t.Status = vals
		default:
			return nil, fmt.Errorf("trigger key %q is not modelled", kv.Key)
		}
	}
	return t, nil
}

func decodeStep(n *yaml.Node) (vStep, error) {
	var s vStep
	es, err := vectors.Entries(n)
	if err != nil {
		return s, err
	}
	for _, kv := range es {
		switch kv.Key {
		case "name":
			s.Name = kv.Val.Value
		case "image":
			if kv.Val.Value != wantImage {
				err = fmt.Errorf("image %q", kv.Val.Value)
			}
		case "commands":
			if s.Cmds, err = vectors.Strings(kv.Val); err == nil {
				s.Cmds = unescapeAll(s.Cmds)
			}
		case "environment":
			var vs []vectors.Var
			if vs, err = vectors.Vars(kv.Val); err == nil {
				s.Env = unescapeVars(vs)
			}
		case "failure":
			s.Ignore = kv.Val.Value == "ignore"
			if !s.Ignore {
				err = fmt.Errorf("failure %q is not modelled", kv.Val.Value)
			}
		default:
			err = fmt.Errorf("step key %q is not modelled", kv.Key)
		}
		if err != nil {
			return s, fmt.Errorf("step %s: %w", s.Name, err)
		}
	}
	return s, nil
}

// decodePlatform rebuilds the os/arch/variant label.
func decodePlatform(n *yaml.Node) (*string, error) {
	es, err := vectors.Entries(n)
	if err != nil {
		return nil, err
	}
	parts := map[string]string{}
	for _, kv := range es {
		parts[kv.Key] = kv.Val.Value
	}
	label := parts["os"] + "/" + parts["arch"]
	if v, ok := parts["variant"]; ok {
		label += "/" + v
	}
	return &label, nil
}

// yamlArg reads the marker argument that holds a YAML document as a JSON string.
func yamlArg(m vectors.Mark, v any) error {
	var text string
	if err := m.DecodeArg(&text); err != nil {
		return err
	}
	return yaml.Unmarshal([]byte(text), v)
}

type (
	manualItem struct {
		Kind     string `yaml:"kind"`
		Branches struct {
			Include []string `yaml:"include"`
		} `yaml:"branches"`
		Tags struct {
			Include []string `yaml:"include"`
		} `yaml:"tags"`
	}
	finallyArg struct {
		Needs []struct {
			JobID     string
			Artifacts bool
		} `json:"needs"`
		NoIf bool `json:"noIf"`
	}
	ifArg struct {
		Triggers string `json:"triggers"`
	}
)

func decodeManual(items []manualItem) *[]vTrig {
	trigs := []vTrig{}
	for _, it := range items {
		inc := it.Branches.Include
		if it.Kind == "tag" {
			inc = it.Tags.Include
		}
		trigs = append(trigs, vTrig{Kind: it.Kind, Include: append([]string{}, inc...)})
	}
	return &trigs
}

func applyMark(d *vDoc, m vectors.Mark) error {
	switch m.Feature {
	case "job.container":
	case "job.allow_failure":
		d.AllowFailure = true
	case "step.script":
		d.Noop = true
	case "trigger.filter.tags":
	case "job.manual":
		var items []manualItem
		if err := yamlArg(m, &items); err != nil {
			return err
		}
		d.Manual = decodeManual(items)
	case "job.if":
		var a ifArg
		if err := m.DecodeArg(&a); err != nil {
			return err
		}
		var items []manualItem
		if err := yaml.Unmarshal([]byte(a.Triggers), &items); err != nil {
			return err
		}
		d.IfTrigs = decodeManual(items)
	case "pipeline.finally":
		var a finallyArg
		if err := m.DecodeArg(&a); err != nil {
			return err
		}
		fin := vFinally{Needs: []vNeed{}, NoIf: a.NoIf}
		for _, n := range a.Needs {
			fin.Needs = append(fin.Needs, vNeed{Job: n.JobID, Artifacts: n.Artifacts})
		}
		d.Finally = &fin
	default:
		return fmt.Errorf("marker %s %s is not modelled", m.Feature, m.Path)
	}
	return nil
}

func checkHeader(key string, val *yaml.Node) error {
	want := map[string]string{"kind": wantKind, "type": wantType}[key]
	if val.Value != want {
		return fmt.Errorf("%s %q, want %q", key, val.Value, want)
	}
	return nil
}

func decodeDoc(src []byte) (vDoc, error) {
	d := vDoc{Deps: []string{}, Steps: []vStep{}, Env: []vectors.Var{}}
	es, err := vectors.RootMapping(src)
	if err != nil {
		return d, err
	}
	for _, e := range es {
		switch e.Key {
		case "kind", "type":
			err = checkHeader(e.Key, e.Val)
		case "name":
			d.Name = e.Val.Value
		case "trigger":
			d.Trigger, err = decodeTrigger(e.Val)
		case "depends_on":
			d.Deps, err = vectors.Strings(e.Val)
		case "platform":
			d.Platform, err = decodePlatform(e.Val)
		case "environment":
			var vs []vectors.Var
			if vs, err = vectors.Vars(e.Val); err == nil {
				d.Env = unescapeVars(vs)
			}
		case "steps":
			d.Steps = []vStep{}
			for _, c := range e.Val.Content {
				var s vStep
				if s, err = decodeStep(c); err != nil {
					break
				}
				d.Steps = append(d.Steps, s)
			}
		default:
			err = fmt.Errorf("key %q is not modelled", e.Key)
		}
		if err != nil {
			return d, err
		}
	}
	for _, m := range vectors.Marks(src) {
		if err := applyMark(&d, m); err != nil {
			return d, err
		}
	}
	return d, nil
}

// TestDroneVectors checks the emitter and the parser against the Lean model: the emitted documents decode to the
// model's abstract documents, and parsing them returns the model's pipeline (theorem drone_from_to).
func TestDroneVectors(t *testing.T) {
	cases := vectors.Load(t, "drone")
	if len(cases) < vectors.MinAdapterCases {
		t.Fatalf("%d cases, want at least %d", len(cases), vectors.MinAdapterCases)
	}
	p := drone.New()
	for i, c := range cases {
		label := fmt.Sprintf("case %d %s", i, c.In)
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
			Drone []vDoc          `json:"drone"`
			Raw   json.RawMessage `json:"raw"`
			Core  json.RawMessage `json:"core"`
		}
		if err := json.Unmarshal(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		fsys := fstest.MapFS{}
		var got []vDoc
		for name, data := range files {
			fsys[name] = &fstest.MapFile{Data: data}
			for _, seg := range splitDocs(data) {
				d, err := decodeDoc(seg)
				if err != nil {
					t.Fatalf("%s: decode %s: %v\n%s", label, name, err, seg)
				}
				got = append(got, d)
			}
		}
		if len(got) != len(out.Drone) {
			t.Fatalf("%s: %d documents, model has %d", label, len(got), len(out.Drone))
		}
		for k, want := range out.Drone {
			raw, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			vectors.DiffAST(t, fmt.Sprintf("%s doc %d %s", label, k, want.Name), raw, got[k])
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
