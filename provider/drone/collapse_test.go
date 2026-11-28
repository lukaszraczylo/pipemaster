package drone_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/drone"
	"github.com/lukaszraczylo/pipemaster/report"
)

func parseYMLProject(t *testing.T, yml string) (*ir.Project, *report.Report) {
	t.Helper()
	p, rep, err := drone.New().Parse(repo(map[string]string{".drone.yml": yml}))
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, yml)
	}
	return p, rep
}

// back emits p, applies edit to the file text and parses it again. A nil edit leaves the file as written.
func back(t *testing.T, p *ir.Project, edit func(string) string) (*ir.Project, *report.Report) {
	t.Helper()
	out, _ := emitText(t, p)
	if edit != nil {
		edited := edit(out)
		if edited == out {
			t.Fatalf("the edit changed nothing:\n%s", out)
		}
		out = edited
	}
	got, rep := parseYMLProject(t, out)
	return got, rep
}

func ignored(rep *report.Report, f caps.Feature) bool {
	return slices.ContainsFunc(rep.Entries, func(e report.Entry) bool {
		return e.Feature == f && e.Level == caps.Lost && strings.Contains(e.Detail, "marker ignored") &&
			strings.Contains(e.Detail, "edited after conversion")
	})
}

func jobOf(t *testing.T, p *ir.Project, id string) *ir.Job {
	t.Helper()
	for _, pl := range p.Pipelines {
		for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
			if j.ID == id {
				return j
			}
		}
	}
	t.Fatalf("no job %q in %+v", id, p.Pipelines)
	return nil
}

func TestMatrixCellsFoldIntoOneJob(t *testing.T) {
	j := gjob("test", gstep("run", "go test ${{ matrix.go }}"), gstep("two", "echo ${{ matrix.tag }}"))
	j.Matrix = &ir.Matrix{
		Axes:    []ir.Axis{{Name: "go", Values: []string{"1.21", "1.22"}}},
		Include: []ir.Combo{{{Key: "go", Value: "1.22"}, {Key: "tag", Value: "x"}}},
	}
	after := gjob("deploy", gstep("ship", "make"))
	after.Needs = []ir.Need{{JobID: "test"}}
	p, rep := back(t, gproject(ghaOrigin, j, after), nil)
	if len(p.Pipelines[0].Jobs) != 2 {
		t.Fatalf("want 2 jobs, got %d", len(p.Pipelines[0].Jobs))
	}
	got := jobOf(t, p, "test")
	if got.Matrix == nil || len(ir.Expand(got.Matrix)) != 2 {
		t.Fatalf("matrix = %+v", got.Matrix)
	}
	if s := got.Steps[0].Script[0]; s != "go test ${{ matrix.go }}" {
		t.Errorf("script = %q", s)
	}
	if s := got.Steps[1].Script[0]; s != "echo ${{ matrix.tag }}" {
		t.Errorf("script = %q", s)
	}
	if n := jobOf(t, p, "deploy").Needs; len(n) != 1 || n[0].JobID != "test" {
		t.Errorf("needs = %+v", n)
	}
	if ignored(rep, caps.JobMatrix) {
		t.Errorf("a false marker-ignored entry: %+v", rep.Entries)
	}
}

func TestEditedMatrixCellKeepsTheDocuments(t *testing.T) {
	cases := map[string]func(string) string{
		"a cell command": func(s string) string { return strings.Replace(s, "go test 1.22", "go test 1.99", 1) },
		"a cell removed": func(s string) string {
			i := strings.LastIndex(s, "\n---\n")
			j := strings.Index(s[i+5:], "\n---\n")
			if j < 0 {
				return s[:i+1]
			}
			return s[:i+1] + s[i+5+j+5:]
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			j := gjob("test", gstep("run", "go test ${{ matrix.go }}"))
			j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "go", Values: []string{"1.21", "1.22"}}}}
			p, rep := back(t, gproject(ghaOrigin, j), edit)
			if p.Pipelines[0].Jobs[0].Matrix != nil {
				t.Errorf("the marker restored the matrix over the edit")
			}
			if !ignored(rep, caps.JobMatrix) {
				t.Errorf("no marker-ignored entry: %+v", rep.Entries)
			}
		})
	}
}

func TestAllowFailureReturnsToTheJob(t *testing.T) {
	mk := func() *ir.Project {
		j := gjob("b", gstep("x", "a"), gstep("y", "b"))
		j.AllowFailure = true
		return gproject(ghaOrigin, j)
	}
	p, rep := back(t, mk(), nil)
	j := jobOf(t, p, "b")
	if !j.AllowFailure || j.Steps[0].ContinueOnError || j.Steps[1].ContinueOnError {
		t.Errorf("job %+v", j)
	}
	if ignored(rep, caps.JobAllowFailure) {
		t.Errorf("false marker-ignored entry")
	}
	p, rep = back(t, mk(), func(s string) string { return strings.Replace(s, "    failure: ignore\n", "", 1) })
	if jobOf(t, p, "b").AllowFailure || !ignored(rep, caps.JobAllowFailure) {
		t.Errorf("an edited failure policy must stand: %+v", rep.Entries)
	}
}

