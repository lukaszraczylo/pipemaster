package concourse_test

import (
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/internal/vectors"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/concourse"
)

// The abstract documents below mirror lean4/Pipemaster/Concourse.lean; their JSON keys are the ones GenVectors writes.
type (
	vTask struct {
		Name    string        `json:"name"`
		CID     string        `json:"cid"`
		CName   string        `json:"cname"`
		Script  []string      `json:"script"`
		Carried []string      `json:"carried"`
		CEnv    []vectors.Var `json:"cenv"`
	}
	vJob struct {
		Name   string     `json:"name"`
		Disp   string     `json:"disp"`
		Passed []string   `json:"passed"`
		Body   []vTask    `json:"body"`
		Hook   []vTask    `json:"hook"`
		Carry  ir.CoreJob `json:"carry"`
	}
	vDoc struct {
		File     string          `json:"file"`
		FinJob   string          `json:"fin_job"`
		Jobs     []vJob          `json:"jobs"`
		FinTasks []string        `json:"fin_tasks"`
		Carry    ir.CorePipeline `json:"carry"`
	}
)

const (
	vectorOrigin = "gha"
	pipelineDir  = ".concourse/"
	pipelineExt  = ".yml"
	featCarry    = "ext.passthrough"
	featName     = "display.name"
	featFinally  = "pipeline.finally"
	carryTail    = "/carry"
	kindJob      = "job"
	stepSegment  = "/step/"
	jobSegment   = "/job/"
)

var hookKeys = []string{"on_success", "on_failure", "ensure"}

type (
	carryArg struct {
		Digest string `json:"digest"`
		Carry  string `json:"carry"`
	}
	nameArg struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	finallyArg struct {
		Job   string   `json:"job"`
		Tasks []string `json:"tasks"`
	}
)

func scalar(es []vectors.Entry, key string) *yaml.Node {
	for _, e := range es {
		if e.Key == key {
			return e.Val
		}
	}
	return nil
}

// collect lists the step nodes that hold key, looking through the do and try wrappers.
func collect(n *yaml.Node, key string, out *[]*yaml.Node) error {
	if n.Kind == yaml.SequenceNode {
		for _, c := range n.Content {
			if err := collect(c, key, out); err != nil {
				return err
			}
		}
		return nil
	}
	es, err := vectors.Entries(n)
	if err != nil {
		return err
	}
	if v := scalar(es, key); v != nil {
		*out = append(*out, n)
		return nil
	}
	for _, k := range []string{"do", "try"} {
		if v := scalar(es, k); v != nil {
			return collect(v, key, out)
		}
	}
	return nil
}

func taskScript(es []vectors.Entry) ([]string, error) {
	cfg := scalar(es, "config")
	if cfg == nil {
		return nil, fmt.Errorf("task has no config")
	}
	ces, err := vectors.Entries(cfg)
	if err != nil {
		return nil, err
	}
	run := scalar(ces, "run")
	if run == nil {
		return nil, fmt.Errorf("task has no run")
	}
	res, err := vectors.Entries(run)
	if err != nil {
		return nil, err
	}
	args := scalar(res, "args")
	if args == nil {
		return nil, fmt.Errorf("task has no args")
	}
	as, err := vectors.Strings(args)
	if err != nil || len(as) == 0 {
		return nil, fmt.Errorf("task args: %v", err)
	}
	return strings.Split(strings.TrimSuffix(as[len(as)-1], "\n"), "\n"), nil
}

func coreOfJob(j *ir.Job) (ir.CoreJob, error) {
	cp, err := ir.CoreOf(&ir.Project{Pipelines: []*ir.Pipeline{{ID: "x", Jobs: []*ir.Job{j}}}})
	if err != nil {
		return ir.CoreJob{}, err
	}
	return cp.Jobs[0], nil
}

type carries map[string]string

func decodeTask(n *yaml.Node, cs carries, jobPath string) (vTask, error) {
	es, err := vectors.Entries(n)
	if err != nil {
		return vTask{}, err
	}
	t := vTask{Name: scalar(es, "task").Value, CEnv: []vectors.Var{}}
	if t.Script, err = taskScript(es); err != nil {
		return t, err
	}
	body, ok := cs[jobPath+stepSegment+t.Name]
	if !ok {
		return t, fmt.Errorf("task %s has no carry", t.Name)
	}
	var st ir.Step
	if err := yaml.Unmarshal([]byte(body), &st); err != nil {
		return t, err
	}
	st.Ext = nil
	cj, err := coreOfJob(&ir.Job{ID: "x", Steps: []*ir.Step{&st}})
	if err != nil {
		return t, err
	}
	cst := cj.Steps[0]
	t.CID, t.CName, t.Carried = cst.ID, cst.Name, cst.Script
	for _, v := range cst.Env {
		t.CEnv = append(t.CEnv, vectors.Var{Name: v.Name, Value: v.Value})
	}
	return t, nil
}

