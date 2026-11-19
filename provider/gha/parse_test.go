package gha_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/internal/golden"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
	"github.com/lukaszraczylo/pipemaster/report"
)

const featuresDir = "../../testdata/gha/features"

func workflow(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[".github/workflows/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

func parse(t *testing.T, files map[string]string) (*ir.Project, *report.Report) {
	t.Helper()
	p, rep, err := gha.New(gha.GHA).Parse(workflow(files))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p, rep
}

func one(t *testing.T, yml string) *ir.Pipeline {
	t.Helper()
	p, _ := parse(t, map[string]string{"ci.yml": yml})
	return p.Pipelines[0]
}

func TestParseFeatures(t *testing.T) {
	dirs, err := os.ReadDir(featuresDir)
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, f := range caps.AllFeatures() {
		known[string(f)] = true
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		t.Run(d.Name(), func(t *testing.T) {
			// <feature>--<slug> is a regression fixture for the feature.
			if feature, _, _ := strings.Cut(d.Name(), "--"); !known[feature] {
				t.Fatalf("%s is not a caps feature", d.Name())
			}
			root := filepath.Join(featuresDir, d.Name())
			p, _, err := gha.New(gha.GHA).Parse(os.DirFS(root))
			if err != nil {
				t.Fatal(err)
			}
			got, err := ir.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, filepath.Join(root, "ir.golden.yaml"), got)
			back, err := ir.Unmarshal(got)
			if err != nil {
				t.Fatalf("golden does not decode: %v", err)
			}
			if ok, diff := ir.Equal(p, back); !ok {
				t.Fatalf("Marshal/Unmarshal round trip differs: %s", diff)
			}
		})
	}
}

func TestOnShorthandForms(t *testing.T) {
	const jobs = "jobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: x\n"
	groups := [][]string{
		{"on: push\n", "on: [push]\n", "on:\n  push:\n", "on:\n  push: {}\n"},
		{"on: issues\n", "on: [issues]\n", "on:\n  issues:\n"},
		{"on: [pull_request, push]\n", "on:\n  push:\n  pull_request:\n"},
	}
	for _, forms := range groups {
		want := one(t, forms[0]+jobs)
		for _, on := range forms[1:] {
			got := one(t, on+jobs)
			if ok, diff := ir.Equal(&ir.Project{Pipelines: []*ir.Pipeline{want}}, &ir.Project{Pipelines: []*ir.Pipeline{got}}); !ok {
				t.Errorf("form %q differs from %q:\n%s", on, forms[0], diff)
			}
		}
	}
	// GHA fires a push without filters for branches and tags.
	if tr := one(t, "on: push\n"+jobs).Triggers; len(tr) != 2 || tr[0].Kind != ir.TriggerPush || tr[1].Kind != ir.TriggerTag {
		t.Fatalf("triggers = %+v", tr)
	}
}

func TestPushBranchesAndTagsSplit(t *testing.T) {
	pl := one(t, `on:
  push:
    branches: [main, "!release/**", "release/1.*"]
    tags: ["v*"]
    paths-ignore: ["docs/**"]
jobs: {}
`)
	var push, tag *ir.Trigger
	for i := range pl.Triggers {
		switch pl.Triggers[i].Kind {
		case ir.TriggerPush:
			push = &pl.Triggers[i]
		case ir.TriggerTag:
			tag = &pl.Triggers[i]
		}
	}
	if push == nil || tag == nil || len(pl.Triggers) != 2 {
		t.Fatalf("triggers = %+v", pl.Triggers)
	}
	if want := []string{"main", "!release/**", "release/1.*"}; !reflect.DeepEqual(push.Branches.Include, want) {
		t.Errorf("branches order = %v", push.Branches.Include)
	}
	if !reflect.DeepEqual(tag.Tags.Include, []string{"v*"}) {
		t.Errorf("tags = %v", tag.Tags)
	}
	for _, tr := range []*ir.Trigger{push, tag} {
		if !reflect.DeepEqual(tr.Paths.Exclude, []string{"docs/**"}) {
			t.Errorf("paths-ignore not applied to %s: %+v", tr.Kind, tr.Paths)
		}
	}
}

func TestEventMapping(t *testing.T) {
	pl := one(t, `on:
  pull_request_target:
    types: [opened]
  schedule:
    - cron: "0 1 * * *"
    - cron: "0 2 * * *"
  workflow_dispatch:
    inputs:
      env:
        description: Target
        required: true
        default: prod
        type: choice
        options: [prod, dev]
  workflow_call:
    inputs:
      x:
        type: string
  repository_dispatch:
    types: [go]
  workflow_run:
    workflows: [Build]
    types: [completed]
  issues:
    types: [opened]
jobs: {}
`)
	kinds := map[ir.TriggerKind]int{}
	for _, tr := range pl.Triggers {
		kinds[tr.Kind]++
	}
	want := map[ir.TriggerKind]int{
		ir.TriggerPR: 1, ir.TriggerSchedule: 2, ir.TriggerManual: 1,
		ir.TriggerAPI: 2, ir.TriggerPipelineDone: 1,
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds = %v", kinds)
	}
	if len(pl.Inputs) != 1 || pl.Inputs[0] != (ir.Input{Name: "env", Type: "choice", Default: "prod", Description: "Target", Required: true}) {
		t.Errorf("inputs = %+v", pl.Inputs)
	}
	g := pl.Ext["gha"].(map[string]any)
	if ev, _ := g["events"].(map[string]any); ev["issues"] == nil {
		t.Errorf("issues event dropped: %#v", pl.Ext)
	}
	for _, tr := range pl.Triggers {
		if tr.Kind == ir.TriggerPR && tr.Ext["gha"].(map[string]any)["event"] != "pull_request_target" {
			t.Errorf("pull_request_target not marked: %#v", tr.Ext)
		}
		if tr.Kind == ir.TriggerPR && tr.Ext["gha"].(map[string]any)["types"] == nil {
			t.Errorf("types dropped: %#v", tr.Ext)
		}
		if tr.Kind == ir.TriggerManual && tr.Ext["gha"].(map[string]any)["inputs"] == nil {
			t.Errorf("input options dropped: %#v", tr.Ext)
		}
	}
}

func TestUnknownKeysToExt(t *testing.T) {
	pl := one(t, `on: push
run-name: hello
jobs:
  x:
    foo: bar
    runs-on: ubuntu-latest
    steps:
      - run: x
        bar: [1, {a: b}]
`)
	if got := pl.Ext["gha"].(map[string]any)["run-name"]; got != "hello" {
		t.Errorf("run-name = %v", got)
	}
	j := pl.Jobs[0]
	if got := j.Ext["gha"].(map[string]any)["foo"]; got != "bar" {
		t.Errorf("job ext = %#v", j.Ext)
	}
	if _, dup := j.Ext["gha"].(map[string]any)["runs-on"]; dup {
		t.Error("consumed key runs-on also landed in Ext")
	}
	bar := j.Steps[0].Ext["gha"].(map[string]any)["bar"]
	if want := []any{1, map[string]any{"a": "b"}}; !reflect.DeepEqual(bar, want) {
		t.Errorf("step ext bar = %#v", bar)
	}
}

func TestParseCycleError(t *testing.T) {
	_, _, err := gha.New(gha.GHA).Parse(workflow(map[string]string{"ci.yml": `on: push
jobs:
  a:
    needs: b
    runs-on: x
    steps: [{run: x}]
  b:
    needs: a
    runs-on: x
    steps: [{run: x}]
`}))
	if err == nil {
		t.Fatal("want error")
	}
	for _, s := range []string{".github/workflows/ci.yml:3", "needs cycle"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q lacks %q", err, s)
		}
	}
}

