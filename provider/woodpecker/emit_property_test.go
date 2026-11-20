package woodpecker_test

import (
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/ir/irtest"
	"github.com/lukaszraczylo/pipemaster/provider/woodpecker"
)

// nativeFragment lists the features a Woodpecker file states without emulation or expressions.
func nativeFragment(f caps.Feature) bool {
	switch f {
	case caps.StepScript, caps.StepImage, caps.StepEnv, caps.StepContinueOnError, caps.StepWorkdir, caps.JobNeeds,
		caps.JobMatrix, caps.JobServices, caps.TriggerPush, caps.TriggerPullRequest, caps.TriggerTag, caps.TriggerManual,
		caps.TriggerFilterBranch, caps.TriggerFilterPaths:
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
		name := word(v.Name)
		if slices.ContainsFunc(out, func(o ir.Var) bool { return o.Name == name }) {
			continue
		}
		out = append(out, ir.Var{Name: name, Value: ir.Value{Lit: word(v.Value.Lit + v.Value.Secret)}})
	}
	return out
}

// nativize narrows generated IR to what one Woodpecker workflow set states and Parse restores.
func nativize(t *rapid.T, p *ir.Project) {
	p.Origin = ir.Origin{Provider: "woodpecker", Dialect: "woodpecker"}
	p.Pipelines = p.Pipelines[:1]
	pl := p.Pipelines[0]
	pl.ID, pl.Name, pl.Ext, pl.Env, pl.Finally, pl.Inputs, pl.Concurrency, pl.Permissions = "woodpecker", "", nil, nil, nil, nil, nil, nil
	pl.Triggers = nativeTriggers(t)
	for _, j := range pl.Jobs {
		nativizeJob(j)
	}
}

func nativeTriggers(t *rapid.T) []ir.Trigger {
	var out []ir.Trigger
	for _, k := range []ir.TriggerKind{ir.TriggerPush, ir.TriggerPR, ir.TriggerTag, ir.TriggerManual} {
		if !rapid.Bool().Draw(t, string(k)) {
			continue
		}
		tr := ir.Trigger{Kind: k}
		if k == ir.TriggerPush || k == ir.TriggerPR {
			if rapid.Bool().Draw(t, "branches") {
				tr.Branches = ir.Filter{Include: words(rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,5}`), 1, 2).Draw(t, "inc"))}
				if rapid.Bool().Draw(t, "exc") {
					tr.Branches.Exclude = []string{"wip"}
				}
			}
			if rapid.Bool().Draw(t, "paths") {
				tr.Paths = ir.Filter{Include: []string{"src/**", "*.go"}[:1+rapid.IntRange(0, 1).Draw(t, "np")]}
			}
		}
		out = append(out, tr)
	}
	return out
}

func nativizeJob(j *ir.Job) {
	j.Name, j.Ext, j.Env, j.Container, j.Runner, j.If = "", nil, nil, nil, ir.Runner{}, nil
	j.Timeout, j.Retry, j.Environment, j.AllowFailure, j.Manual = nil, nil, nil, false, false
	j.Cache, j.Artifacts, j.Outputs = nil, nil, nil
	for i := range j.Needs {
		j.Needs[i].Artifacts = false
	}
	if j.Matrix != nil {
		axes := make([]ir.Axis, len(j.Matrix.Axes))
		for i, a := range j.Matrix.Axes {
			axes[i] = ir.Axis{Name: word(a.Name), Values: words(a.Values)}
		}
		j.Matrix = &ir.Matrix{Axes: axes}
	}
	for i := range j.Services {
		sv := &j.Services[i]
		sv.Name, sv.Image = word(sv.Name), ir.Image{Ref: word(sv.Image.Ref)}
		sv.Env, sv.Ports = nativeVars(sv.Env), words(sv.Ports)
	}
	for _, st := range j.Steps {
		nativizeStep(st)
	}
}

func nativizeStep(st *ir.Step) {
	st.Name, st.Ext, st.If, st.Timeout, st.Shell, st.Action, st.With = "", nil, nil, nil, "", nil, nil
	st.Kind, st.Image = ir.StepScript, &ir.Image{Ref: word(st.Image.Ref)}
	st.Script = words(st.Script)
	if len(st.Script) == 0 {
		st.Script = []string{"wx"}
	}
	st.Env = nativeVars(st.Env)
	if st.WorkDir != "" {
		st.WorkDir = word(st.WorkDir)
	}
}

func TestIdentityProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := irtest.GenProject(t, "woodpecker", nativeFragment)
		nativize(t, p)
		ir.Normalize(p)
		raw, _, err := woodpecker.New().Emit(p)
		if err != nil {
			t.Fatalf("Emit: %v", err)
		}
		files := map[string]string{}
		for n, b := range raw {
			files[n] = string(b)
		}
		back, _, err := woodpecker.New().Parse(repo(files))
		if err != nil {
			t.Fatalf("Parse: %v\n%v", err, files)
		}
		if ok, diff := ir.Equal(p, back); !ok {
			t.Fatalf("identity broken:\n%s\n--- emitted ---\n%v", diff, files)
		}
	})
}
