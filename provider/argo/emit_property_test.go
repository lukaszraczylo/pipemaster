package argo_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/ir/irtest"
	"github.com/lukaszraczylo/pipemaster/provider/argo"
)

const propTimeout = 90 * time.Second

// nativeFragment lists the features that an Argo file states without expressions: no image, no action, no condition.
func nativeFragment(f caps.Feature) bool {
	switch f {
	case caps.StepScript, caps.StepEnv, caps.StepContinueOnError, caps.StepWorkdir,
		caps.JobNeeds, caps.JobMatrix, caps.JobServices, caps.JobTimeout, caps.JobRetry, caps.JobAllowFailure, caps.JobOutputs,
		caps.TriggerPush, caps.TriggerPullRequest, caps.TriggerTag, caps.TriggerFilterBranch, caps.TriggerFilterTags:
		return true
	}
	return false
}

// word keeps letters, digits and underscores behind a letter, so the text is a valid name and a safe scalar.
func word(s string) string {
	var sb strings.Builder
	sb.WriteByte('w')
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func words(in []string) []string {
	var out []string
	for _, s := range in {
		if w := word(s); !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

func nativeVars(in []ir.Var) []ir.Var {
	var out []ir.Var
	for _, v := range in {
		name := strings.ToUpper(word(v.Name))
		if slices.ContainsFunc(out, func(o ir.Var) bool { return o.Name == name }) {
			continue
		}
		out = append(out, ir.Var{Name: name, Value: ir.Value{Lit: word(v.Value.Lit + v.Value.Secret)}})
	}
	return out
}

func nativize(t *rapid.T, p *ir.Project) {
	p.Origin = ir.Origin{Provider: "argo", Dialect: "argo"}
	p.Pipelines = p.Pipelines[:1]
	pl := p.Pipelines[0]
	pl.ID, pl.Name, pl.Ext, pl.Finally, pl.Inputs, pl.Concurrency, pl.Permissions = "argo", "", nil, nil, nil, nil, nil
	pl.Env = nativeVars(pl.Env)
	pl.Triggers = nativeTriggers(t)
	for _, j := range pl.Jobs {
		nativizeJob(j)
	}
}

func nativeTriggers(t *rapid.T) []ir.Trigger {
	var out []ir.Trigger
	for _, k := range []ir.TriggerKind{ir.TriggerPush, ir.TriggerPR, ir.TriggerTag} {
		if !rapid.Bool().Draw(t, string(k)) {
			continue
		}
		tr := ir.Trigger{Kind: k}
		inc := func(label string) []string {
			return words(rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,5}`), 1, 2).Draw(t, label))
		}
		switch k {
		case ir.TriggerTag:
			if rapid.Bool().Draw(t, "tags") {
				tr.Tags = ir.Filter{Include: inc("tinc")}
			}
		default:
			if rapid.Bool().Draw(t, "branches") {
				tr.Branches = ir.Filter{Include: inc("inc")}
				if rapid.Bool().Draw(t, "exc") {
					tr.Branches.Exclude = []string{"wip"}
				}
			}
		}
		out = append(out, tr)
	}
	return out
}

func nativizeJob(j *ir.Job) {
	j.Name, j.Ext, j.Container, j.Runner, j.If, j.Environment, j.Manual = "", nil, nil, ir.Runner{}, nil, nil, false
	j.Cache, j.Artifacts = nil, nil
	j.Env = nativeVars(j.Env)
	if j.Timeout != nil {
		d := ir.Duration(propTimeout)
		j.Timeout = &d
	}
	if j.Retry != nil {
		j.Retry = &ir.Retry{Attempts: 2}
	}
	for i := range j.Needs {
		j.Needs[i].Artifacts, j.Needs[i].Optional = false, false
	}
	var outs []ir.Output
	for _, o := range j.Outputs {
		o = ir.Output{Name: word(o.Name)}
		if !slices.ContainsFunc(outs, func(x ir.Output) bool { return x.Name == o.Name }) {
			outs = append(outs, o)
		}
	}
	j.Outputs = outs
	if j.Matrix != nil {
		var axes []ir.Axis
		for _, a := range j.Matrix.Axes {
			a = ir.Axis{Name: word(a.Name), Values: words(a.Values)}
			if len(a.Values) > 0 && !slices.ContainsFunc(axes, func(x ir.Axis) bool { return x.Name == a.Name }) {
				axes = append(axes, a)
			}
		}
		j.Matrix = &ir.Matrix{Axes: axes}
		if len(axes) == 0 {
			j.Matrix = nil
		}
	}
	for i := range j.Services {
		sv := &j.Services[i]
		sv.Name, sv.Image = word(sv.Name), ir.Image{Ref: word(sv.Image.Ref)}
		sv.Env, sv.Ports = nativeVars(sv.Env), []string{"80"}
	}
	for i, st := range j.Steps {
		nativizeStep(st)
		st.ID = "s" + strconv.Itoa(i)
	}
}

func nativizeStep(st *ir.Step) {
	st.Name, st.Ext, st.If, st.Action, st.With, st.Image, st.Timeout = "", nil, nil, nil, nil, nil, nil
	st.Kind, st.Shell = ir.StepScript, ""
	st.Script = words(st.Script)
	if len(st.Script) == 0 {
		st.Script = []string{"wx"}
	}
	st.Env = nativeVars(st.Env)
	if st.WorkDir != "" {
		st.WorkDir = "/" + word(st.WorkDir)
	}
}

// TestIdentityProperty checks that a native project survives one trip with no change outside Ext, and that a
// second trip changes nothing at all.
func TestIdentityProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := irtest.GenProject(t, "argo", nativeFragment)
		nativize(t, p)
		ir.Normalize(p)
		raw, _, err := argo.New().Emit(p)
		if err != nil {
			t.Fatalf("Emit: %v", err)
		}
		files := map[string]string{}
		for n, b := range raw {
			files[n] = string(b)
		}
		back, _, err := argo.New().Parse(repo(files))
		if err != nil {
			t.Fatalf("Parse: %v\n%s", err, joinFiles(files))
		}
		first, _, err := argo.New().Parse(repo(files))
		if err != nil {
			t.Fatal(err)
		}
		stripExt(back)
		if ok, diff := ir.Equal(p, back); !ok {
			t.Fatalf("identity broken:\n%s\n--- emitted ---\n%s", diff, joinFiles(files))
		}
		raw2, _, err := argo.New().Emit(first)
		if err != nil {
			t.Fatalf("second Emit: %v", err)
		}
		files2 := map[string]string{}
		for n, b := range raw2 {
			files2[n] = string(b)
		}
		second, _, err := argo.New().Parse(repo(files2))
		if err != nil {
			t.Fatalf("second Parse: %v\n%s", err, joinFiles(files2))
		}
		if ok, diff := ir.Equal(first, second); !ok {
			t.Fatalf("a second trip changed the IR:\n%s\n%s", diff, joinFiles(files2))
		}
	})
}
