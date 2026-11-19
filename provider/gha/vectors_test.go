package gha_test

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/internal/vectors"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
)

// The abstract workflow below mirrors lean4/Pipemaster/Gha.lean; its JSON keys are the ones GenVectors writes.
type (
	vNeed struct {
		Job       string `json:"job"`
		Artifacts bool   `json:"artifacts"`
	}
	vStep struct {
		Uses *string       `json:"uses"`
		ID   string        `json:"id"`
		Name string        `json:"name"`
		Run  []string      `json:"run"`
		Env  []vectors.Var `json:"env"`
	}
	vJob struct {
		Timeout         *int          `json:"timeout"`
		Cond            *string       `json:"cond"`
		Key             string        `json:"key"`
		Name            string        `json:"name"`
		Needs           []string      `json:"needs"`
		RunsOn          []string      `json:"runs_on"`
		Env             []vectors.Var `json:"env"`
		Steps           []vStep       `json:"steps"`
		ContinueOnError bool          `json:"continue_on_error"`
	}
	vPush struct {
		Branches *[]string `json:"branches"`
		Tags     *[]string `json:"tags"`
	}
	vOn struct {
		PR       *[]string `json:"pr"`
		Push     *vPush    `json:"push"`
		Dispatch bool      `json:"dispatch"`
	}
	vMark struct {
		Kind     string   `json:"kind"`
		Job      string   `json:"job"`
		Needs    []string `json:"needs"`
		NeedList []vNeed  `json:"need_list"`
		Idx      int      `json:"idx"`
		Attempts int      `json:"attempts"`
		Pre      int      `json:"pre"`
	}
	vWorkflow struct {
		On    vOn     `json:"on"`
		ID    string  `json:"id"`
		Name  string  `json:"name"`
		Jobs  []vJob  `json:"jobs"`
		Marks []vMark `json:"marks"`
	}
)

func decodeOn(n *yaml.Node) (vOn, error) {
	var on vOn
	es, err := vectors.Entries(n)
	if err != nil {
		return on, err
	}
	for _, e := range es {
		switch e.Key {
		case "pull_request":
			branches, err := branchList(e.Val, "branches")
			if err != nil {
				return on, err
			}
			on.PR = &branches
		case "push":
			p := &vPush{}
			if e.Val.Tag != "!!null" {
				pes, err := vectors.Entries(e.Val)
				if err != nil {
					return on, err
				}
				for _, pe := range pes {
					l, err := vectors.Strings(pe.Val)
					if err != nil {
						return on, err
					}
					switch pe.Key {
					case "branches":
						p.Branches = &l
					case "tags":
						p.Tags = &l
					default:
						return on, fmt.Errorf("push key %q is not modelled", pe.Key)
					}
				}
			}
			on.Push = p
		case "workflow_dispatch":
			on.Dispatch = true
		default:
			return on, fmt.Errorf("on key %q is not modelled", e.Key)
		}
	}
	return on, nil
}

func branchList(n *yaml.Node, key string) ([]string, error) {
	if n.Tag == "!!null" {
		return []string{}, nil
	}
	es, err := vectors.Entries(n)
	if err != nil {
		return nil, err
	}
	if len(es) == 0 {
		return []string{}, nil
	}
	if len(es) != 1 || es[0].Key != key {
		return nil, fmt.Errorf("line %d: want only %q", n.Line, key)
	}
	return vectors.Strings(es[0].Val)
}

func decodeStep(n *yaml.Node) (vStep, error) {
	var s vStep
	es, err := vectors.Entries(n)
	if err != nil {
		return s, err
	}
	for _, e := range es {
		switch e.Key {
		case "id":
			s.ID = e.Val.Value
		case "name":
			s.Name = e.Val.Value
		case "uses":
			u := e.Val.Value
			s.Uses = &u
		case "run":
			s.Run = strings.Split(strings.TrimSuffix(e.Val.Value, "\n"), "\n")
		case "env":
			if s.Env, err = vectors.Vars(e.Val); err != nil {
				return s, err
			}
		default:
			return s, fmt.Errorf("step key %q is not modelled", e.Key)
		}
	}
	return s, nil
}

func decodeJob(key string, n *yaml.Node) (vJob, error) {
	j := vJob{Key: key}
	es, err := vectors.Entries(n)
	if err != nil {
		return j, err
	}
	for _, e := range es {
		switch e.Key {
		case "name":
			j.Name = e.Val.Value
		case "needs":
			j.Needs, err = vectors.Strings(e.Val)
		case "if":
			c := e.Val.Value
			j.Cond = &c
		case "runs-on":
			j.RunsOn, err = vectors.Strings(e.Val)
		case "timeout-minutes":
			var m int
			if m, err = vectors.Int(e.Val); err == nil {
				j.Timeout = &m
			}
		case "continue-on-error":
			j.ContinueOnError, err = vectors.Bool(e.Val)
		case "env":
			j.Env, err = vectors.Vars(e.Val)
		case "steps":
			for _, sn := range e.Val.Content {
				var s vStep
				if s, err = decodeStep(sn); err != nil {
					break
				}
				j.Steps = append(j.Steps, s)
			}
		default:
			err = fmt.Errorf("job key %q is not modelled", e.Key)
		}
		if err != nil {
			return j, fmt.Errorf("job %s: %w", key, err)
		}
	}
	return j, nil
}