func envProject() *ir.Project {
	j := gjob("b", gstep("x", "a"))
	j.Env = []ir.Var{{Name: "KEEP", Value: ir.Value{Lit: "one"}}, {Name: "TOKEN", Value: ir.Value{Kind: ir.ValueSecret, Secret: "tok"}}}
	p := gproject(ghaOrigin, j)
	p.Pipelines[0].Env = []ir.Var{{Name: "GOFLAGS", Value: ir.Value{Lit: "-x"}}, {Name: "KEY", Value: ir.Value{Kind: ir.ValueSecret, Secret: "key"}}}
	return p
}

func TestEnvCopiesReturnToTheirScope(t *testing.T) {
	p, rep := back(t, envProject(), nil)
	j := jobOf(t, p, "b")
	if len(j.Env) != 2 || j.Env[0].Name != "KEEP" || j.Env[1].Name != "TOKEN" || j.Env[1].Value.Kind != ir.ValueSecret {
		t.Errorf("job env = %+v", j.Env)
	}
	if len(j.Steps[0].Env) != 0 {
		t.Errorf("step env = %+v", j.Steps[0].Env)
	}
	if env := p.Pipelines[0].Env; len(env) != 2 || env[0].Name != "GOFLAGS" || env[1].Name != "KEY" {
		t.Errorf("pipeline env = %+v", env)
	}
	if ignored(rep, caps.JobEnv) || ignored(rep, caps.PipelineEnv) {
		t.Errorf("false marker-ignored entry: %+v", rep.Entries)
	}
}

func TestEnvWithoutSecretsReturnsToThePipeline(t *testing.T) {
	j := gjob("b", gstep("x", "a"))
	j.Env = []ir.Var{{Name: "KEEP", Value: ir.Value{Lit: "one"}}}
	p := gproject(ghaOrigin, j)
	p.Pipelines[0].Env = []ir.Var{{Name: "GOFLAGS", Value: ir.Value{Lit: "-x"}}}
	got, _ := back(t, p, nil)
	if env := jobOf(t, got, "b").Env; len(env) != 1 || env[0].Name != "KEEP" {
		t.Errorf("job env = %+v", env)
	}
	if env := got.Pipelines[0].Env; len(env) != 1 || env[0].Name != "GOFLAGS" {
		t.Errorf("pipeline env = %+v", env)
	}
}

func TestEditedEnvKeepsTheParsedValue(t *testing.T) {
	cases := map[string]func(string) string{
		"a secret removed from a step": func(s string) string {
			return regexp.MustCompile(`\n +TOKEN:\n +from_secret: tok`).ReplaceAllLiteralString(s, "")
		},
		"the document environment emptied": func(s string) string { return strings.Replace(s, "  KEEP: one\n", "", 1) },
		"a job value changed":              func(s string) string { return strings.Replace(s, "  KEEP: one\n", "  KEEP: two\n", 1) },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			out, _ := emitText(t, envProject())
			if edit(out) == out {
				t.Fatalf("edit does not apply to:\n%s", out)
			}
			_, rep := back(t, envProject(), edit)
			if !ignored(rep, caps.JobEnv) {
				t.Errorf("no marker-ignored entry: %+v", rep.Entries)
			}
		})
	}
}

func TestContainerCacheAndArtifactsReturn(t *testing.T) {
	j := gjob("b", &ir.Step{ID: "x", Kind: ir.StepScript, Script: []string{"a"}})
	j.Container = &ir.Image{Ref: "node:22"}
	j.Cache = []ir.Cache{{Key: "k", Paths: []string{"~/.m2"}}}
	j.Artifacts = []ir.Artifact{{Name: "dist", Paths: []string{"dist/"}}}
	p, _ := back(t, gproject(ghaOrigin, j), nil)
	got := jobOf(t, p, "b")
	if got.Container == nil || got.Container.Ref != "node:22" || got.Steps[0].Image != nil {
		t.Errorf("container = %+v, step image = %+v", got.Container, got.Steps[0].Image)
	}
	if len(got.Cache) != 1 || len(got.Artifacts) != 1 {
		t.Errorf("cache %+v artifacts %+v", got.Cache, got.Artifacts)
	}
}

func TestNoImageJobDropsTheDefaultImage(t *testing.T) {
	j := gjob("b", &ir.Step{ID: "x", Kind: ir.StepScript, Script: []string{"a"}})
	p, _ := back(t, gproject(ghaOrigin, j), nil)
	if img := jobOf(t, p, "b").Steps[0].Image; img != nil {
		t.Errorf("step image = %+v", img)
	}
}

