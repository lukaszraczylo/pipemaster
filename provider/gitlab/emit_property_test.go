package gitlab_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/ir/irtest"
)

const maxGitLabRetries = 2

// nativeFragment lists the features a GitLab file states without emulation or expressions; the generator
// draws random text for each, and nativize narrows that text to what the parser reads back.
func nativeFragment(f caps.Feature) bool {
	switch f {
	case caps.StepScript, caps.JobNeeds, caps.JobRetry, caps.JobTimeout, caps.JobManual, caps.JobAllowFailure,
		caps.JobEnv, caps.PipelineEnv, caps.JobContainer, caps.JobRunner, caps.JobMatrix, caps.JobCache, caps.JobArtifacts:
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

// nativize narrows generated IR to what one .gitlab-ci.yml states and Parse restores.
func nativize(p *ir.Project) {
	p.Origin = ir.Origin{Provider: "gitlab", Dialect: "gitlab"}
	p.Pipelines = p.Pipelines[:1]
	pl := p.Pipelines[0]
	pl.ID, pl.Name, pl.Ext = "gitlab-ci", "", nil
	pl.Triggers = []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag}}
	pl.Env = nativeVars(pl.Env)
	pl.Finally = nil
	for _, j := range pl.Jobs {
		nativizeJob(j, pl.Jobs)
	}
	nameStages(pl)
}

// nameStages writes the stage names Parse reads back from a layered pipeline.
func nameStages(pl *ir.Pipeline) {
	layers, err := ir.Layers(pl.Jobs)
	if err != nil || len(layers) < 2 {
		return
	}
	names := make([]any, len(layers))
	for i, layer := range layers {
		names[i] = fmt.Sprintf("stage-%d", i)
		for _, id := range layer {
			for _, j := range pl.Jobs {
				if j.ID == id {
					j.Ext = ir.Ext{"gitlab": map[string]any{"stage": names[i]}}
				}
			}
		}
	}
	pl.Ext = ir.Ext{"gitlab": map[string]any{"stages": names}}
}

func nativizeJob(j *ir.Job, all []*ir.Job) {
	j.Name, j.Ext = "", nil
	j.Env = nativeVars(j.Env)
	if len(j.Steps) > 0 {
		st := j.Steps[0]
		lines := words(st.Script)
		j.Steps = []*ir.Step{{ID: "script", Kind: ir.StepScript, Script: lines, Ext: ir.Ext{"gitlab": map[string]any{"section": "script"}}}}
	}
	for i := range j.Needs {
		j.Needs[i].Artifacts = needsArtifacts(all, j.Needs[i].JobID)
	}
	if j.Retry != nil {
		j.Retry = &ir.Retry{Attempts: 1 + j.Retry.Attempts%(maxGitLabRetries+1)}
	}
	if j.Container != nil {
		j.Container = &ir.Image{Ref: word(j.Container.Ref)}
	}
	j.Runner = ir.Runner{Labels: words(j.Runner.Labels)}
	if j.Matrix != nil {
		axes := make([]ir.Axis, len(j.Matrix.Axes))
		for i, a := range j.Matrix.Axes {
			axes[i] = ir.Axis{Name: a.Name, Values: words(a.Values)}
		}
		j.Matrix = &ir.Matrix{Axes: axes}
	}
	for i := range j.Cache {
		j.Cache[i] = ir.Cache{Key: word(j.Cache[i].Key), Paths: words(j.Cache[i].Paths), Policy: "pull"}
	}
	for i := range j.Artifacts {
		j.Artifacts[i] = ir.Artifact{Name: word(j.Artifacts[i].Name), Paths: words(j.Artifacts[i].Paths)}
	}
}

func needsArtifacts(all []*ir.Job, id string) bool {
	for _, j := range all {
		if j.ID == id {
			return len(j.Artifacts) > 0
		}
	}
	return false
}

func TestIdentityProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := irtest.GenProject(t, nativeFragment)
		nativize(p)
		back, files := roundTrip(t, p)
		if ok, diff := ir.Equal(p, back); !ok {
			t.Fatalf("identity broken:\n%s\n--- emitted ---\n%s", diff, dumpFiles(files))
		}
	})
}