func TestParseMissingNeedError(t *testing.T) {
	_, _, err := gha.New(gha.GHA).Parse(workflow(map[string]string{"ci.yml": "on: push\njobs:\n  a:\n    needs: nope\n    runs-on: x\n    steps: [{run: x}]\n"}))
	if err == nil || !strings.Contains(err.Error(), "ci.yml:3") || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestSyntaxErrorHasLine(t *testing.T) {
	_, _, err := gha.New(gha.GHA).Parse(workflow(map[string]string{"ci.yml": "on: push\njobs:\n  a: [\n"}))
	if err == nil || !strings.Contains(err.Error(), "ci.yml:") {
		t.Fatalf("err = %v", err)
	}
}

func TestDuplicateStem(t *testing.T) {
	_, _, err := gha.New(gha.GHA).Parse(workflow(map[string]string{"ci.yml": "on: push\n", "ci.yaml": "on: push\n"}))
	if err == nil || !strings.Contains(err.Error(), "ci.yml") || !strings.Contains(err.Error(), "ci.yaml") {
		t.Fatalf("err = %v", err)
	}
}

func TestBadUses(t *testing.T) {
	for _, u := range []string{"nope", "a@v1", "owner/repo", "docker://", "a b/c@v1"} {
		_, _, err := gha.New(gha.GHA).Parse(workflow(map[string]string{"ci.yml": "on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - uses: " + `"` + u + `"` + "\n"}))
		if err == nil || !strings.Contains(err.Error(), "ci.yml:6") {
			t.Errorf("uses %q: err = %v", u, err)
		}
	}
}

func TestActionRefs(t *testing.T) {
	pl := one(t, `on: push
jobs:
  a:
    runs-on: x
    steps:
      - uses: actions/checkout@v4
      - uses: owner/repo/sub/dir@main
      - uses: docker://alpine:3.20
      - uses: ./local/act
`)
	want := []ir.ActionRef{
		{Kind: ir.ActionRepo, Owner: "actions", Repo: "checkout", Ref: "v4"},
		{Kind: ir.ActionRepo, Owner: "owner", Repo: "repo", Path: "sub/dir", Ref: "main"},
		{Kind: ir.ActionDocker, Image: "alpine:3.20"},
		{Kind: ir.ActionLocal, Path: "./local/act"},
	}
	for i, s := range pl.Jobs[0].Steps {
		if s.Kind != ir.StepAction || *s.Action != want[i] {
			t.Errorf("step %d = %+v", i, s.Action)
		}
	}
}

func TestStepIDs(t *testing.T) {
	pl := one(t, `on: push
jobs:
  build:
    runs-on: x
    steps:
      - run: a
      - id: named
        run: b
      - run: c
`)
	ss := pl.Jobs[0].Steps
	if ss[0].ID != "build-step-1" || ss[1].ID != "named" || ss[2].ID != "build-step-3" {
		t.Fatalf("ids = %s %s %s", ss[0].ID, ss[1].ID, ss[2].ID)
	}
	derived := func(s *ir.Step) bool {
		g, _ := s.Ext["gha"].(map[string]any)
		return g[gha.ExtDerivedID] == true
	}
	if !derived(ss[0]) || derived(ss[1]) || !derived(ss[2]) {
		t.Fatal("derived marker wrong")
	}
}

func TestScriptTemplates(t *testing.T) {
	pl := one(t, `on: push
jobs:
  a:
    runs-on: x
    steps:
      - run: |
          echo ${{ github.sha }}
          echo ${{ github.workspace }} ${{ toJSON(github.event.foo[github.run_id]) }}
          echo plain
`)
	want := []string{
		"echo ${{ sha }}",
		"echo ${{ gha.github.workspace }} ${{ opaque(gha): toJSON(github.event.foo[github.run_id]) }}",
		"echo plain",
	}
	if got := pl.Jobs[0].Steps[0].Script; !reflect.DeepEqual(got, want) {
		t.Fatalf("script = %q", got)
	}
	for _, l := range pl.Jobs[0].Steps[0].Script {
		if _, err := expr.ParseScriptTemplate(l); err != nil {
			t.Errorf("line %q is not a canonical template: %v", l, err)
		}
	}
}

func TestValueForms(t *testing.T) {
	pl := one(t, `on: push
env:
  PLAIN: text
  NUM: 3.10
  SEC: ${{ secrets.TOKEN }}
  ONE: ${{ github.ref_name }}
  MIX: "v{1} ${{ github.sha }} and ${{ github.actor }}}"
jobs: {}
`)
	byName := map[string]ir.Value{}
	for _, v := range pl.Env {
		byName[v.Name] = v.Value
	}
	if v := byName["PLAIN"]; v.Kind != ir.ValueLit || v.Lit != "text" {
		t.Errorf("PLAIN = %+v", v)
	}
	if v := byName["NUM"]; v.Kind != ir.ValueLit || v.Lit != "3.1" {
		t.Errorf("NUM = %+v", v)
	}
	if v := byName["SEC"]; v.Kind != ir.ValueSecret || v.Secret != "TOKEN" {
		t.Errorf("SEC = %+v", v)
	}
	if v := byName["ONE"]; v.Kind != ir.ValueExpr || expr.String(v.Expr) != "ref_name" {
		t.Errorf("ONE = %+v", v)
	}
	if v := byName["MIX"]; v.Kind != ir.ValueExpr || expr.String(v.Expr) != "format('v{{1}} {0} and {1}}}', sha, actor)" {
		t.Errorf("MIX = %q", expr.String(v.Expr))
	}
	if names := []string{pl.Env[0].Name, pl.Env[1].Name}; names[0] != "PLAIN" || names[1] != "NUM" {
		t.Errorf("env order lost: %v", names)
	}
}

func TestIfBareAndWrapped(t *testing.T) {
	pl := one(t, `on: push
jobs:
  a:
    if: github.ref == 'refs/heads/main'
    runs-on: x
    steps:
      - if: ${{ failure() }}
        run: x
      - if: ${{ github.event_name == 'push' }} 
        run: y
`)
	j := pl.Jobs[0]
	if got := expr.String(j.If); got != "ref == 'refs/heads/main'" {
		t.Errorf("job if = %q", got)
	}
	if got := expr.String(j.Steps[0].If); got != "failure()" {
		t.Errorf("wrapped if = %q", got)
	}
	if got := expr.String(j.Steps[1].If); got != "event == 'push'" {
		t.Errorf("trailing-space if = %q", got)
	}
}

func TestMatrixExpressionKeptInExtAndReported(t *testing.T) {
	p, rep := parse(t, map[string]string{"ci.yml": `on: push
jobs:
  a:
    runs-on: x
    strategy:
      matrix: ${{ fromJSON(needs.b.outputs.m) }}
      fail-fast: false
    steps: [{run: x}]
  b:
    runs-on: x
    steps: [{run: x}]
`})
	var a *ir.Job
	for _, j := range p.Pipelines[0].Jobs {
		if j.ID == "a" {
			a = j
		}
	}
	if a.Matrix != nil {
		t.Fatal("expression matrix must not be modelled")
	}
	if _, ok := a.Ext["gha"].(map[string]any)["strategy"]; !ok {
		t.Fatalf("strategy missing from Ext: %#v", a.Ext)
	}
	if len(rep.Entries) != 1 || rep.Entries[0].Feature != caps.JobMatrix || !strings.Contains(rep.Entries[0].SourceLoc, "ci.yml:5") {
		t.Fatalf("report = %+v", rep.Entries)
	}
}

func TestMatrixJSONValues(t *testing.T) {
	pl := one(t, `on: push
jobs:
  a:
    runs-on: x
    strategy:
      max-parallel: 2
      matrix:
        go: ["1.24", 1.25]
        cfg: [{b: 2, a: 1}]
        include:
          - go: "1.24"
            extra: yes
    steps: [{run: x}]
`)
	m := pl.Jobs[0].Matrix
	if m == nil || m.MaxParallel != 2 || len(m.Axes) != 2 {
		t.Fatalf("matrix = %+v", m)
	}
	if !reflect.DeepEqual(m.Axes[0].Values, []string{"1.24", "1.25"}) || m.Axes[1].Values[0] != `{"a":1,"b":2}` {
		t.Errorf("axes = %+v", m.Axes)
	}
}

func TestEmbeddedMetaIgnored(t *testing.T) {
	pl := one(t, "on: push\njobs: {}\n# pipemaster:meta:begin\n# abcd\n# pipemaster:meta:end\n")
	if pl.Ext != nil {
		t.Fatalf("meta block leaked into Ext: %#v", pl.Ext)
	}
}

func TestReusableWorkflowCall(t *testing.T) {
	pl := one(t, `on: push
jobs:
  call:
    uses: ./.github/workflows/other.yml
    with:
      x: ${{ github.sha }}
    secrets: inherit
`)
	s := pl.Jobs[0].Steps[0]
	if s.Kind != ir.StepPipelineCall || s.Action.Kind != ir.ActionLocal || s.With["x"].Kind != ir.ValueExpr {
		t.Fatalf("step = %+v", s)
	}
	if s.Ext["gha"].(map[string]any)["secrets"] != "inherit" {
		t.Fatalf("secrets dropped: %#v", s.Ext)
	}
}

func TestDetectAndRegistered(t *testing.T) {
	p, err := provider.Get(provider.GHA)
	if err != nil {
		t.Fatal(err)
	}
	if p.Detect(fstest.MapFS{}) || !p.Detect(workflow(map[string]string{"a.yaml": "on: push\n"})) {
		t.Fatal("Detect wrong")
	}
	if p.Detect(fstest.MapFS{".github/workflows/readme.md": &fstest.MapFile{}}) {
		t.Fatal("Detect accepts non-yaml")
	}
	_ = p.Caps()
}

func TestOtherDialectSharesFamilyExtKey(t *testing.T) {
	fsys := fstest.MapFS{".gitea/workflows/ci.yml": &fstest.MapFile{Data: []byte("on: push\nrun-name: x\njobs: {}\n")}}
	p, _, err := gha.New(gha.Gitea).Parse(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Pipelines[0].Ext[provider.GHA]; !ok || p.Origin.Dialect != provider.Gitea {
		t.Fatalf("origin %+v ext %#v", p.Origin, p.Pipelines[0].Ext)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: echo ${{ github.sha }}\n",
		"on: [push, pull_request]\njobs: {}\n",
		"on:\n  schedule:\n    - cron: '* * * * *'\njobs:\n  a:\n    needs: a\n",
		"jobs: &x\n  a: *x\n",
		"on: {push: {branches: [a, '!b']}}\nenv: {A: '${{ secrets.X }}'}\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		fsys := workflow(map[string]string{"f.yml": s})
		p, _, err := gha.New(gha.GHA).Parse(fsys)
		if err != nil {
			return
		}
		if _, err := ir.Marshal(p); err != nil {
			t.Fatalf("Marshal of a parsed project failed: %v", err)
		}
	})
}

func TestNullOn(t *testing.T) {
	if pl := one(t, "on:\njobs: {}\n"); len(pl.Triggers) != 0 || pl.Ext != nil {
		t.Fatalf("pipeline = %+v", pl)
	}
}

func TestDefaultsRunApplyToRunSteps(t *testing.T) {
	pl := one(t, `on: push
defaults:
  run:
    shell: bash
    working-directory: app
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: make
      - run: make test
        working-directory: other
      - uses: actions/checkout@v4
  lint:
    runs-on: ubuntu-latest
    defaults:
      run:
        shell: sh
    steps:
      - run: lint
`)
	build, lint := pl.Jobs[0].Steps, pl.Jobs[1].Steps
	if build[0].WorkDir != "app" || build[0].Shell != "bash" {
		t.Errorf("pipeline defaults not applied: %+v", build[0])
	}
	if build[1].WorkDir != "other" || build[1].Shell != "bash" {
		t.Errorf("a step's own working-directory must win: %+v", build[1])
	}
	if build[2].WorkDir != "" || build[2].Shell != "" {
		t.Errorf("defaults.run must not reach a uses step: %+v", build[2])
	}
	if lint[0].Shell != "sh" || lint[0].WorkDir != "app" {
		t.Errorf("job defaults must override per key: %+v", lint[0])
	}
	if gh, _ := pl.Ext["gha"].(map[string]any); gh["defaults"] != nil {
		t.Errorf("defaults is consumed and must not also sit in Ext: %v", pl.Ext)
	}
}