func TestPlaceholderStepIsDropped(t *testing.T) {
	p, _ := back(t, gproject(ghaOrigin, gjob("b")), nil)
	if st := jobOf(t, p, "b").Steps; len(st) != 0 {
		t.Errorf("steps = %+v", st)
	}
}

func TestEmptyPipelineHasNoJob(t *testing.T) {
	p, _ := back(t, gproject(ghaOrigin), nil)
	if n := len(p.Pipelines[0].Jobs); n != 0 {
		t.Errorf("want no job, got %d", n)
	}
}

func TestSecretReferencesReturn(t *testing.T) {
	p, rep := back(t, gproject(ghaOrigin, gjob("b", gstep("x", "curl -H ${{ secrets.TOKEN }}"))), nil)
	st := jobOf(t, p, "b").Steps[0]
	if st.Script[0] != "curl -H ${{ secrets.TOKEN }}" || len(st.Env) != 0 {
		t.Errorf("script %q env %+v", st.Script, st.Env)
	}
	if hasEntry(rep, "marker not collapsed") {
		t.Errorf("entries: %+v", rep.Entries)
	}
}

func guardJobs() *ir.Project {
	c := or(eq(ref(expr.CtxEvent), "pull_request"), eq(ref(expr.CtxBranch), "1.0"))
	s := gstep("x", "one", "two")
	s.If = c
	return gproject("drone", gjob("b", s, gstep("y", "three")))
}

func TestStepGuardReturnsAsTheCondition(t *testing.T) {
	p, rep := back(t, guardJobs(), nil)
	j := jobOf(t, p, "b")
	if j.Steps[0].If == nil || !slices.Equal(j.Steps[0].Script, []string{"one", "two"}) {
		t.Errorf("step %+v", j.Steps[0])
	}
	if hasEntry(rep, "marker not collapsed") || ignored(rep, caps.StepIf) {
		t.Errorf("entries: %+v", rep.Entries)
	}
}

func TestEditedStepGuardStays(t *testing.T) {
	p, rep := back(t, guardJobs(), func(s string) string {
		return strings.Replace(s, "exit 0; fi # pipemaster:guard", "exit 1; fi # pipemaster:guard", 1)
	})
	j := jobOf(t, p, "b")
	if j.Steps[0].If != nil || !ignored(rep, caps.StepIf) {
		t.Errorf("an edited guard must stand: %+v %+v", j.Steps[0], rep.Entries)
	}
}

func jobGuard() *ir.Project {
	j := gjob("b", gstep("x", "1"), gstep("y", "2"))
	j.If = or(eq(ref(expr.CtxEvent), "pull_request"), eq(ref(expr.CtxBranch), "1.0"))
	return gproject("drone", j)
}

func TestJobGuardReturnsAsTheCondition(t *testing.T) {
	p, _ := back(t, jobGuard(), nil)
	j := jobOf(t, p, "b")
	if j.If == nil || !slices.Equal(j.Steps[0].Script, []string{"1"}) || !slices.Equal(j.Steps[1].Script, []string{"2"}) {
		t.Errorf("job %+v step %+v", j.If, j.Steps)
	}
}

func TestEditedJobGuardStays(t *testing.T) {
	p, rep := back(t, jobGuard(), func(s string) string {
		return strings.Replace(s, "if ! ", "if ", 1)
	})
	if jobOf(t, p, "b").If != nil || !ignored(rep, caps.JobIf) {
		t.Errorf("an edited guard must stand: %+v", rep.Entries)
	}
}

func TestFinallyJobReturns(t *testing.T) {
	p := gproject(ghaOrigin, gjob("build", gstep("x", "a")))
	p.Pipelines[0].Finally = []*ir.Job{gjob("notify", gstep("n", "b"))}
	p.Pipelines[0].Finally[0].Needs = []ir.Need{{JobID: "build"}}
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}}
	got, _ := back(t, p, nil)
	pl := got.Pipelines[0]
	if len(pl.Jobs) != 1 || len(pl.Finally) != 1 || pl.Finally[0].ID != "notify" || pl.Finally[0].If != nil {
		t.Fatalf("jobs %d finally %+v", len(pl.Jobs), pl.Finally)
	}
	if n := pl.Finally[0].Needs; len(n) != 1 || n[0].JobID != "build" {
		t.Errorf("needs = %+v", n)
	}
	if len(pl.Triggers) != 1 || pl.Triggers[0].Kind != ir.TriggerPush {
		t.Errorf("triggers = %+v", pl.Triggers)
	}
}

