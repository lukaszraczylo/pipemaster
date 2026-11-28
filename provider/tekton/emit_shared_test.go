package tekton_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func twoStepJob() *ir.Job {
	j := scriptJob("build", "echo data > shared.txt")
	j.Steps = append(j.Steps, &ir.Step{ID: "use", Kind: ir.StepScript, Image: &ir.Image{Ref: "golang:1.27"}, Script: []string{"cat shared.txt"}})
	return j
}

func TestTwoStepTaskWithNoCheckoutSharesAWorkingDirectory(t *testing.T) {
	p := project(twoStepJob())
	files, rep := emit(t, p)
	task := taskDoc(t, files, "ci-build")
	if got := get(task, "spec", "stepTemplate", "workingDir"); got != "/workspace" {
		t.Fatalf("stepTemplate.workingDir = %v, want /workspace", got)
	}
	wantEntry(t, rep, caps.StepWorkdir, caps.Emulated, "/workspace")
	back, brep := reparse(t, files)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Fatalf("the working directory did not collapse on the way back:\n%s", diff)
	}
	for _, e := range brep.Entries {
		if e.Feature == caps.StepWorkdir {
			t.Errorf("unexpected entry on the way back: %+v", e)
		}
	}
}

func TestOneStepTaskKeepsNoWorkingDirectory(t *testing.T) {
	files, rep := emit(t, project(scriptJob("build", "make")))
	if got := get(taskDoc(t, files, "ci-build"), "spec", "stepTemplate", "workingDir"); got != nil {
		t.Fatalf("a single step needs no shared directory, got %v", got)
	}
	if entry(rep, caps.StepWorkdir, "") != nil {
		t.Fatalf("unexpected workdir entry: %+v", rep.Entries)
	}
}
