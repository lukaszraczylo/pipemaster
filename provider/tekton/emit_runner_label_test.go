package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestRunnerTagBecomesTaskRunSpecNodeSelector(t *testing.T) {
	j := scriptJob("build", "make")
	j.Runner = ir.Runner{Labels: []string{"gitlab-org"}}
	p := project(j)
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}}
	files, rep := emit(t, p)
	all := joinFiles(files)
	for _, want := range []string{"taskRunSpecs:", "pipelineTaskName: build", "nodeSelector:", "runner.pipemaster.io/gitlab-org: \"true\""} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q:\n%s", want, all)
		}
	}
	wantEntry(t, rep, caps.JobRunner, caps.External, "runner.pipemaster.io/gitlab-org=true")
}

func TestRunnerTagWithoutTriggerTemplateIsLost(t *testing.T) {
	j := scriptJob("build", "make")
	j.Runner = ir.Runner{Labels: []string{"gitlab-org"}}
	_, rep := emit(t, project(j))
	wantEntry(t, rep, caps.JobRunner, caps.Lost, "no generated TriggerTemplate")
}