func TestMultiPipelineReturns(t *testing.T) {
	a := &ir.Pipeline{ID: "ci", Jobs: []*ir.Job{gjob("test", gstep("t", "a"))}}
	b := &ir.Pipeline{ID: "deploy", Jobs: []*ir.Job{gjob("ship", gstep("s", "b"))}}
	b.Jobs[0].Needs = nil
	p := &ir.Project{Origin: ir.Origin{Provider: ghaOrigin, Dialect: ghaOrigin}, Pipelines: []*ir.Pipeline{a, b}}
	got, _ := back(t, p, nil)
	if len(got.Pipelines) != 2 || got.Pipelines[0].ID != "ci" || got.Pipelines[1].ID != "deploy" {
		t.Fatalf("pipelines = %+v", got.Pipelines)
	}
	if got.Pipelines[0].Jobs[0].ID != "test" || got.Pipelines[1].Jobs[0].ID != "ship" {
		t.Errorf("jobs %q %q", got.Pipelines[0].Jobs[0].ID, got.Pipelines[1].Jobs[0].ID)
	}
}

func TestRefTriggersReturnFromTheMarker(t *testing.T) {
	trs := []ir.Trigger{
		{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}}},
		{Kind: ir.TriggerPR},
		{Kind: ir.TriggerTag},
	}
	mk := func() *ir.Project {
		p := gproject(ghaOrigin, gjob("b", gstep("x", "a")))
		p.Pipelines[0].Triggers = trs
		return p
	}
	got, _ := back(t, mk(), nil)
	if pl := got.Pipelines[0]; len(pl.Triggers) != 3 || pl.Jobs[0].If != nil || !slices.ContainsFunc(pl.Triggers, func(tr ir.Trigger) bool { return len(tr.Branches.Include) == 1 }) {
		t.Errorf("triggers %+v if %v", pl.Triggers, pl.Jobs[0].If)
	}
	got, rep := back(t, mk(), func(s string) string { return strings.Replace(s, "refs/heads/main", "refs/heads/dev", 1) })
	if pl := got.Pipelines[0]; len(pl.Triggers) != 0 || pl.Jobs[0].If == nil {
		t.Errorf("an edited ref must stand: %+v", pl.Triggers)
	}
	if !ignored(rep, caps.TriggerFilterBranch) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

func TestManualJobAndApprovalReturn(t *testing.T) {
	man := gjob("deploy", gstep("x", "a"))
	man.Manual = true
	ap := gjob("promote", gstep("y", "b"))
	ap.Environment = &ir.Environment{Name: "prod", Approval: true}
	p := gproject(ghaOrigin, man, ap)
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}}
	got, _ := back(t, p, nil)
	m, a := jobOf(t, got, "deploy"), jobOf(t, got, "promote")
	if !m.Manual || m.If != nil {
		t.Errorf("manual job = %+v if %v", m.Manual, m.If)
	}
	if a.Environment == nil || !a.Environment.Approval || a.Environment.Name != "prod" || a.If != nil {
		t.Errorf("approval job = %+v if %v", a.Environment, a.If)
	}
	if tr := got.Pipelines[0].Triggers; len(tr) != 1 || tr[0].Kind != ir.TriggerPush {
		t.Errorf("triggers = %+v", tr)
	}
}

func TestActionStepReturns(t *testing.T) {
	j := gjob("b", action("co", "actions", "checkout", "v4", nil), gstep("x", "a"))
	p, _ := back(t, gproject(ghaOrigin, j), nil)
	got := jobOf(t, p, "b")
	if len(got.Steps) != 2 || got.Steps[0].ID != "co" || got.Steps[0].Action == nil || got.Steps[0].Action.Repo != "checkout" {
		t.Errorf("steps = %+v", got.Steps)
	}
}

func TestMarkerLinesAreNotNeededForAPlainFile(t *testing.T) {
	pl, rep := parseYML(t, minimal)
	if len(pl.Jobs) != 1 || hasEntry(rep, "marker") {
		t.Errorf("a file with no marker parses as before: %+v", rep.Entries)
	}
}

func TestMatrixTextInEnvAndServicesReturns(t *testing.T) {
	matrixRef := func() ir.Value { return ir.Value{Kind: ir.ValueExpr, Expr: ref("matrix", "go")} }
	st := gstep("run", "go test")
	st.Env = []ir.Var{{Name: "GOV", Value: matrixRef()}}
	j := gjob("test", st)
	j.Env = []ir.Var{{Name: "JOBGO", Value: matrixRef()}}
	j.Services = []ir.Service{{Name: "db", Image: ir.Image{Ref: "postgres:${{ matrix.go }}"}}}
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "go", Values: []string{"1.21", "1.22"}}}}
	p, rep := back(t, gproject(ghaOrigin, j), nil)
	got := jobOf(t, p, "test")
	if got.Matrix == nil || ignored(rep, caps.JobMatrix) {
		t.Fatalf("the cells did not fold: %+v", rep.Entries)
	}
	if v := got.Steps[0].Env[0].Value; v.Kind != ir.ValueExpr || expr.String(v.Expr) != "matrix.go" {
		t.Errorf("step env = %+v", v)
	}
	if v := got.Env[0].Value; v.Kind != ir.ValueExpr {
		t.Errorf("job env = %+v", v)
	}
	if r := got.Services[0].Image.Ref; r != "postgres:${{ matrix.go }}" {
		t.Errorf("service image = %q", r)
	}
}

