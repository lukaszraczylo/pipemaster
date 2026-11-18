package gha_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/validate"
)

const baseWorkflow = `name: base
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: make build
  deploy:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - run: make deploy
`

func baseProject(t *testing.T) *ir.Project {
	t.Helper()
	fsys := fstest.MapFS{".github/workflows/ci.yml": &fstest.MapFile{Data: []byte(baseWorkflow)}}
	p, _, err := gha.New(gha.GHA).Parse(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func job(p *ir.Project, id string) *ir.Job {
	for _, j := range p.Pipelines[0].Jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func lit(s string) expr.Expr { return expr.Lit{Kind: expr.LitStr, Str: s} }

type emulCase struct {
	mutate  func(*ir.Project)
	feature caps.Feature
	path    string
	want    []string
	level   caps.Level
}

const (
	jobBuild  = "pipeline/ci/job/build"
	jobDeploy = "pipeline/ci/job/deploy"
)

func emulCases() map[string]emulCase {
	return map[string]emulCase{
		"retry": {
			feature: caps.JobRetry, path: jobBuild, level: caps.Emulated,
			mutate: func(p *ir.Project) { job(p, "build").Retry = &ir.Retry{Attempts: 3} },
			want:   []string{"# pipemaster:emulated job.retry " + jobBuild, "seq 1 3", "make build"},
		},
		"manual": {
			feature: caps.JobManual, path: jobDeploy, level: caps.Emulated,
			mutate: func(p *ir.Project) { job(p, "deploy").Manual = true },
			want:   []string{"# pipemaster:emulated job.manual " + jobDeploy, "workflow_dispatch", "github.event_name == 'workflow_dispatch'"},
		},
		"cache": {
			feature: caps.JobCache, path: jobBuild, level: caps.Emulated,
			mutate: func(p *ir.Project) {
				job(p, "build").Cache = []ir.Cache{{Key: "deps-${{ sha }}", Paths: []string{"~/.cache", "vendor"}}}
			},
			want: []string{"# pipemaster:emulated job.cache " + jobBuild, "uses: actions/cache@v6", "key: deps-${{ github.sha }}", "~/.cache"},
		},
		"artifacts": {
			feature: caps.JobArtifacts, path: jobBuild, level: caps.Emulated,
			mutate: func(p *ir.Project) {
				job(p, "build").Artifacts = []ir.Artifact{{Name: "bin", Paths: []string{"dist/"}, Expire: "1 week"}}
			},
			want: []string{"# pipemaster:emulated job.artifacts " + jobBuild, "uses: actions/upload-artifact@v7", "name: bin", "retention-days: 7"},
		},
		"needs.artifacts": {
			feature: caps.JobNeedsArtifacts, path: jobDeploy, level: caps.Emulated,
			mutate: func(p *ir.Project) {
				job(p, "build").Artifacts = []ir.Artifact{{Name: "bin", Paths: []string{"dist/"}}}
				job(p, "deploy").Needs = []ir.Need{{JobID: "build", Artifacts: true}}
			},
			want: []string{"# pipemaster:emulated job.needs.artifacts " + jobDeploy, "uses: actions/download-artifact@v8", "name: bin"},
		},
		"finally": {
			feature: caps.PipelineFinally, path: "pipeline/ci", level: caps.Emulated,
			mutate: func(p *ir.Project) {
				p.Pipelines[0].Finally = []*ir.Job{{
					ID: "cleanup", Runner: ir.Runner{Labels: []string{"ubuntu-latest"}},
					Steps: []*ir.Step{{ID: "c1", Kind: ir.StepScript, Script: []string{"make clean"}}},
				}}
			},
			want: []string{"# pipemaster:emulated pipeline.finally pipeline/ci", "cleanup:", "always()", "- build", "- deploy"},
		},
		"environment.approval": {
			feature: caps.JobEnvironmentApproval, path: jobDeploy, level: caps.External,
			mutate: func(p *ir.Project) { job(p, "deploy").Environment = &ir.Environment{Name: "prod", Approval: true} },
			want:   []string{"environment: prod"},
		},
		"step.image": {
			feature: caps.StepImage, path: jobBuild + "/step/build-step-1", level: caps.Emulated,
			mutate: func(p *ir.Project) {
				s := job(p, "build").Steps[0]
				s.Image = &ir.Image{Ref: "alpine:3", Entrypoint: []string{""}}
			},
			want: []string{"# pipemaster:emulated step.image " + jobBuild + "/step/build-step-1", "uses: docker://alpine:3", "entrypoint: /bin/sh", "make build"},
		},
		"trigger.resource": {
			feature: caps.TriggerResource, path: "pipeline/ci/trigger/1", level: caps.Lost,
			mutate: func(p *ir.Project) {
				p.Pipelines[0].Triggers = append(p.Pipelines[0].Triggers, ir.Trigger{Kind: ir.TriggerResource, Resource: &ir.ResourceRef{Name: "r", Type: "git"}})
			},
			want: []string{"push"},
		},
		"expr.opaque": {
			feature: caps.ExprOpaque, path: jobBuild, level: caps.Lost,
			mutate: func(p *ir.Project) { job(p, "build").If = expr.Opaque{Dialect: "gitlab", Text: "$X == 1"} },
			want:   []string{"# pipemaster:lost", "$X == 1"},
		},
		"script.interpolation": {
			feature: caps.ScriptInterpolation, path: jobBuild + "/step/build-step-1", level: caps.Lost,
			mutate: func(p *ir.Project) {
				job(p, "build").Steps[0].Script = []string{"echo ${{ sha =~ 'a' }} ${{ branch }}"}
			},
			want: []string{"# pipemaster:lost", "github.ref_type == 'branch' && github.ref_name || ''"},
		},
	}
}

func TestEmulations(t *testing.T) {
	for name, c := range emulCases() {
		t.Run(name, func(t *testing.T) {
			p := baseProject(t)
			c.mutate(p)
			files, rep, err := gha.New(gha.GHA).Emit(p)
			if err != nil {
				t.Fatal(err)
			}
			data := string(files[".github/workflows/ci.yml"])
			for _, w := range c.want {
				if !strings.Contains(data, w) {
					t.Errorf("output lacks %q:\n%s", w, data)
				}
			}
			if got := entryLevel(rep, c.feature, c.path); got != c.level {
				t.Errorf("report level for %s at %s = %v, want %v\n%s", c.feature, c.path, got, c.level, data)
			}
			if err := validate.File("gha", "ci.yml", files[".github/workflows/ci.yml"]); err != nil {
				t.Fatalf("%v\n%s", err, data)
			}
			lintFile(t, "ci.yml", files[".github/workflows/ci.yml"])
		})
	}
}

func entryLevel(rep *report.Report, f caps.Feature, path string) caps.Level {
	for _, e := range rep.Entries {
		if e.Feature == f && e.IRPath == path {
			return e.Level
		}
	}
	return caps.Native
}

func TestEveryNonNativeCapIsEmulatedOrLost(t *testing.T) {
	covered := map[caps.Feature]bool{}
	for _, c := range emulCases() {
		covered[c.feature] = true
	}
	m := gha.New(gha.GHA).Caps()
	for _, f := range caps.AllFeatures() {
		s := m.Lookup(f)
		if s.Level != caps.Native && !covered[f] {
			t.Errorf("%s is %s but has no emulation test", f, s.Level)
		}
	}
}

func TestCapsLedger(t *testing.T) {
	want := map[caps.Feature]caps.Level{
		caps.JobRetry: caps.Emulated, caps.JobManual: caps.Emulated, caps.JobCache: caps.Emulated,
		caps.JobArtifacts: caps.Emulated, caps.JobNeedsArtifacts: caps.Emulated, caps.PipelineFinally: caps.Emulated,
		caps.JobEnvironmentApproval: caps.External, caps.StepImage: caps.Emulated, caps.TriggerResource: caps.Lost,
	}
	m := gha.New(gha.GHA).Caps()
	for _, f := range caps.AllFeatures() {
		w, ok := want[f]
		if !ok {
			w = caps.Native
		}
		if got := m.Lookup(f).Level; got != w {
			t.Errorf("%s: level %v, want %v", f, got, w)
		}
	}
	if m.Lookup(caps.JobRetry).Strategy != "step-level retry loop via shell" ||
		m.Lookup(caps.JobManual).Strategy != "workflow_dispatch trigger" {
		t.Error("strategy text differs from the ruling")
	}
}

func TestEmitUnaccountedErrors(t *testing.T) {
	p := baseProject(t)
	p.Pipelines[0].ID = "a/b"
	if _, _, err := gha.New(gha.GHA).Emit(p); err == nil {
		t.Fatal("want error for a pipeline id that is not a file stem")
	}
}

func TestEmitValueForms(t *testing.T) {
	p := baseProject(t)
	s := job(p, "build").Steps[0]
	s.Env = []ir.Var{
		{Name: "NUM", Value: ir.Value{Lit: "3.1"}},
		{Name: "BOOL", Value: ir.Value{Lit: "true"}},
		{Name: "NUL", Value: ir.Value{Lit: "null"}},
		{Name: "STAR", Value: ir.Value{Lit: "*x"}},
		{Name: "SEC", Value: ir.Value{Kind: ir.ValueSecret, Secret: "TOKEN"}},
		{Name: "MIX", Value: ir.Value{Kind: ir.ValueExpr, Expr: expr.Call{Name: "format", Args: []expr.Expr{lit("v{0}-{1}"), expr.Ref{Path: []string{"sha"}}, lit("x")}}}},
	}
	fsys, files := emitFS(t, p)
	data := string(files[".github/workflows/ci.yml"])
	for _, w := range []string{`NUM: "3.1"`, `BOOL: "true"`, `NUL: "null"`, "${{ secrets.TOKEN }}", "v${{ github.sha }}-${{ 'x' }}"} {
		if !strings.Contains(data, w) {
			t.Errorf("output lacks %q:\n%s", w, data)
		}
	}
	back, _, err := gha.New(gha.GHA).Parse(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if ok, diff := ir.Equal(p, back); !ok {
		t.Fatalf("values do not round trip:\n%s", diff)
	}
}
