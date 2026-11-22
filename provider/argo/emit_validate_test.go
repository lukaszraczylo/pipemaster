package argo_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/argo"
	"github.com/lukaszraczylo/pipemaster/validate"
)

// richProject uses every emitted construct: triggers, cron, matrix, needs, outputs, finally, services, cache.
func richProject() *ir.Project {
	d := ir.Duration(time.Minute)
	build := scriptJob("build", "make", "echo ${{ branch }} ${{ secrets.KEY }}")
	build.Outputs = []ir.Output{{Name: "digest", Value: "abc"}}
	build.Services = []ir.Service{{Name: "db", Image: ir.Image{Ref: "postgres:17"}, Ports: []string{"5432"}}}
	build.Artifacts = []ir.Artifact{{Name: "bin", Paths: []string{"out/bin"}}}
	build.Timeout, build.Retry = &d, &ir.Retry{Attempts: 2}
	build.Cache = []ir.Cache{{Key: "k", Paths: []string{"~/.cache"}}}
	test := scriptJob("test", "echo ${{ needs.build.outputs.digest }} ${{ matrix.os }} ${{ inputs.ver }}")
	test.Needs = []ir.Need{{JobID: "build", Artifacts: true}}
	test.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "os", Values: []string{"linux", "mac"}}}, Exclude: []ir.Combo{{{Key: "os", Value: "mac"}}}}
	deploy := scriptJob("deploy", "echo deploy")
	deploy.Needs = []ir.Need{{JobID: "test"}}
	deploy.Manual = true
	done := scriptJob("done", "echo done")
	p := project(build, test, deploy)
	pl := p.Pipelines[0]
	pl.Finally = []*ir.Job{done}
	pl.Inputs = []ir.Input{{Name: "ver", Default: "1"}}
	pl.Concurrency = &ir.Concurrency{Group: "ci"}
	pl.Triggers = []ir.Trigger{
		{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}}},
		{Kind: ir.TriggerPR},
		{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}}},
		{Kind: ir.TriggerSchedule, Cron: "0 3 * * *"},
	}
	return p
}

func TestEmittedFilesValidateAgainstSchemas(t *testing.T) {
	files, _ := emit(t, richProject())
	for name, src := range files {
		err := validate.File(argo.New().ID(), name, []byte(src))
		if err != nil && !strings.Contains(err.Error(), "no schema") {
			t.Fatalf("%s does not validate: %v\n%s", name, err, src)
		}
	}
}

func TestEmittedFilesPassArgoLint(t *testing.T) {
	linttool.RequireArgo(t)
	files, _ := emit(t, richProject())
	var template string
	for name, src := range files {
		if strings.HasPrefix(name, ".argo/workflows/") {
			template = src
		}
	}
	for name, src := range files {
		body := src
		switch {
		case strings.HasPrefix(name, ".argo/cron/"):
			body = template + "---\n" + src
		case !strings.HasPrefix(name, ".argo/workflows/"):
			continue
		}
		if out, err := linttool.ArgoLint(t, name, []byte(body)); err != nil {
			t.Fatalf("argo lint rejects %s: %v\n%s\n%s", name, err, out, body)
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