func TestEnvReferencesInCommandsReturn(t *testing.T) {
	p, _ := back(t, gproject(ghaOrigin, gjob("b", gstep("x", "echo ${{ env.REGION }}"))), nil)
	if s := jobOf(t, p, "b").Steps[0].Script[0]; s != "echo ${{ env.REGION }}" {
		t.Errorf("script = %q", s)
	}
}

func TestRenamedJobIDReturns(t *testing.T) {
	a, b := gjob("docs test", gstep("x", "a")), gjob("ship", gstep("y", "b"))
	b.Needs = []ir.Need{{JobID: "docs test"}}
	p, _ := back(t, gproject(ghaOrigin, a, b), nil)
	if got := jobOf(t, p, "ship"); len(got.Needs) != 1 || got.Needs[0].JobID != "docs test" {
		t.Fatalf("needs = %+v", got.Needs)
	}
	jobOf(t, p, "docs test")
}

func TestAlwaysJobBesideRefTriggersReturns(t *testing.T) {
	b, c := gjob("b", gstep("x", "a")), gjob("c", gstep("y", "b"))
	c.Needs = []ir.Need{{JobID: "b"}}
	c.If = expr.Call{Name: "always"}
	p := gproject(ghaOrigin, b, c)
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPR}, {Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"master"}}}}
	got, _ := back(t, p, nil)
	pl := got.Pipelines[0]
	if len(pl.Triggers) != 2 || jobOf(t, got, "b").If != nil {
		t.Errorf("triggers %+v, b.If %v", pl.Triggers, jobOf(t, got, "b").If)
	}
	if x := jobOf(t, got, "c").If; x == nil || expr.String(x) != "always()" {
		t.Errorf("c.If = %v", x)
	}
}

func TestDependsOnEmptyListIsARoot(t *testing.T) {
	pl, _ := parseYML(t, "kind: pipeline\nname: p\nsteps:\n- {name: a, image: x, depends_on: []}\n- {name: b, image: x, depends_on: [a]}\n")
	if pl.Jobs[0].Steps[0].Ext != nil {
		t.Errorf("a root step carries no depends_on: %+v", pl.Jobs[0].Steps[0].Ext)
	}
}

func TestNeverRunningJobKeepsThePipelineTriggers(t *testing.T) {
	j := gjob("b", gstep("x", "a"))
	j.If = expr.Lit{Kind: expr.LitBool, Bool: false}
	p := gproject(ghaOrigin, j)
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag}}
	got, _ := back(t, p, nil)
	if n := len(got.Pipelines[0].Triggers); n != 2 {
		t.Errorf("triggers = %+v", got.Pipelines[0].Triggers)
	}
}

func matrixPluginJob() *ir.Job {
	pl := &ir.Step{
		ID: "notify", Kind: ir.StepPlugin, Image: &ir.Image{Ref: "plugins/slack"},
		With: map[string]ir.Value{"k": {Kind: ir.ValueExpr, Expr: ref("matrix", "go")}},
	}
	j := gjob("test", pl)
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "go", Values: []string{"1.21", "1.22"}}}}
	return j
}

func TestEditedPluginCellsWithoutSettingsKeepTheDocuments(t *testing.T) {
	cut := func(s string) string {
		var out []string
		skip := false
		for _, l := range strings.Split(s, "\n") {
			switch {
			case strings.HasPrefix(l, "    settings:"):
				skip = true
			case skip && strings.HasPrefix(l, "      "):
			default:
				skip = false
				out = append(out, l)
			}
		}
		return strings.Join(out, "\n")
	}
	p, rep := back(t, gproject(ghaOrigin, matrixPluginJob()), cut)
	if p.Pipelines[0].Jobs[0].Matrix != nil {
		t.Errorf("the marker restored the matrix over the edit")
	}
	if !ignored(rep, caps.JobMatrix) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

func TestScriptStepHoldingSettingsFoldsWithoutPanic(t *testing.T) {
	st := gstep("run", "go test")
	st.With = map[string]ir.Value{"k": {Kind: ir.ValueExpr, Expr: ref("matrix", "go")}}
	j := gjob("test", st)
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "go", Values: []string{"1.21", "1.22"}}}}
	p, _ := back(t, gproject(ghaOrigin, j), nil)
	if got := jobOf(t, p, "test"); got.Matrix == nil || len(got.Steps[0].With) != 1 {
		t.Errorf("job = %+v", got)
	}
}

