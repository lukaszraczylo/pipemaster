package concourse_test

import (
	"fmt"
	"testing"

	"pgregory.net/rapid"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/concourse"
)

var identityImages = []string{"golang:1.22", "alpine:3.20", "node:22"}

func genJobs(t *rapid.T) []*ir.Job {
	n := rapid.IntRange(1, 4).Draw(t, "jobs")
	var jobs []*ir.Job
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("job-%d", i)
		j := &ir.Job{ID: id, Name: id}
		for s := 0; s < rapid.IntRange(1, 3).Draw(t, "steps"); s++ {
			sid := fmt.Sprintf("%s-s%d", id, s)
			st := &ir.Step{ID: sid, Name: sid, Kind: ir.StepScript, Script: []string{rapid.SampledFrom([]string{"make", "go test ./...", "echo hi"}).Draw(t, "line")}}
			if rapid.Bool().Draw(t, "image") {
				st.Image = &ir.Image{Ref: rapid.SampledFrom(identityImages).Draw(t, "ref")}
			} else {
				st.Image = &ir.Image{Ref: "ubuntu:latest"}
			}
			if rapid.Bool().Draw(t, "env") {
				st.Env = []ir.Var{{Name: "MODE", Value: ir.Value{Lit: rapid.SampledFrom([]string{"fast", "slow"}).Draw(t, "mode")}}}
			}
			st.ContinueOnError = rapid.Bool().Draw(t, "coe")
			j.Steps = append(j.Steps, st)
		}
		for k := 0; k < i; k++ {
			if rapid.Bool().Draw(t, "need") {
				j.Needs = append(j.Needs, ir.Need{JobID: fmt.Sprintf("job-%d", k)})
			}
		}
		if rapid.Bool().Draw(t, "retry") {
			j.Retry = &ir.Retry{Attempts: rapid.IntRange(2, 4).Draw(t, "attempts")}
		}
		if rapid.Bool().Draw(t, "timeout") {
			d := ir.Duration(rapid.SampledFrom([]int{10, 30, 90}).Draw(t, "mins")) * ir.Duration(60e9)
			j.Timeout = &d
		}
		jobs = append(jobs, j)
	}
	return jobs
}

// fragment is what the native fragment of the IR keeps through emit and parse.
func fragment(p *ir.Project) string {
	var out string
	for _, pl := range p.Pipelines {
		for _, tr := range pl.Triggers {
			out += fmt.Sprintf("T %s %v\n", tr.Kind, tr.Branches.Include)
		}
		for _, j := range pl.Jobs {
			out += fmt.Sprintf("J %s %q needs=%v manual=%v", j.ID, j.Name, j.Needs, j.Manual)
			if j.Retry != nil {
				out += fmt.Sprintf(" retry=%d", j.Retry.Attempts)
			}
			if j.Timeout != nil {
				out += fmt.Sprintf(" timeout=%v", *j.Timeout)
			}
			out += "\n"
			for _, s := range j.Steps {
				img := ""
				if s.Image != nil {
					img = s.Image.Ref
				}
				out += fmt.Sprintf("  S %s %q %v img=%s env=%v coe=%v\n", s.ID, s.Name, s.Script, img, s.Env, s.ContinueOnError)
			}
		}
	}
	return out
}

func TestNativeFragmentIsAnIdentity(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		p := project(genJobs(rt)...)
		p.Pipelines[0].Triggers = []ir.Trigger{pushOn(rapid.SampledFrom([]string{"main", "dev"}).Draw(rt, "branch"))}
		ir.Normalize(p)
		want := fragment(p)
		raw, _, err := concourse.New().Emit(p)
		if err != nil {
			rt.Fatalf("Emit: %v", err)
		}
		files := map[string]string{}
		for n, b := range raw {
			files[n] = string(b)
		}
		back, _ := reparse(t, files)
		if got := fragment(back); got != want {
			rt.Fatalf("fragment changed\nwant:\n%s\ngot:\n%s\nfile:\n%s", want, got, files[".concourse/ci.yml"])
		}
	})
}