// decodeMarks maps each marker to the model's marker type; an unmodelled marker is an error.
func decodeMarks(src []byte, id string) ([]vMark, error) {
	out := []vMark{}
	root := "pipeline/" + id
	for _, m := range vectors.Marks(src) {
		job, isJob := strings.CutPrefix(m.Path, root+"/job/")
		var idx int
		if rest, ok := strings.CutPrefix(m.Path, root+"/trigger/"); ok {
			if _, err := fmt.Sscanf(rest, "%d", &idx); err != nil {
				return nil, fmt.Errorf("marker path %s: %w", m.Path, err)
			}
		}
		switch {
		case m.Feature == "trigger.push":
			out = append(out, vMark{Kind: "trig_push", Idx: idx})
		case m.Feature == "trigger.tag":
			out = append(out, vMark{Kind: "trig_tag", Idx: idx})
		case m.Feature == "trigger.manual" && m.Path == root:
			out = append(out, vMark{Kind: "trig_manual"})
		case m.Feature == "job.manual" && isJob:
			out = append(out, vMark{Kind: "job_manual", Job: job})
		case m.Feature == "job.retry" && isJob:
			var a struct{ Attempts int }
			if err := m.DecodeArg(&a); err != nil {
				return nil, err
			}
			out = append(out, vMark{Kind: "job_retry", Job: job, Attempts: a.Attempts})
		case m.Feature == "job.needs.artifacts" && isJob:
			var a struct {
				Needs     []string `json:"needs"`
				Pre, Post int
			}
			if err := m.DecodeArg(&a); err != nil {
				return nil, err
			}
			if a.Post != 0 {
				return nil, fmt.Errorf("marker %s: %d closing steps are not modelled", m.Path, a.Post)
			}
			out = append(out, vMark{Kind: "job_needs_art", Job: job, Needs: a.Needs, Pre: a.Pre})
		case m.Feature == "pipeline.finally" && isJob:
			var a struct {
				Needs []struct {
					JobID     string
					Artifacts bool
				}
			}
			if err := m.DecodeArg(&a); err != nil {
				return nil, err
			}
			fm := vMark{Kind: "finally", Job: job, NeedList: []vNeed{}}
			for _, n := range a.Needs {
				fm.NeedList = append(fm.NeedList, vNeed{Job: n.JobID, Artifacts: n.Artifacts})
			}
			out = append(out, fm)
		case m.Feature == "pipeline.finally" && m.Path == root:
			out = append(out, vMark{Kind: "finally_root"})
		default:
			return nil, fmt.Errorf("marker %s %s is not modelled", m.Feature, m.Path)
		}
	}
	return out, nil
}

func markKey(m vMark) string {
	return fmt.Sprintf("%s\x00%s\x00%d", m.Kind, m.Job, m.Idx)
}

// decodeWorkflow reads one emitted workflow file into the model's abstract shape.
func decodeWorkflow(file string, src []byte) (vWorkflow, error) {
	w := vWorkflow{ID: strings.TrimSuffix(path.Base(file), path.Ext(file))}
	es, err := vectors.RootMapping(src)
	if err != nil {
		return w, err
	}
	for _, e := range es {
		switch e.Key {
		case "name":
			w.Name = e.Val.Value
		case "on":
			if w.On, err = decodeOn(e.Val); err != nil {
				return w, err
			}
		case "jobs":
			jes, err := vectors.Entries(e.Val)
			if err != nil {
				return w, err
			}
			for _, je := range jes {
				j, err := decodeJob(je.Key, je.Val)
				if err != nil {
					return w, err
				}
				w.Jobs = append(w.Jobs, j)
			}
		default:
			return w, fmt.Errorf("workflow key %q is not modelled", e.Key)
		}
	}
	if w.Marks, err = decodeMarks(src, w.ID); err != nil {
		return w, err
	}
	sortMarks(w.Marks)
	return w, nil
}

func sortMarks(ms []vMark) {
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && markKey(ms[j]) < markKey(ms[j-1]); j-- {
			ms[j], ms[j-1] = ms[j-1], ms[j]
		}
	}
}

// TestGhaVectors checks the emitter and the parser against the Lean model: the emitted file decodes to the
// model's abstract workflow, and parsing it returns the model's pipeline (theorem gha_from_to).
func TestGhaVectors(t *testing.T) {
	cases := vectors.Load(t, "gha")
	if len(cases) < vectors.MinAdapterCases {
		t.Fatalf("%d cases, want at least %d", len(cases), vectors.MinAdapterCases)
	}
	p := gha.New(gha.GHA)
	for i, c := range cases {
		label := fmt.Sprintf("case %d %s", i, c.In)
		proj, err := ir.FromCore(c.In)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		files, _, err := p.Emit(proj)
		if err != nil || len(files) != 1 {
			t.Fatalf("%s: emit: %v (%d files)", label, err, len(files))
		}
		fsys := fstest.MapFS{}
		var out struct {
			Gha  json.RawMessage `json:"gha"`
			Core json.RawMessage `json:"core"`
		}
		if err := json.Unmarshal(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		for name, data := range files {
			fsys[name] = &fstest.MapFile{Data: data}
			w, err := decodeWorkflow(name, data)
			if err != nil {
				t.Fatalf("%s: decode %s: %v\n%s", label, name, err, data)
			}
			vectors.DiffAST(t, label, out.Gha, w)
		}
		back, _, err := p.Parse(fsys)
		if err != nil {
			t.Fatalf("%s: parse: %v", label, err)
		}
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
