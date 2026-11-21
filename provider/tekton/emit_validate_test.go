package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
	"github.com/lukaszraczylo/pipemaster/validate"
)

// richProject uses every emitted construct: triggers, cron, matrix, needs, finally, results, actions, workspace, services.
func richProject() *ir.Project {
	co := actionStep("co", "checkout@v4", map[string]string{"fetch-depth": "1"})
	build := jobWith(co, &ir.Step{ID: "b", Kind: ir.StepScript, Script: []string{"make", "echo ${{ branch }}"}, Env: []ir.Var{{Name: "K", Value: ir.Value{Kind: ir.ValueSecret, Secret: "KEY"}}}})
	build.ID = "build"
	build.Outputs = []ir.Output{{Name: "digest"}}
	build.Services = []ir.Service{{Name: "db", Image: ir.Image{Ref: "postgres:17"}, Ports: []string{"5432"}}}
	build.Artifacts = []ir.Artifact{{Name: "bin", Paths: []string{"out/"}}}
	test := scriptJob("test", "echo ${{ needs.build.outputs.digest }} ${{ matrix.os }} ${{ inputs.ver }}")
	test.Needs = []ir.Need{{JobID: "build", Artifacts: true}}
	test.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "os", Values: []string{"linux", "mac"}}}, Exclude: []ir.Combo{{{Key: "os", Value: "mac"}}}}
	test.If = eqStr([]string{"branch"}, "main")
	d := ir.Duration(60_000_000_000)
	test.Timeout, test.Retry = &d, &ir.Retry{Attempts: 2}
	done := scriptJob("done", "echo done")
	done.If = expr.Call{Name: "failure"}
	p := project(build, test)
	pl := p.Pipelines[0]
	pl.Finally = []*ir.Job{done}
	pl.Inputs = []ir.Input{{Name: "ver", Default: "1"}}
	pl.Triggers = []ir.Trigger{
		{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}}},
		{Kind: ir.TriggerPR, Paths: ir.Filter{Include: []string{"src/**"}}},
		{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}}},
		{Kind: ir.TriggerSchedule, Cron: "0 3 * * *"},
	}
	return p
}

func TestEmittedFilesValidateAgainstSchemas(t *testing.T) {
	files, _ := emit(t, richProject())
	for name, src := range files {
		if schemaless(name) {
			continue
		}
		if err := validate.File(tekton.New().ID(), name, []byte(src)); err != nil {
			t.Fatalf("%s does not validate: %v\n%s", name, err, src)
		}
	}
}

func TestEmittedFilesPassKubeconform(t *testing.T) {
	linttool.RequireKubeconform(t)
	files, _ := emit(t, richProject())
	for name, src := range files {
		if schemaless(name) {
			continue
		}
		if out, err := linttool.KubeconformLint(t, name, []byte(src)); err != nil {
			t.Fatalf("kubeconform rejects %s: %v\n%s\n%s", name, err, out, src)
		}
	}
}

func TestEmitIsDeterministic(t *testing.T) {
	a, _ := emit(t, richProject())
	b, _ := emit(t, richProject())
	if joinFiles(a) != joinFiles(b) {
		t.Fatal("two emissions differ")
	}
}

// schemaless names the files whose kinds have no embedded schema: the Triggers CRDs, RBAC and the kustomization.
func schemaless(name string) bool {
	for _, s := range []string{"triggerbinding", "triggertemplate", "rbac", "kustomization"} {
		if strings.Contains(name, s) {
			return true
		}
	}
	return false
}
