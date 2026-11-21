package gitlab_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func jobByID(t *testing.T, p *ir.Project, id string) *ir.Job {
	t.Helper()
	for _, j := range p.Pipelines[0].Jobs {
		if j.ID == id {
			return j
		}
	}
	t.Fatalf("job %s missing", id)
	return nil
}

func bkProducer(steps ...*ir.Step) *ir.Job {
	j := glJob("prod", steps...)
	j.Outputs = []ir.Output{{Name: "v", Value: "1"}}
	return j
}

func bkConsumer(line string, matrix bool) *ir.Job {
	c := glJob("cons", glStep(line))
	if matrix {
		c.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "q", Values: []string{"a", "b"}}}}
	}
	c.Needs = []ir.Need{{JobID: "prod", Artifacts: true}}
	return c
}

func TestOutputsSurviveProducerAfterScript(t *testing.T) {
	const line = "echo ${{ needs.prod.outputs.v }}"
	cl := &ir.Step{ID: "cleanup", Kind: ir.StepScript, Script: []string{"rm -rf tmp"}, If: expr.Call{Name: "always"}}
	_, back, rep := emitParse(t, glProject("gitlab", bkProducer(glStep("make"), cl), bkConsumer(line, false)), nil)
	noIgnoredNotes(t, rep)
	p := jobByID(t, back, "prod")
	if len(p.Outputs) != 1 || p.Outputs[0].Name != "v" || p.Outputs[0].Value != "1" {
		t.Fatalf("outputs not restored: %+v", p.Outputs)
	}
	var all []string
	for _, s := range p.Steps {
		all = append(all, s.Script...)
	}
	if got := strings.Join(all, "|"); got != "make|rm -rf tmp" {
		t.Errorf("producer scripts = %q, want writer line gone and cleanup untouched", got)
	}
	if got := jobByID(t, back, "cons").Steps[0].Script[0]; got != line {
		t.Errorf("consumer line %q", got)
	}
}

func TestOutputsSurviveGHAImportedProducerSteps(t *testing.T) {
	const line = "echo ${{ needs.prod.outputs.v }}"
	mk := func(id, l string) *ir.Step { s := glStep(l); s.ID = id; return s }
	_, back, rep := emitParse(t, glProject("gitlab", bkProducer(mk("build", "make"), mk("pack", "tar cf x")), bkConsumer(line, false)), nil)
	noIgnoredNotes(t, rep)
	p := jobByID(t, back, "prod")
	if len(p.Outputs) != 1 {
		t.Fatalf("outputs not restored: %+v", p.Outputs)
	}
	var all []string
	for _, s := range p.Steps {
		all = append(all, s.Script...)
	}
	if got := strings.Join(all, "|"); got != "make|tar cf x" {
		t.Errorf("producer scripts = %q", got)
	}
}

func TestMatrixAndOutputOnOneLine(t *testing.T) {
	const line = "echo ${{ matrix.q }} ${{ needs.prod.outputs.v }}"
	for _, matrix := range []bool{true, false} {
		for _, prodFirst := range []bool{true, false} {
			name := "marker"
			if !matrix {
				name = "nomarker"
			}
			if prodFirst {
				name += "-prodfirst"
			}
			t.Run(name, func(t *testing.T) {
				c := bkConsumer(line, matrix)
				if !matrix {
					c.Matrix = nil
				}
				jobs := []*ir.Job{bkProducer(glStep("make")), c}
				if !prodFirst {
					jobs = []*ir.Job{c, jobs[0]}
				}
				_, back, rep := emitParse(t, glProject("gitlab", jobs...), nil)
				noIgnoredNotes(t, rep)
				if got := jobByID(t, back, "cons").Steps[0].Script[0]; got != line {
					t.Errorf("got %q want %q", got, line)
				}
			})
		}
	}
}

func TestEditedMatrixAndOutputLineKeepsFileText(t *testing.T) {
	const line = "echo ${{ matrix.q }} ${{ needs.prod.outputs.v }}"
	jobs := []*ir.Job{bkProducer(glStep("make")), bkConsumer(line, false)}
	jobs[1].Matrix = nil
	_, back, rep := emitParse(t, glProject("gitlab", jobs...), func(s string) string {
		if !strings.Contains(s, "- echo $q $v") {
			t.Fatalf("emitted text lacks the line:\n%s", s)
		}
		return strings.Replace(s, "- echo $q $v", "- echo $q $v X", 1)
	})
	if got := jobByID(t, back, "cons").Steps[0].Script[0]; got != "echo $q $v X" {
		t.Errorf("edited line = %q, want the file text", got)
	}
	if !ignoredNote(rep) {
		t.Error("edited line gave no marker-ignored note")
	}
}

func TestStepEnvAndOutputsRestoreTogether(t *testing.T) {
	const line = "echo ${{ needs.prod.outputs.v }}"
	mk := glStep("make")
	mk.Env = []ir.Var{{Name: "A", Value: ir.Value{Lit: "one"}}}
	_, back, _ := emitParse(t, glProject("gitlab", bkProducer(mk), bkConsumer(line, false)), nil)
	p := jobByID(t, back, "prod")
	if len(p.Outputs) != 1 || len(p.Steps) != 1 || len(p.Steps[0].Env) != 1 || strings.Join(p.Steps[0].Script, "|") != "make" {
		t.Fatalf("producer = outputs %+v, env %+v, script %q", p.Outputs, p.Steps[0].Env, p.Steps[0].Script)
	}
}
