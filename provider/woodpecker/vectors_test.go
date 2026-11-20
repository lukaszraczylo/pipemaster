package woodpecker_test

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/internal/vectors"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/woodpecker"
)

// The abstract files below mirror lean4/Pipemaster/Woodpecker.lean; their JSON keys are the ones GenVectors writes.
type (
	vEntry struct {
		Events []string `json:"events"`
		Branch []string `json:"branch"`
		Ref    []string `json:"ref"`
		Status []string `json:"status"`
	}
	vDep struct {
		Name     string `json:"name"`
		Optional bool   `json:"optional"`
	}
	vStep struct {
		Name   string        `json:"name"`
		Cmds   []string      `json:"cmds"`
		Env    []vectors.Var `json:"env"`
		Shadow []string      `json:"shadow"`
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
	vFile struct {
		Manual       *[]vTrig      `json:"manual"`
		Finally      *vFinally     `json:"finally"`
		TagIdx       *int          `json:"tag_idx"`
		Name         string        `json:"name"`
		Whens        []vEntry      `json:"whens"`
		Deps         []vDep        `json:"deps"`
		Steps        []vStep       `json:"steps"`
		JobEnv       []vectors.Var `json:"job_env"`
		Platform     *string       `json:"platform"`
		Noop         bool          `json:"noop"`
		AllowFailure bool          `json:"allow_failure"`
	}
)

const (
	workflowDir  = ".woodpecker/"
	workflowExt  = ".yaml"
	tagRefPrefix = "refs/tags/"
	stepPathMid  = "/step/"
	triggerPath  = "/trigger/"
	// vectorOrigin is the provider the vector projects claim to come from; it only names the metadata layer.
	vectorOrigin = "gha"
)

// unescape reverses the emitter's `$` to `$$` escape, which the model leaves out.
func unescape(s string) string { return strings.ReplaceAll(s, "$$", "$") }

func unescapeVars(vs []vectors.Var) []vectors.Var {
	for i := range vs {
		vs[i].Value = unescape(vs[i].Value)
	}
	return vs
}

func decodeEntry(n *yaml.Node) (vEntry, error) {
	var e vEntry
	es, err := vectors.Entries(n)
	if err != nil {
		return e, err
	}
	for _, kv := range es {
		switch kv.Key {
		case "event":
			e.Events, err = vectors.Strings(kv.Val)
		case "branch":
			e.Branch, err = vectors.Strings(kv.Val)
		case "ref":
			var refs []string
			if refs, err = vectors.Strings(kv.Val); err == nil {
				for _, r := range refs {
					e.Ref = append(e.Ref, strings.TrimPrefix(r, tagRefPrefix))
				}
			}
		case "status":
			e.Status, err = vectors.Strings(kv.Val)
		default:
			err = fmt.Errorf("when key %q is not modelled", kv.Key)
		}
		if err != nil {
			return e, err
		}
	}
	return e, nil
}

func decodeDep(n *yaml.Node) (vDep, error) {
	if n.Kind == yaml.ScalarNode {
		return vDep{Name: n.Value}, nil
	}
	es, err := vectors.Entries(n)
	if err != nil {
		return vDep{}, err
	}
	var d vDep
	for _, kv := range es {
		switch kv.Key {
		case "name":
			d.Name = kv.Val.Value
		case "optional":
			if d.Optional, err = vectors.Bool(kv.Val); err != nil {
				return d, err
			}
		default:
			return d, fmt.Errorf("depends_on key %q is not modelled", kv.Key)
		}
	}
	return d, nil
}

func decodeStep(n *yaml.Node) (vStep, error) {
	s := vStep{Shadow: []string{}}
	es, err := vectors.Entries(n)
	if err != nil {
		return s, err
	}
	for _, kv := range es {
		switch kv.Key {
		case "name":
			s.Name = kv.Val.Value
		case "image":
		case "commands":
			s.Cmds, err = vectors.Strings(kv.Val)
			for i := range s.Cmds {
				s.Cmds[i] = unescape(s.Cmds[i])
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

// decodeLabels reads the labels.platform key; any other label key is not modelled.
func decodeLabels(n *yaml.Node) (*string, error) {
	es, err := vectors.Entries(n)
	if err != nil {
		return nil, err
	}
	var platform *string
	for _, kv := range es {
		if kv.Key != "platform" {
			return nil, fmt.Errorf("label key %q is not modelled", kv.Key)
		}
		v := kv.Val.Value
		platform = &v
	}
	return platform, nil
}

func decodeSeq[T any](n *yaml.Node, one func(*yaml.Node) (T, error)) ([]T, error) {
	out := []T{}
	for _, c := range n.Content {
		v, err := one(c)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
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
	envItem struct {
		Name  string `yaml:"name"`
		Value struct {
			Lit string `yaml:"lit"`
		} `yaml:"value"`
	}
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
	shadowArg struct {
		Names []string `json:"names"`
	}
)

func applyMark(f *vFile, m vectors.Mark) error {
	switch m.Feature {
	case "job.container":
	case "job.allow_failure":
		f.AllowFailure = true
	case "step.script":
		f.Noop = true
	case "job.env":
		var items []envItem
		if err := yamlArg(m, &items); err != nil {
			return err
		}
		f.JobEnv = []vectors.Var{}
		for _, it := range items {
			f.JobEnv = append(f.JobEnv, vectors.Var{Name: it.Name, Value: unescape(it.Value.Lit)})
		}
	case "job.manual":
		var items []manualItem
		if err := yamlArg(m, &items); err != nil {
			return err
		}
		trigs := []vTrig{}
		for _, it := range items {
			inc := it.Branches.Include
			if it.Kind == "tag" {
				inc = it.Tags.Include
			}
			trigs = append(trigs, vTrig{Kind: it.Kind, Include: append([]string{}, inc...)})
		}
		f.Manual = &trigs
	case "pipeline.finally":
		var a finallyArg
		if err := m.DecodeArg(&a); err != nil {
			return err
		}
		fin := vFinally{Needs: []vNeed{}, NoIf: a.NoIf}
		for _, n := range a.Needs {
			fin.Needs = append(fin.Needs, vNeed{Job: n.JobID, Artifacts: n.Artifacts})
		}
		f.Finally = &fin
	case "trigger.filter.tags":
		_, tail, ok := strings.Cut(m.Path, triggerPath)
		if !ok {
			return fmt.Errorf("trigger path %q", m.Path)
		}
		idx, err := strconv.Atoi(tail)
		if err != nil {
			return err
		}
		f.TagIdx = &idx
	case "step.env":
		var a shadowArg
		if err := m.DecodeArg(&a); err != nil {
			return err
		}
		_, sid, ok := strings.Cut(m.Path, stepPathMid)
		if !ok {
			return fmt.Errorf("step.env path %q", m.Path)
		}
		for i := range f.Steps {
			if f.Steps[i].Name == sid {
				f.Steps[i].Shadow = a.Names
				return nil
			}
		}
		return fmt.Errorf("step.env names no step: %q", m.Path)
	default:
		return fmt.Errorf("marker %s %s is not modelled", m.Feature, m.Path)
	}
	return nil
}

func decodeFile(name string, src []byte) (vFile, error) {
	f := vFile{Name: strings.TrimSuffix(strings.TrimPrefix(name, workflowDir), workflowExt)}
	es, err := vectors.RootMapping(src)
	if err != nil {
		return f, err
	}
	for _, e := range es {
		switch e.Key {
		case "when":
			f.Whens, err = decodeSeq(e.Val, decodeEntry)
		case "depends_on":
			f.Deps, err = decodeSeq(e.Val, decodeDep)
		case "steps":
			f.Steps, err = decodeSeq(e.Val, decodeStep)
		case "labels":
			f.Platform, err = decodeLabels(e.Val)
		default:
			err = fmt.Errorf("key %q is not modelled", e.Key)
		}
		if err != nil {
			return f, err
		}
	}
	for _, m := range vectors.Marks(src) {
		if err := applyMark(&f, m); err != nil {
			return f, err
		}
	}
	return f, nil
}

// TestWoodpeckerVectors checks the emitter and the parser against the Lean model: the emitted files decode to the
// model's abstract files, and parsing them returns the model's pipeline (theorem woodpecker_from_to).
func TestWoodpeckerVectors(t *testing.T) {
	cases := vectors.Load(t, "woodpecker")
	if len(cases) < vectors.MinAdapterCases {
		t.Fatalf("%d cases, want at least %d", len(cases), vectors.MinAdapterCases)
	}
	p := woodpecker.New()
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
			Woodpecker []vFile         `json:"woodpecker"`
			Raw        json.RawMessage `json:"raw"`
			Core       json.RawMessage `json:"core"`
		}
		if err := json.Unmarshal(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		fsys := fstest.MapFS{}
		got := map[string]vFile{}
		for name, data := range files {
			fsys[name] = &fstest.MapFile{Data: data}
			f, err := decodeFile(name, data)
			if err != nil {
				t.Fatalf("%s: decode %s: %v\n%s", label, name, err, data)
			}
			got[f.Name] = f
		}
		if len(got) != len(out.Woodpecker) {
			names := make([]string, 0, len(got))
			for n := range got {
				names = append(names, n)
			}
			sort.Strings(names)
			t.Fatalf("%s: %d files %v, model has %d", label, len(got), names, len(out.Woodpecker))
		}
		for _, want := range out.Woodpecker {
			g, ok := got[want.Name]
			if !ok {
				t.Fatalf("%s: model file %q is not emitted", label, want.Name)
			}
			raw, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			vectors.DiffAST(t, label+" "+want.Name, raw, g)
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