func decodeTasks(n *yaml.Node, cs carries, jobPath string) ([]vTask, error) {
	out := []vTask{}
	if n == nil {
		return out, nil
	}
	var ns []*yaml.Node
	if err := collect(n, "task", &ns); err != nil {
		return nil, err
	}
	for _, tn := range ns {
		t, err := decodeTask(tn, cs, jobPath)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func decodeFile(name string, src []byte) (vDoc, error) {
	d := vDoc{File: strings.TrimSuffix(path.Base(name), pipelineExt), Jobs: []vJob{}, FinTasks: []string{}}
	pipe := "pipeline/" + d.File
	cs, disp := carries{}, map[string]string{}
	twins := map[string]bool{} // the tag twin of a job is outside the model; the parser folds it into its job
	for _, m := range vectors.Marks(src) {
		switch {
		case m.Feature == string(caps.TriggerTag) && len(m.Args) > 0:
			twins[m.Path] = true
		case m.Feature == featCarry && strings.HasSuffix(m.Path, carryTail):
			var a carryArg
			if err := m.DecodeArg(&a); err != nil {
				return d, err
			}
			cs[strings.TrimSuffix(m.Path, carryTail)] = a.Carry
		case m.Feature == featName:
			var a nameArg
			if err := m.DecodeArg(&a); err != nil {
				return d, err
			}
			if a.Kind == kindJob {
				disp[m.Path] = a.Name
			}
		case m.Feature == featFinally && len(m.Args) > 0:
			var a finallyArg
			if err := m.DecodeArg(&a); err != nil {
				return d, err
			}
			d.FinJob, d.FinTasks = a.Job, a.Tasks
		}
	}
	body, ok := cs[pipe]
	if !ok {
		return d, fmt.Errorf("pipeline has no carry")
	}
	var pl ir.Pipeline
	if err := yaml.Unmarshal([]byte(body), &pl); err != nil {
		return d, err
	}
	cp, err := ir.CoreOf(&ir.Project{Pipelines: []*ir.Pipeline{&pl}})
	if err != nil {
		return d, err
	}
	d.Carry = *cp
	root, err := vectors.RootMapping(src)
	if err != nil {
		return d, err
	}
	jobs := scalar(root, "jobs")
	if jobs == nil {
		return d, fmt.Errorf("file has no jobs")
	}
	for _, jn := range jobs.Content {
		es, err := vectors.Entries(jn)
		if err != nil {
			return d, err
		}
		j := vJob{Name: scalar(es, "name").Value, Passed: []string{}}
		jp := pipe + jobSegment + j.Name
		if twins[jp] {
			continue
		}
		j.Disp = disp[jp]
		cb, ok := cs[jp]
		if !ok {
			return d, fmt.Errorf("job %s has no carry", j.Name)
		}
		var cjob ir.Job
		if err := yaml.Unmarshal([]byte(cb), &cjob); err != nil {
			return d, err
		}
		cjob.Steps, cjob.Ext = nil, nil
		if j.Carry, err = coreOfJob(&cjob); err != nil {
			return d, err
		}
		plan := scalar(es, "plan")
		if plan == nil {
			return d, fmt.Errorf("job %s has no plan", j.Name)
		}
		var gets []*yaml.Node
		if err := collect(plan, "get", &gets); err != nil {
			return d, err
		}
		for _, g := range gets {
			ges, err := vectors.Entries(g)
			if err != nil {
				return d, err
			}
			if p := scalar(ges, "passed"); p != nil && len(j.Passed) == 0 {
				if j.Passed, err = vectors.Strings(p); err != nil {
					return d, err
				}
			}
		}
		if j.Body, err = decodeTasks(plan, cs, jp); err != nil {
			return d, err
		}
		j.Hook = []vTask{}
		for _, h := range hookKeys {
			ts, err := decodeTasks(scalar(es, h), cs, jp)
			if err != nil {
				return d, err
			}
			j.Hook = append(j.Hook, ts...)
		}
		d.Jobs = append(d.Jobs, j)
	}
	return d, nil
}

func decodeFiles(files map[string][]byte) (vDoc, error) {
	var found []string
	for n := range files {
		if strings.HasPrefix(n, pipelineDir) && strings.HasSuffix(n, pipelineExt) {
			found = append(found, n)
		}
	}
	if len(found) != 1 {
		return vDoc{}, fmt.Errorf("want one pipeline file, have %v", found)
	}
	return decodeFile(found[0], files[found[0]])
}

func TestConcourseVectors(t *testing.T) {
	cases := vectors.Load(t, "concourse")
	if len(cases) < vectors.MinAdapterCases {
		t.Fatalf("%d cases, want at least %d", len(cases), vectors.MinAdapterCases)
	}
	p := concourse.New()
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
			Concourse json.RawMessage `json:"concourse"`
			Raw       json.RawMessage `json:"raw"`
			Core      json.RawMessage `json:"core"`
		}
		if err := json.Unmarshal(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		got, err := decodeFiles(files)
		if err != nil {
			t.Fatalf("%s: decode: %v", label, err)
		}
		vectors.DiffAST(t, label, out.Concourse, got)
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