func TestEditedSecretReferenceKeepsTheFile(t *testing.T) {
	p, rep := back(t, gproject(ghaOrigin, gjob("b", gstep("x", "curl -H ${{ secrets.TOKEN }}"))),
		func(s string) string { return strings.Replace(s, "$${TOKEN}", "plain", 1) })
	st := jobOf(t, p, "b").Steps[0]
	if st.Script[0] != "curl -H plain" || len(st.Env) != 1 {
		t.Errorf("script %q env %+v", st.Script, st.Env)
	}
	if !ignored(rep, caps.ScriptInterpolation) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

func TestEditedContainerImageKeepsTheFile(t *testing.T) {
	j := gjob("b", &ir.Step{ID: "x", Kind: ir.StepScript, Script: []string{"a"}})
	j.Container = &ir.Image{Ref: "node:22"}
	p, rep := back(t, gproject(ghaOrigin, j), func(s string) string {
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			if !strings.HasPrefix(l, "#") {
				lines[i] = strings.ReplaceAll(l, "node:22", "node:99")
			}
		}
		return strings.Join(lines, "\n")
	})
	got := jobOf(t, p, "b")
	if got.Container != nil || got.Steps[0].Image == nil || got.Steps[0].Image.Ref != "node:99" {
		t.Errorf("container = %+v, step image = %+v", got.Container, got.Steps[0].Image)
	}
	if !ignored(rep, caps.JobContainer) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

func TestEditedGlobFilterStays(t *testing.T) {
	mk := func() *ir.Project {
		p := gproject(ghaOrigin, gjob("b", gstep("x", "a")))
		p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main", "feat/**"}}}}
		return p
	}
	got, rep := back(t, mk(), func(s string) string { return strings.Replace(s, "    - feat/**\n", "    - feat/*\n", 1) })
	if inc := got.Pipelines[0].Triggers[0].Branches.Include; !slices.Equal(inc, []string{"main", "feat/*"}) {
		t.Errorf("include = %v", inc)
	}
	if !ignored(rep, caps.TriggerFilterBranch) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

func TestEditedMergedConditionStays(t *testing.T) {
	j := gjob("b", gstep("x", "a"))
	j.If = expr.Lit{Kind: expr.LitBool, Bool: false}
	p := gproject(ghaOrigin, j)
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag}}
	got, rep := back(t, p, func(s string) string { return strings.Replace(s, "- '**'", "- push", 1) })
	if len(got.Pipelines[0].Triggers) != 0 {
		t.Errorf("an edited condition must not restore triggers: %+v", got.Pipelines[0].Triggers)
	}
	if !ignored(rep, caps.JobIf) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

func TestEditedGateKeepsTheFile(t *testing.T) {
	man := gjob("deploy", gstep("x", "a"))
	man.Manual = true
	p := gproject(ghaOrigin, man)
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}}
	got, rep := back(t, p, func(s string) string { return strings.Replace(s, "- custom", "- push", 1) })
	if jobOf(t, got, "deploy").Manual {
		t.Errorf("an edited gate must not restore the manual flag")
	}
	if !ignored(rep, caps.JobManual) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

func TestEditedApprovalGateKeepsTheFile(t *testing.T) {
	ap := gjob("promote", gstep("y", "b"))
	ap.Environment = &ir.Environment{Name: "prod", Approval: true}
	p := gproject(ghaOrigin, ap)
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}}
	got, rep := back(t, p, func(s string) string { return strings.Replace(s, "- promote", "- push", 1) })
	if e := jobOf(t, got, "promote").Environment; e != nil && e.Approval {
		t.Errorf("an edited gate must not restore the approval: %+v", e)
	}
	if !ignored(rep, caps.JobEnvironmentApproval) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

func TestContainerMarkerWithoutTakenStillRestores(t *testing.T) {
	j := gjob("b", &ir.Step{ID: "x", Kind: ir.StepScript, Script: []string{"a"}})
	j.Container = &ir.Image{Ref: "node:22"}
	p, rep := back(t, gproject(ghaOrigin, j), func(s string) string { return regexp.MustCompile(`,"taken":\d+`).ReplaceAllString(s, "") })
	got := jobOf(t, p, "b")
	if got.Container == nil || got.Container.Ref != "node:22" || got.Steps[0].Image != nil || ignored(rep, caps.JobContainer) {
		t.Errorf("container = %+v, step image = %+v, entries %+v", got.Container, got.Steps[0].Image, rep.Entries)
	}
}

// TestContainerMarkerMissesAnEditBesideAnEqualOwnImage documents a limit: a step that names the container image
// itself counts as a step that took it, so editing the other step is not detected.
func TestContainerMarkerMissesAnEditBesideAnEqualOwnImage(t *testing.T) {
	j := gjob("b",
		&ir.Step{ID: "own", Kind: ir.StepScript, Script: []string{"a"}, Image: &ir.Image{Ref: "node:22"}},
		&ir.Step{ID: "took", Kind: ir.StepScript, Script: []string{"b"}})
	j.Container = &ir.Image{Ref: "node:22"}
	p, rep := back(t, gproject(ghaOrigin, j), func(s string) string {
		i := strings.LastIndex(s, "image: node:22")
		return s[:i] + "image: node:99" + s[i+len("image: node:22"):]
	})
	got := jobOf(t, p, "b")
	if got.Container == nil || ignored(rep, caps.JobContainer) {
		t.Fatalf("the limit changed; update the report: container %+v entries %+v", got.Container, rep.Entries)
	}
	if st := got.Steps[1]; st.Image == nil || st.Image.Ref != "node:99" {
		t.Errorf("the edited step image must stay: %+v", st.Image)
	}
}

// lastCell applies edit to the last document of the file only.
func lastCell(edit func(string) string) func(string) string {
	return func(s string) string {
		i := strings.LastIndex(s, "\n---\n") + 5
		return s[:i] + edit(s[i:])
	}
}

func TestEditInLaterMatrixCellKeepsTheCells(t *testing.T) {
	cases := map[string]func(string) string{
		"a plain command": func(s string) string { return strings.Replace(s, "make test", "make test-race", 1) },
		"an added when": func(s string) string {
			return strings.Replace(s, "'make test'\n", "'make test'\n    when:\n      branch:\n        - main\n", 1)
		},
		"the image": func(s string) string { return strings.Replace(s, "image: alpine:3", "image: busybox", 1) },
		"the trigger": func(s string) string {
			return strings.Replace(s, "    - push\n    - tag\n", "    - pull_request\n", 1)
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			j := gjob("test", gstep("run", "go test ${{ matrix.go }}"), gstep("two", "make test"))
			j.Container = &ir.Image{Ref: "golang:1.22"}
			j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "go", Values: []string{"1.21", "1.22"}}}}
			pr := gproject(ghaOrigin, j)
			pr.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag}}
			p, rep := back(t, pr, lastCell(edit))
			if len(p.Pipelines[0].Jobs) != 2 {
				t.Errorf("the edited cell was folded away: %d jobs", len(p.Pipelines[0].Jobs))
			}
			if !ignored(rep, caps.JobMatrix) {
				t.Errorf("no marker-ignored entry: %+v", rep.Entries)
			}
		})
	}
}

func gatedProject() *ir.Project {
	man := gjob("deploy", gstep("x", "./deploy"))
	man.Manual = true
	p := gproject(ghaOrigin, gjob("build", gstep("b", "make")), man)
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}}}}
	return p
}

func TestEditedTriggerBesideManualGateKeepsTheFile(t *testing.T) {
	p, rep := back(t, gatedProject(), func(s string) string {
		i := strings.Index(s, "name: build")
		return s[:i] + strings.Replace(s[i:], "- push", "- pull_request", 1)
	})
	tr := p.Pipelines[0].Triggers
	if len(tr) != 1 || tr[0].Kind != ir.TriggerPR {
		t.Errorf("the gate marker replaced the edited triggers: %+v", tr)
	}
	if !ignored(rep, caps.JobManual) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

func TestWhenAddedBesideStepGuardKeepsBoth(t *testing.T) {
	p, rep := back(t, guardJobs(), func(s string) string {
		return strings.Replace(s, "      - two\n", "      - two\n    when:\n      branch:\n        - main\n", 1)
	})
	st := jobOf(t, p, "b").Steps[0]
	if st.If == nil || !strings.Contains(expr.String(st.If), "main") {
		t.Errorf("the author's filter was replaced: %v", st.If)
	}
	if !ignored(rep, caps.StepIf) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
	if !slices.ContainsFunc(st.Script, func(l string) bool { return strings.Contains(l, "pipemaster:guard") }) {
		t.Errorf("the guard line was dropped, so the step runs where the guard skipped it: %v", st.Script)
	}
}

func TestTriggerEditedBesideJobGuardKeepsTheGuard(t *testing.T) {
	j := gjob("deploy", gstep("x", "./deploy"))
	j.If = eq(ref(expr.CtxActor), "bob")
	p, rep := back(t, gproject(ghaOrigin, j), func(s string) string {
		return strings.Replace(s, "steps:\n", "trigger:\n  repo:\n    - acme/app\nsteps:\n", 1)
	})
	got := jobOf(t, p, "deploy")
	txt := expr.String(got.If)
	if !strings.Contains(txt, "bob") || !strings.Contains(txt, "acme/app") {
		t.Errorf("job condition %q lost the guard or the edited trigger; entries %+v", txt, rep.Entries)
	}
}

func TestFinallyJobGuardReturns(t *testing.T) {
	p := gproject(ghaOrigin, gjob("build", gstep("x", "a")))
	fin := gjob("notify", gstep("n", "b"))
	fin.Needs = []ir.Need{{JobID: "build"}}
	fin.If = eq(ref(expr.CtxActor), "bob")
	p.Pipelines[0].Finally = []*ir.Job{fin}
	got, _ := back(t, p, nil)
	if txt := expr.String(jobOf(t, got, "notify").If); !strings.Contains(txt, "bob") {
		t.Errorf("the finally guard was lost: %q", txt)
	}
}

func TestFoldedGuardRoundTripsExactly(t *testing.T) {
	cond := and(eq(ref(expr.CtxRefName), "main"), expr.Binary{Op: expr.OpNe, L: ref(expr.CtxActor), R: slit("bot")})
	for name, mk := range map[string]func() *ir.Project{
		"job": func() *ir.Project {
			j := gjob("deploy", gstep("x", "./deploy"))
			j.If = cond
			return gproject(ghaOrigin, j)
		},
		"step": func() *ir.Project {
			s := gstep("x", "./deploy")
			s.If = cond
			return gproject(ghaOrigin, gjob("deploy", s))
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, rep := back(t, mk(), nil)
			j := jobOf(t, got, "deploy")
			x := j.If
			if name == "step" {
				x = j.Steps[0].If
			}
			if x == nil || expr.String(x) != expr.String(cond) {
				t.Errorf("condition = %v, want %s; entries %+v", x, expr.String(cond), rep.Entries)
			}
		})
	}
}

func TestEditedEnvValueWins(t *testing.T) {
	p, _ := back(t, envProject(), func(s string) string { return strings.Replace(s, "  KEEP: one\n", "  KEEP: two\n", 1) })
	for _, v := range jobOf(t, p, "b").Env {
		if v.Name == "KEEP" && v.Value.Lit != "two" {
			t.Errorf("KEEP = %+v, want the edited value", v.Value)
		}
	}
}

func TestMatrixMarkerWithoutCellsOrWithBadPositionDoesNotPanic(t *testing.T) {
	doc := func(arg string) string {
		return "# pipemaster:emulated job.matrix pipeline/ci/job/test\n# pipemaster:arg " + arg + "\n" +
			"kind: pipeline\ntype: docker\nname: test\nsteps:\n  - name: s\n    image: alpine\n    commands:\n      - echo hi\n"
	}
	for name, arg := range map[string]string{
		"no cell, position past the steps": `{"matrix":{"Axes":[{"Name":"go","Values":[]}]},"job":"test","cell":[],"held":[{"script":["x"],"pos":99}],"index":0}`,
		"no cell, negative position":       `{"matrix":{"Axes":[{"Name":"go","Values":[]}]},"job":"test","cell":[],"held":[{"script":["x"],"pos":-1}],"index":0}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, rep := parseYMLProject(t, doc(arg))
			if got := jobOf(t, p, "test"); got.Matrix != nil {
				t.Errorf("the marker restored a matrix over an impossible position")
			}
			if !ignored(rep, caps.JobMatrix) {
				t.Errorf("no marker-ignored entry: %+v", rep.Entries)
			}
		})
	}
}

func TestShellVariableBesideSecretReferenceStaysAVariable(t *testing.T) {
	want := []string{"echo ${TOKEN}", "curl -H ${{ secrets.TOKEN }} ${TOKEN}", "echo ${{ secrets.TOKEN }}"}
	p, rep := back(t, gproject(ghaOrigin, gjob("b", gstep("x", want...))), nil)
	if got := jobOf(t, p, "b").Steps[0].Script; !slices.Equal(got, want) {
		t.Errorf("script %q, want %q", got, want)
	}
	if hasEntry(rep, "marker not collapsed") || ignored(rep, caps.ScriptInterpolation) {
		t.Errorf("entries: %+v", rep.Entries)
	}
}

func TestAddedVariableUseKeepsTheReferencesAsWritten(t *testing.T) {
	p, rep := back(t, gproject(ghaOrigin, gjob("b", gstep("x", "curl -H ${{ secrets.TOKEN }}"))),
		func(s string) string {
			return strings.Replace(s, "curl -H $${TOKEN}", "echo $${TOKEN} && curl -H $${TOKEN}", 1)
		})
	if got := jobOf(t, p, "b").Steps[0].Script[0]; strings.Contains(got, "${{") {
		t.Errorf("the parser guessed which occurrence the conversion made: %q", got)
	}
	if !ignored(rep, caps.ScriptInterpolation) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}
