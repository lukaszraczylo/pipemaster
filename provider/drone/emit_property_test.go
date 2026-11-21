package drone_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/ir/irtest"
	"github.com/lukaszraczylo/pipemaster/provider/drone"
)

// nativeFragment lists the features a Drone file states without emulation, expressions or loss.
func nativeFragment(f caps.Feature) bool {
	switch f {
	case caps.StepScript, caps.StepImage, caps.StepEnv, caps.StepContinueOnError, caps.StepWorkdir, caps.JobNeeds,
		caps.JobServices, caps.JobEnv, caps.TriggerPush, caps.TriggerPullRequest, caps.TriggerTag, caps.TriggerFilterBranch:
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

// nativeTriggers draws triggers that read back as one shared pure trigger: push, pull request and tag events in
// that order, and a branch filter only when every event is a push or a pull request.
func nativeTriggers(t *rapid.T) []ir.Trigger {
	var kinds []ir.TriggerKind
	for _, k := range []ir.TriggerKind{ir.TriggerPush, ir.TriggerPR, ir.TriggerTag} {
		if rapid.Bool().Draw(t, string(k)) {
			kinds = append(kinds, k)
		}
	}
	var filter ir.Filter
	if !slices.Contains(kinds, ir.TriggerTag) && rapid.Bool().Draw(t, "filtered") {
		filter.Include = words(rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,5}`), 1, 2).Draw(t, "inc"))
		if rapid.Bool().Draw(t, "exc") {
			filter.Exclude = []string{"wip"}
		}
	}
	var out []ir.Trigger
	for _, k := range kinds {
		tr := ir.Trigger{Kind: k}
		if k != ir.TriggerTag {
			tr.Branches = ir.Filter{Include: slices.Clone(filter.Include), Exclude: slices.Clone(filter.Exclude)}
		}
		out = append(out, tr)
	}
	return out
}

// nativize narrows generated IR to what one Drone file states and Parse restores.
func nativize(t *rapid.T, p *ir.Project) {
	p.Origin = ir.Origin{Provider: "drone", Dialect: "drone"}
	p.Pipelines = p.Pipelines[:1]
	pl := p.Pipelines[0]
	pl.ID, pl.Name, pl.Ext, pl.Env, pl.Finally, pl.Inputs, pl.Concurrency, pl.Permissions = "drone", "", nil, nil, nil, nil, nil, nil
	pl.Triggers = nativeTriggers(t)
	if len(pl.Jobs) == 0 {
		pl.Jobs = []*ir.Job{{}}
	}
	ids := map[string]string{}
	for i, j := range pl.Jobs {
		ids[j.ID] = fmt.Sprintf("j%d", i)
	}
	for _, j := range pl.Jobs {
		nativizeJob(j, ids)
	}
}

func nativizeJob(j *ir.Job, ids map[string]string) {
	id := ids[j.ID]
	j.ID, j.Name, j.Ext, j.Container, j.Runner, j.If, j.Matrix = id, "", nil, nil, ir.Runner{}, nil, nil
	j.Timeout, j.Retry, j.Environment, j.AllowFailure, j.Manual = nil, nil, nil, false, false
	j.Cache, j.Artifacts, j.Outputs = nil, nil, nil
	j.Env = nativeVars(j.Env)
	var needs []ir.Need
	for _, n := range j.Needs {
		if to, ok := ids[n.JobID]; ok && to != id && !slices.ContainsFunc(needs, func(o ir.Need) bool { return o.JobID == to }) {
			needs = append(needs, ir.Need{JobID: to})
		}
	}
	j.Needs = needs
	for i := range j.Services {
		sv := &j.Services[i]
		sv.Name, sv.Image = fmt.Sprintf("svc%d", i), ir.Image{Ref: word(sv.Image.Ref)}
		sv.Env, sv.Ports = nativeVars(sv.Env), words(sv.Ports)
	}
	for i, st := range j.Steps {
		nativizeStep(st, fmt.Sprintf("s%d", i))
	}
	if len(j.Steps) == 0 {
		j.Steps = []*ir.Step{{ID: "s0", Kind: ir.StepScript, Script: []string{"wx"}, Image: &ir.Image{Ref: "wimg"}}}
	}
}

func nativizeStep(st *ir.Step, id string) {
	st.ID, st.Name, st.Ext, st.If, st.Timeout, st.Shell, st.Action, st.With = id, "", nil, nil, nil, "", nil, nil
	ref := "wimg"
	if st.Image != nil {
		ref = word(st.Image.Ref)
	}
	st.Kind, st.Image = ir.StepScript, &ir.Image{Ref: ref}
	st.Script = words(st.Script)
	if len(st.Script) == 0 {
		st.Script = []string{"wx"}
	}
	// A shell variable that Drone does not define must reach the shell unchanged.
	st.Script[0] += " $V1 ${V2}"
	st.Env = nativeVars(st.Env)
	if st.WorkDir != "" {
		st.WorkDir = word(st.WorkDir)
	}
}

func TestIdentityProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := irtest.GenProject(t, "drone", nativeFragment)
		nativize(t, p)
		ir.Normalize(p)
		raw, _, err := drone.New().Emit(p)
		if err != nil {
			t.Fatalf("Emit: %v", err)
		}
		files := map[string]string{}
		for n, b := range raw {
			files[n] = string(b)
		}
		back, _, err := drone.New().Parse(repo(files))
		if err != nil {
			t.Fatalf("Parse: %v\n%v", err, files)
		}
		if ok, diff := ir.Equal(p, back); !ok {
			t.Fatalf("identity broken:\n%s\n--- emitted ---\n%v", diff, files)
		}
	})
}
