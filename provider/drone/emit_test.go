package drone_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	dexpr "github.com/lukaszraczylo/pipemaster/expr/drone"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/drone"
	"github.com/lukaszraczylo/pipemaster/report"
)

func ref(p ...string) expr.Ref { return expr.Ref{Path: p} }

func slit(s string) expr.Lit { return expr.Lit{Kind: expr.LitStr, Str: s} }

func eq(l expr.Expr, s string) expr.Expr { return expr.Binary{Op: expr.OpEq, L: l, R: slit(s)} }

func or(a, b expr.Expr) expr.Expr  { return expr.Binary{Op: expr.OpOr, L: a, R: b} }
func and(a, b expr.Expr) expr.Expr { return expr.Binary{Op: expr.OpAnd, L: a, R: b} }

func TestEmitNil(t *testing.T) {
	files, rep, err := drone.New().Emit(nil)
	if err != nil {
		t.Fatalf("Emit(nil): %v", err)
	}
	if len(files) != 0 || rep == nil {
		t.Fatalf("Emit(nil) = %d files, report %v", len(files), rep)
	}
}

func TestDocumentHeader(t *testing.T) {
	out, _ := emitText(t, gproject(ghaOrigin, gjob("build", gstep("test", "go test ./..."))))
	d := docsOf(t, out)[0]
	if d["kind"] != "pipeline" || d["type"] != "docker" || d["name"] != "build" {
		t.Fatalf("header = %v %v %v", d["kind"], d["type"], d["name"])
	}
	if _, has := d["environment"]; has {
		t.Fatalf("environment = %#v; a pipeline without variables has no environment key, which a kubernetes pipeline does not allow", d["environment"])
	}
}

func TestOneDocumentPerJob(t *testing.T) {
	j2 := gjob("deploy", gstep("ship", "make ship"))
	j2.Needs = []ir.Need{{JobID: "build"}}
	out, _ := emitText(t, gproject(ghaOrigin, gjob("build", gstep("test", "go test")), j2))
	docs := docsOf(t, out)
	if len(docs) != 2 || countLines(out, "---") != 1 {
		t.Fatalf("want two documents joined by one separator, got %d\n%s", len(docs), out)
	}
	dep, _ := docNamed(t, docs, "deploy")["depends_on"].([]any)
	if len(dep) != 1 || dep[0] != "build" {
		t.Fatalf("depends_on = %v", dep)
	}
}

func TestSequentialStepsHaveNoDependsOn(t *testing.T) {
	out, _ := emitText(t, gproject(ghaOrigin, gjob("build", gstep("a", "one"), gstep("b", "two"), gstep("c", "three"))))
	for _, s := range stepsOf(docsOf(t, out)[0]) {
		if _, has := s["depends_on"]; has {
			t.Fatalf("a sequential step has depends_on: %v", s)
		}
	}
}

func TestStepDAGPutsDependsOnOnEveryStep(t *testing.T) {
	root, mid, leaf := gstep("root", "a"), gstep("mid", "b"), gstep("leaf", "c")
	mid.Ext = ir.Ext{"drone": map[string]any{"depends_on": []any{"root"}}}
	leaf.Ext = ir.Ext{"drone": map[string]any{"depends_on": []any{"root", "mid"}}}
	out, _ := emitText(t, gproject("drone", gjob("build", root, mid, leaf)))
	steps := stepsOf(docsOf(t, out)[0])
	want := map[string]int{"root": 0, "mid": 1, "leaf": 2}
	for _, s := range steps {
		dep, has := s["depends_on"].([]any)
		if !has {
			t.Fatalf("step %v lacks depends_on\n%s", s["name"], out)
		}
		if len(dep) != want[s["name"].(string)] {
			t.Fatalf("step %v depends_on = %v", s["name"], dep)
		}
	}
	if !strings.Contains(out, "depends_on: []") {
		t.Fatalf("the root step needs an empty list:\n%s", out)
	}
}

func TestMatrixCells(t *testing.T) {
	j := gjob("test", gstep("run", "go test ${{ matrix.go }} ${{ matrix.os }}"))
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "go", Values: []string{"1.21", "1.22"}}, {Name: "os", Values: []string{"a b", "a-b"}}}}
	after := gjob("deploy", gstep("ship", "make"))
	after.Needs = []ir.Need{{JobID: "test"}}
	out, rep := emitText(t, gproject(ghaOrigin, j, after))
	docs := docsOf(t, out)
	if len(docs) != 5 {
		t.Fatalf("want four cells and one job, got %d\n%s", len(docs), out)
	}
	seen := map[string]bool{}
	for _, d := range docs {
		name := d["name"].(string)
		if name == "deploy" {
			continue
		}
		if seen[name] || !strings.HasPrefix(name, "test-1.2") {
			t.Fatalf("cell name %q is a duplicate or has the wrong form", name)
		}
		seen[name] = true
	}
	if len(seen) != 4 {
		t.Fatalf("want 4 distinct cells, got %v", seen)
	}
	dep := docNamed(t, docs, "deploy")["depends_on"].([]any)
	if len(dep) != 4 {
		t.Fatalf("depends_on lists %d cells, want 4: %v", len(dep), dep)
	}
	for _, d := range dep {
		if !seen[d.(string)] {
			t.Fatalf("depends_on names %v, which is no cell", d)
		}
	}
	cmd := stepsOf(docNamed(t, docs, "test-1.21-a-b"))[0]["commands"].([]any)[0].(string)
	if !strings.Contains(cmd, "go test 1.21 a") {
		t.Fatalf("cell values are not substituted: %q", cmd)
	}
	if n := countLines(out, "# pipemaster:emulated job.matrix pipeline/drone/job/test"); n != 4 {
		t.Fatalf("want one marker per cell, got %d\n%s", n, out)
	}
	for _, c := range []string{`"job":"test"`, `"Key":"go"`} {
		if !strings.Contains(out, c) {
			t.Fatalf("a cell marker lacks %s\n%s", c, out)
		}
	}
	wantLevel(t, rep, caps.JobMatrix, "job/test", caps.Emulated)
}

func TestMatrixCellNamesNeverCollide(t *testing.T) {
	j := gjob("t", gstep("run", "true"))
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "v", Values: []string{"a b", "a-b", "a/b", "a_b"}}}}
	clash := gjob("t-a-b", gstep("run", "true"))
	out, _ := emitText(t, gproject(ghaOrigin, j, clash))
	seen := map[string]bool{}
	for _, d := range docsOf(t, out) {
		n := d["name"].(string)
		if seen[n] {
			t.Fatalf("document name %q repeats\n%s", n, out)
		}
		seen[n] = true
	}
	if len(seen) != 5 {
		t.Fatalf("want 5 documents, got %d", len(seen))
	}
}

func TestMatrixNamesStayWithinTheLimit(t *testing.T) {
	long := strings.Repeat("x", 120)
	j := gjob("t", gstep("run", "true"))
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "v", Values: []string{long + "1", long + "2"}}}}
	out, _ := emitText(t, gproject(ghaOrigin, j))
	seen := map[string]bool{}
	for _, d := range docsOf(t, out) {
		n := d["name"].(string)
		if len(n) > 100 || seen[n] {
			t.Fatalf("name %q has %d bytes or repeats", n, len(n))
		}
		seen[n] = true
	}
}

func TestMatrixSettingsAreLost(t *testing.T) {
	ff := true
	j := gjob("t", gstep("run", "true"))
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "v", Values: []string{"1"}}}, FailFast: &ff, MaxParallel: 2}
	_, rep := emitText(t, gproject(ghaOrigin, j))
	if !hasEntry(rep, "fail-fast") || !hasEntry(rep, "max-parallel") {
		t.Fatalf("the report lacks the lost matrix settings: %v", rep.Entries)
	}
}

func TestDollarEscape(t *testing.T) {
	st := gstep("run", "echo $HOME ${{ sha }}")
	st.Env = []ir.Var{{Name: "P", Value: ir.Value{Lit: "a$b"}}}
	out, _ := emitText(t, gproject(ghaOrigin, gjob("build", st)))
	if !strings.Contains(out, "echo $$HOME $${DRONE_COMMIT_SHA}") || !strings.Contains(out, "P: a$$b") {
		t.Fatalf("$ is not escaped:\n%s", out)
	}
	back := reparse(t, out).Pipelines[0].Jobs[0].Steps[0]
	if back.Script[0] != "echo $HOME ${{ sha }}" || back.Env[0].Value.Lit != "a$b" {
		t.Fatalf("parse does not invert the escape: %q %v", back.Script, back.Env)
	}
}

func TestEntrypointOnCommandStep(t *testing.T) {
	keep := gstep("keep", "true")
	keep.Image.Entrypoint = []string{""}
	lose := gstep("lose", "true")
	lose.Image.Entrypoint = []string{"/bin/bash", "-c"}
	plugin := &ir.Step{ID: "plug", Kind: ir.StepPlugin, Image: &ir.Image{Ref: "plugins/x", Entrypoint: []string{"/run"}}, With: map[string]ir.Value{"a": {Lit: "b"}}}
	out, rep := emitText(t, gproject(ghaOrigin, gjob("build", keep, lose, plugin)))
	steps := stepsOf(docsOf(t, out)[0])
	if _, has := steps[0]["entrypoint"]; has {
		t.Fatalf("an empty entrypoint changes nothing on a command step and is not written: %v", steps[0])
	}
	if _, has := steps[1]["entrypoint"]; has {
		t.Fatalf("a replaced entrypoint is written: %v", steps[1])
	}
	if ep, _ := steps[2]["entrypoint"].([]any); len(ep) != 1 || ep[0] != "/run" {
		t.Fatalf("a plugin step keeps its entrypoint: %v", steps[2])
	}
	wantLevel(t, rep, caps.JobContainer, "step/lose", caps.Lost)
	wantLevel(t, rep, caps.StepImage, "step/keep", caps.Lost)
}

func TestJobContainerEntrypointIsLostForInheritingSteps(t *testing.T) {
	j := gjob("build", &ir.Step{ID: "a", Kind: ir.StepScript, Script: []string{"true"}})
	j.Container = &ir.Image{Ref: "golang:1.22", Entrypoint: []string{"/x"}}
	out, rep := emitText(t, gproject(ghaOrigin, j))
	if !strings.Contains(out, "image: golang:1.22") || strings.Contains(out, "entrypoint:\n") || hasLine(out, "    entrypoint") || strings.Contains(out, "      entrypoint") {
		t.Fatalf("the step takes the image and not the entrypoint:\n%s", out)
	}
	wantLevel(t, rep, caps.JobContainer, "job/build", caps.Lost)
}

func TestDefaultImageIsReported(t *testing.T) {
	out, rep := emitText(t, gproject(ghaOrigin, gjob("build", &ir.Step{ID: "a", Kind: ir.StepScript, Script: []string{"true"}})))
	if !strings.Contains(out, "image: ubuntu:latest") {
		t.Fatalf("no default image:\n%s", out)
	}
	wantLevel(t, rep, caps.JobContainer, "job/build", caps.Emulated)
}

func TestSecretsAndEnvironment(t *testing.T) {
	st := gstep("run", "curl -u ${{ secrets.TOKEN }} x")
	st.Env = []ir.Var{{Name: "KEY", Value: ir.Value{Kind: ir.ValueSecret, Secret: "api_key"}}, {Name: "PLAIN", Value: ir.Value{Lit: "v"}}}
	j := gjob("build", st)
	j.Env = []ir.Var{{Name: "JOB", Value: ir.Value{Lit: "j"}}, {Name: "SEC", Value: ir.Value{Kind: ir.ValueSecret, Secret: "s"}}}
	p := gproject(ghaOrigin, j)
	p.Pipelines[0].Env = []ir.Var{{Name: "PIPE", Value: ir.Value{Lit: "p"}}}
	out, rep := emitText(t, p)
	d := docsOf(t, out)[0]
	env := d["environment"].(map[string]any)
	if env["JOB"] != "j" || env["PIPE"] != "p" || len(env) != 2 {
		t.Fatalf("document environment = %v", env)
	}
	senv := stepsOf(d)[0]["environment"].(map[string]any)
	for name, secret := range map[string]string{"KEY": "api_key", "TOKEN": "TOKEN", "SEC": "s"} {
		m, _ := senv[name].(map[string]any)
		if m["from_secret"] != secret {
			t.Fatalf("step environment %s = %v, want from_secret %s", name, senv[name], secret)
		}
	}
	if !strings.Contains(out, "$${TOKEN}") || !hasLine(out, "# pipemaster:emulated pipeline.env pipeline/drone") {
		t.Fatalf("the secret text or the pipeline.env marker is missing:\n%s", out)
	}
	wantLevel(t, rep, caps.PipelineEnv, "pipeline/drone", caps.Emulated)
}

func TestAllowFailureAndContinueOnError(t *testing.T) {
	a, b := gstep("a", "x"), gstep("b", "y")
	b.ContinueOnError = true
	j := gjob("one", a)
	j.AllowFailure = true
	out, rep := emitText(t, gproject(ghaOrigin, j, gjob("two", b)))
	docs := docsOf(t, out)
	if stepsOf(docNamed(t, docs, "one"))[0]["failure"] != "ignore" || stepsOf(docNamed(t, docs, "two"))[0]["failure"] != "ignore" {
		t.Fatalf("failure: ignore is missing\n%s", out)
	}
	if !hasLine(out, "# pipemaster:emulated job.allow_failure pipeline/drone/job/one") {
		t.Fatalf("no allow_failure marker:\n%s", out)
	}
	wantLevel(t, rep, caps.JobAllowFailure, "job/one", caps.Emulated)
}

func TestFinallyDocument(t *testing.T) {
	p := gproject(ghaOrigin, gjob("a", gstep("x", "1")), gjob("b", gstep("y", "2")))
	p.Pipelines[0].Finally = []*ir.Job{gjob("cleanup", gstep("z", "3"))}
	out, rep := emitText(t, p)
	d := docNamed(t, docsOf(t, out), "cleanup")
	dep := d["depends_on"].([]any)
	st := d["trigger"].(map[string]any)["status"].([]any)
	if len(dep) != 2 || len(st) != 2 {
		t.Fatalf("finally depends_on %v status %v", dep, st)
	}
	wantLevel(t, rep, caps.PipelineFinally, "pipeline/drone", caps.Emulated)
	if !hasLine(out, "# pipemaster:emulated pipeline.finally pipeline/drone") {
		t.Fatalf("no finally marker:\n%s", out)
	}
}

func TestRunnerPlatformAndNode(t *testing.T) {
	j := gjob("build", gstep("x", "1"))
	j.Runner = ir.Runner{Labels: []string{"linux/arm64", "ubuntu-latest"}, Selector: map[string]string{"disk": "ssd"}}
	out, rep := emitText(t, gproject(ghaOrigin, j))
	d := docsOf(t, out)[0]
	pf := d["platform"].(map[string]any)
	if pf["os"] != "linux" || pf["arch"] != "arm64" || d["node"].(map[string]any)["disk"] != "ssd" {
		t.Fatalf("platform %v node %v", pf, d["node"])
	}
	wantLevel(t, rep, caps.JobRunner, "job/build", caps.External)
}

func TestEmitServices(t *testing.T) {
	j := gjob("build", gstep("x", "1"))
	j.Services = []ir.Service{{Name: "db", Image: ir.Image{Ref: "postgres:16"}, Env: []ir.Var{{Name: "P", Value: ir.Value{Lit: "x"}}}, Ports: []string{"5432"}}}
	out, _ := emitText(t, gproject(ghaOrigin, j))
	svc := docsOf(t, out)[0]["services"].([]any)[0].(map[string]any)
	if svc["name"] != "db" || svc["image"] != "postgres:16" {
		t.Fatalf("service = %v", svc)
	}
}

func TestTimeoutIsExternal(t *testing.T) {
	j := gjob("build", gstep("x", "1"))
	d := ir.Duration(600e9)
	j.Timeout = &d
	_, rep := emitText(t, gproject(ghaOrigin, j))
	wantLevel(t, rep, caps.JobTimeout, "job/build", caps.External)
}

func TestLostStepFields(t *testing.T) {
	st := gstep("x", "1")
	d := ir.Duration(60e9)
	st.Timeout, st.Shell = &d, "bash"
	_, rep := emitText(t, gproject(ghaOrigin, gjob("build", st)))
	wantLevel(t, rep, caps.StepTimeout, "step/x", caps.Lost)
	wantLevel(t, rep, caps.StepShell, "step/x", caps.Lost)
}

func TestForeignExtIsReported(t *testing.T) {
	st := gstep("x", "1")
	st.Ext = ir.Ext{"gitlab": map[string]any{"retry": 2}}
	out, rep := emitText(t, gproject("gitlab", gjob("build", st)))
	if strings.Contains(out, "retry") {
		t.Fatalf("foreign Ext is written:\n%s", out)
	}
	wantLevel(t, rep, caps.ExtPassthrough, "step/x", caps.Lost)
}

func TestOwnExtIsWritten(t *testing.T) {
	st := gstep("x", "1")
	st.Ext = ir.Ext{"drone": map[string]any{"pull": "always", "privileged": true}}
	j := gjob("build", st)
	j.Ext = ir.Ext{"drone": map[string]any{"clone": map[string]any{"depth": 1}, "workspace": map[string]any{"path": "src"}}}
	p := gproject("drone", j)
	p.Pipelines[0].Ext = ir.Ext{"drone": map[string]any{drone.ExtResources: []any{map[string]any{"kind": "secret", "name": "s", "data": "abc"}}}}
	out, _ := emitText(t, p)
	docs := docsOf(t, out)
	if len(docs) != 2 || docs[1]["kind"] != "secret" {
		t.Fatalf("the secret resource is not appended:\n%s", out)
	}
	s := stepsOf(docs[0])[0]
	if s["pull"] != "always" || s["privileged"] != true || docs[0]["clone"] == nil || docs[0]["workspace"] == nil {
		t.Fatalf("own Ext keys are lost:\n%s", out)
	}
}

func TestPipelineWithoutJobsGetsAPlaceholder(t *testing.T) {
	p := gproject(ghaOrigin)
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}}
	out, rep := emitText(t, p)
	if !strings.Contains(out, "name: noop") {
		t.Fatalf("no placeholder step:\n%s", out)
	}
	wantLevel(t, rep, caps.StepScript, "job/empty", caps.Emulated)
}

func TestMultiPipelineNames(t *testing.T) {
	p := gproject(ghaOrigin, gjob("build", gstep("x", "1")))
	p.Pipelines = append(p.Pipelines, &ir.Pipeline{ID: "docs", Jobs: []*ir.Job{gjob("build", gstep("x", "1"))}})
	out, _ := emitText(t, p)
	docs := docsOf(t, out)
	docNamed(t, docs, "drone-build")
	docNamed(t, docs, "docs-build")
	if n := countLines(out, "# pipemaster:emulated project.multi_pipeline"); n != 2 {
		t.Fatalf("want a marker per pipeline job, got %d\n%s", n, out)
	}
}

func TestStepNamesAreMadeValid(t *testing.T) {
	out, _ := emitText(t, gproject(ghaOrigin, gjob("my job", gstep("build $ test", "1"), gstep("build - test", "2"))))
	d := docsOf(t, out)[0]
	if d["name"] != "my-job" {
		t.Fatalf("name = %v", d["name"])
	}
	seen := map[any]bool{}
	for _, s := range stepsOf(d) {
		if seen[s["name"]] {
			t.Fatalf("step name repeats: %v", s["name"])
		}
		seen[s["name"]] = true
	}
	ids := drone.New().(interface {
		EmittedIDs(*ir.Project) map[string]string
	}).EmittedIDs(gproject(ghaOrigin, gjob("my job", gstep("build $ test", "1"))))
	if ids["pipeline/drone/job/my-job"] != "my job" || ids["pipeline/drone/job/my-job/step/build---test"] != "build $ test" {
		t.Fatalf("EmittedIDs = %v", ids)
	}
}

func TestStepNamesKeepSpacesAndColons(t *testing.T) {
	out, rep := emitText(t, gproject(ghaOrigin, gjob("build", gstep("Build API", "1"), gstep("build:webui", "2"), gstep(" pad ", "3"))))
	names := []string{}
	for _, s := range stepsOf(docsOf(t, out)[0]) {
		names = append(names, s["name"].(string))
	}
	if want := []string{"Build API", "build:webui", "-pad-"}; !slices.Equal(names, want) {
		t.Fatalf("names = %q, want %q", names, want)
	}
	for _, e := range rep.Entries {
		if e.Feature == caps.ExtPassthrough && strings.Contains(e.Strategy, "renamed") && strings.Contains(e.IRPath, "Build API") {
			t.Fatalf("a name Drone reads as written is not a rename: %+v", e)
		}
	}
}

func TestUnguardableEntryIsCommentedOut(t *testing.T) {
	out, rep := emitText(t, gproject(ghaOrigin, gjob("build", gstep("x", "echo ${{ steps.a.outputs.b }}", "ok"))))
	if !strings.Contains(out, "# pipemaster:lost script.interpolation") || strings.Contains(out, "${{") {
		t.Fatalf("the unspellable line must be reported and absent:\n%s", out)
	}
	wantLevel(t, rep, caps.ScriptInterpolation, "step/x", caps.Lost)
}

func TestConditionOnStepFilterGuardDisabled(t *testing.T) {
	branch := eq(ref(expr.CtxBranch), "main")
	cases := []struct {
		cond  expr.Expr
		check func(t *testing.T, out string, step map[string]any)
		name  string
		level caps.Level
	}{
		{name: "filter", cond: branch, check: func(t *testing.T, _ string, s map[string]any) {
			if b, _ := s["when"].(map[string]any)["branch"].([]any); len(b) != 1 || b[0] != "main" {
				t.Fatalf("when = %v", s["when"])
			}
		}, level: caps.Native},
		{name: "guard", cond: or(eq(ref(expr.CtxEvent), "pull_request"), eq(ref(expr.CtxBranch), "1.0")), check: func(t *testing.T, out string, s map[string]any) {
			first := s["commands"].([]any)[0].(string)
			if !strings.HasPrefix(first, "if ! ") || !strings.Contains(first, "$$DRONE_BUILD_EVENT") || s["when"] != nil {
				t.Fatalf("guard line %q when %v", first, s["when"])
			}
			if !hasLine(out, "# pipemaster:emulated step.if pipeline/drone/job/build/step/x") {
				t.Fatalf("no guard marker:\n%s", out)
			}
			plain := strings.ReplaceAll(first, "$$", "$")
			if _, cond, ok := dexpr.RemoveGuard([]string{plain}); !ok || cond == nil {
				t.Fatalf("the guard does not survive unescape: %q", plain)
			}
		}, level: caps.Emulated},
		{name: "disabled", cond: and(or(branch, eq(ref(expr.CtxTag), "v1")), expr.Call{Name: "hashFiles", Args: []expr.Expr{slit("x")}}), check: func(t *testing.T, _ string, s map[string]any) {
			ev := s["when"].(map[string]any)["event"].(map[string]any)["exclude"].([]any)
			if len(ev) != 1 || ev[0] != "**" {
				t.Fatalf("when = %v", s["when"])
			}
		}, level: caps.Lost},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := gstep("x", "echo hi")
			st.If = c.cond
			out, rep := emitText(t, gproject("drone", gjob("build", st)))
			c.check(t, out, stepsOf(docsOf(t, out)[0])[0])
			if c.level != caps.Native {
				wantLevel(t, rep, caps.StepIf, "step/x", c.level)
			}
		})
	}
}

func TestNoOrAcrossKeys(t *testing.T) {
	cond := or(and(eq(ref(expr.CtxEvent), "push"), eq(ref(expr.CtxBranch), "main")), eq(ref(expr.CtxEvent), "tag"))
	st := gstep("x", "echo hi")
	st.If = cond
	out, rep := emitText(t, gproject("drone", gjob("build", st)))
	s := stepsOf(docsOf(t, out)[0])[0]
	if s["when"] != nil {
		t.Fatalf("the OR became one AND when:\n%s", out)
	}
	if first := s["commands"].([]any)[0].(string); !strings.HasPrefix(first, "if ! ") || !strings.Contains(first, "$$DRONE_BRANCH") {
		t.Fatalf("the OR is not a shell guard: %q", first)
	}
	wantLevel(t, rep, caps.StepIf, "step/x", caps.Emulated)
}

func TestJobConditionFilterAndGuard(t *testing.T) {
	filter := gjob("a", gstep("x", "1"))
	filter.If = eq(ref(expr.CtxBranch), "main")
	guard := gjob("b", gstep("x", "1"), gstep("y", "2"))
	guard.If = or(eq(ref(expr.CtxEvent), "pull_request"), eq(ref(expr.CtxBranch), "1.0"))
	plug := gjob("c", &ir.Step{ID: "p", Kind: ir.StepPlugin, Image: &ir.Image{Ref: "plugins/x"}})
	plug.If = guard.If
	out, rep := emitText(t, gproject("drone", filter, guard, plug))
	docs := docsOf(t, out)
	if b, _ := docNamed(t, docs, "a")["trigger"].(map[string]any)["branch"].([]any); len(b) != 1 || b[0] != "main" {
		t.Fatalf("the job filter is not the trigger:\n%s", out)
	}
	for _, s := range stepsOf(docNamed(t, docs, "b")) {
		if !strings.HasPrefix(s["commands"].([]any)[0].(string), "if ! ") {
			t.Fatalf("a step lacks the job guard: %v", s)
		}
	}
	if docNamed(t, docs, "c")["trigger"].(map[string]any)["event"].(map[string]any)["exclude"] == nil {
		t.Fatalf("a job with a plugin step cannot be guarded, so it must be disabled:\n%s", out)
	}
	wantLevel(t, rep, caps.JobIf, "job/b", caps.Emulated)
	wantLevel(t, rep, caps.JobIf, "job/c", caps.Lost)
}

func TestOpaqueDroneConditionIsVerbatim(t *testing.T) {
	st := gstep("x", "1")
	st.If = expr.Opaque{Dialect: "drone", Text: `{"event":"push","action":"opened","branch":"a$b"}`}
	out := emitUnchecked(t, gproject("drone", gjob("build", st)))
	w := stepsOf(docsOf(t, out)[0])[0]["when"].(map[string]any)
	if w["event"] != "push" || w["action"] != "opened" || w["branch"] != "a$$b" {
		t.Fatalf("when = %v", w)
	}
}

func TestUnknownOpaqueIsLost(t *testing.T) {
	st := gstep("x", "1")
	st.If = expr.Opaque{Dialect: "gitlab", Text: "$X"}
	_, rep := emitText(t, gproject(ghaOrigin, gjob("build", st)))
	wantLevel(t, rep, caps.StepIf, "step/x", caps.Lost)
}

const foldCaveatText = "Drone compares case-sensitively; GHA ignores case"

func foldEntry(t *testing.T, rep *report.Report, f caps.Feature, pathHas string) (report.Entry, bool) {
	t.Helper()
	for _, e := range rep.Entries {
		if e.Feature == f && strings.Contains(e.IRPath, pathHas) && strings.Contains(e.Detail, foldCaveatText) {
			return e, true
		}
	}
	return report.Entry{}, false
}

func TestFoldedComparisonBecomesFilter(t *testing.T) {
	for _, c := range []struct {
		cond expr.Expr
		name string
		want []string
	}{
		{eq(ref(expr.CtxRef), "refs/heads/main"), "github.ref", []string{"refs/heads/main"}},
		{eq(ref(expr.CtxRefName), "main"), "github.ref_name", []string{"refs/heads/main", "refs/tags/main"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := gstep("x", "echo hi")
			st.If = c.cond
			out, rep := emitText(t, gproject(ghaOrigin, gjob("build", st)))
			when := stepsOf(docsOf(t, out)[0])[0]["when"].(map[string]any)
			got, _ := when["ref"].([]any)
			if len(got) != len(c.want) {
				t.Fatalf("when = %v\n%s", when, out)
			}
			for i, w := range c.want {
				if got[i] != w {
					t.Fatalf("when = %v", when)
				}
			}
			if ent, ok := foldEntry(t, rep, caps.StepIf, "step/x"); !ok || ent.Level != caps.Emulated {
				t.Fatalf("no emulated fold entry: %v", rep.Entries)
			}
		})
	}
}

func TestFoldedComparisonBecomesGuard(t *testing.T) {
	st := gstep("x", "echo hi")
	st.If = or(eq(ref(expr.CtxRef), "refs/heads/Main"), eq(ref(expr.CtxEvent), "pull_request"))
	out, rep := emitText(t, gproject(ghaOrigin, gjob("build", st)))
	s := stepsOf(docsOf(t, out)[0])[0]
	first := s["commands"].([]any)[0].(string)
	if !strings.HasPrefix(first, "if ! ") || !strings.Contains(first, "'refs/heads/Main'") || s["when"] != nil {
		t.Fatalf("guard line %q when %v", first, s["when"])
	}
	if ent, ok := foldEntry(t, rep, caps.StepIf, "step/x"); !ok || ent.Level != caps.Emulated {
		t.Fatalf("no emulated fold entry: %v", rep.Entries)
	}
}

func TestFoldedJobConditionIsEmulated(t *testing.T) {
	j := gjob("a", gstep("x", "1"))
	j.If = eq(ref(expr.CtxBranch), "Main")
	out, rep := emitText(t, gproject(ghaOrigin, j))
	if b, _ := docsOf(t, out)[0]["trigger"].(map[string]any)["branch"].([]any); len(b) != 1 || b[0] != "Main" {
		t.Fatalf("the job filter is not the trigger:\n%s", out)
	}
	if ent, ok := foldEntry(t, rep, caps.JobIf, "job/a"); !ok || ent.Level != caps.Emulated {
		t.Fatalf("no emulated fold entry: %v", rep.Entries)
	}
}

func TestDroneOriginComparisonStaysExact(t *testing.T) {
	st := gstep("x", "echo hi")
	st.If = eq(ref(expr.CtxBranch), "Main")
	out, rep := emitText(t, gproject("drone", gjob("build", st)))
	if b, _ := stepsOf(docsOf(t, out)[0])[0]["when"].(map[string]any)["branch"].([]any); len(b) != 1 || b[0] != "Main" {
		t.Fatalf("when = %v", out)
	}
	if _, ok := foldEntry(t, rep, caps.StepIf, "step/x"); ok {
		t.Fatalf("a Drone-origin condition must not carry the fold caveat: %v", rep.Entries)
	}
}

func TestRenamedJobIDIsReported(t *testing.T) {
	out, rep := emitText(t, gproject("drone", gjob("Ci Main", gstep("x", "1")), gjob("ok", gstep("x", "1"))))
	if !strings.Contains(out, "name: Ci-Main") {
		t.Fatalf("the document is not named Ci-Main:\n%s", out)
	}
	var found bool
	for _, e := range rep.Entries {
		if e.Feature == caps.ExtPassthrough && strings.Contains(e.IRPath, "job/Ci Main") {
			found = true
			if e.Level != caps.Emulated || !strings.Contains(e.Detail, "Ci Main") || !strings.Contains(e.Detail, "Ci-Main") {
				t.Fatalf("entry = %+v", e)
			}
		}
		if strings.Contains(e.IRPath, "job/ok") && e.Feature == caps.ExtPassthrough {
			t.Fatalf("a valid job id must not be reported: %+v", e)
		}
	}
	if !found {
		t.Fatalf("the rename has no report entry: %v", rep.Entries)
	}
}

func TestBlankScriptLinesSurviveTheFile(t *testing.T) {
	for name, script := range map[string][]string{
		"inner":    {"echo a", "", "echo b"},
		"run":      {"echo a", "", "", "echo b"},
		"trailing": {"echo a", ""},
		"leading":  {"", "echo a"},
	} {
		t.Run(name, func(t *testing.T) {
			st := gstep("s", script...)
			out, _ := emitText(t, gproject(ghaOrigin, gjob("build", st)))
			if strings.Contains(out, `""`) {
				t.Fatalf("a command must not be empty:\n%s", out)
			}
			pl, _ := parseYML(t, out)
			if got := pl.Jobs[0].Steps[0].Script; !slices.Equal(got, st.Script) {
				t.Fatalf("script = %q, want %q", got, st.Script)
			}
		})
	}
}

func TestExtKeysWithUnderscoreAreWritten(t *testing.T) {
	j := gjob("build", gstep("x", "echo"))
	j.Ext = ir.Ext{"drone": map[string]any{"__buildenv": map[string]any{"image": "golang"}}}
	p := gproject("drone", j)
	ir.Normalize(p)
	files, _, err := drone.New().Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	out := string(files[ymlName])
	if got, _ := docsOf(t, out)[0]["__buildenv"].(map[string]any); got["image"] != "golang" {
		t.Fatalf("a helper key of the source file is kept:\n%s", out)
	}
	if strings.Contains(out, drone.ExtCommands) {
		t.Fatal("the step grouping record is not a file key")
	}
}

func TestNoPosixGuardOnWindows(t *testing.T) {
	cond := or(eq(ref(expr.CtxEvent), "pull_request"), eq(ref(expr.CtxBranch), "1.0"))
	for name, mk := range map[string]func() *ir.Project{
		"job guard": func() *ir.Project {
			j := gjob("b", gstep("x", "1"))
			j.If = cond
			j.Runner = ir.Runner{Platform: "windows/amd64"}
			return gproject("drone", j)
		},
		"step guard": func() *ir.Project {
			s := gstep("x", "1")
			s.If = cond
			j := gjob("b", s)
			j.Runner = ir.Runner{Platform: "windows/amd64"}
			return gproject("drone", j)
		},
		"platform in Ext": func() *ir.Project {
			s := gstep("x", "1")
			s.If = cond
			j := gjob("b", s)
			j.Ext = ir.Ext{"drone": map[string]any{"platform": map[string]any{"os": "windows"}}}
			return gproject("drone", j)
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, rep := emitText(t, mk())
			if strings.Contains(out, "pipemaster:guard") {
				t.Errorf("a POSIX guard line on a Windows document:\n%s", out)
			}
			if !slices.ContainsFunc(rep.Entries, func(e report.Entry) bool {
				return (e.Feature == caps.JobIf || e.Feature == caps.StepIf) && e.Level == caps.Lost
			}) {
				t.Errorf("no lost entry for the disabled condition: %+v", rep.Entries)
			}
		})
	}
}

func TestDependencyOnAnUnwrittenStepIsReported(t *testing.T) {
	a := gstep("a")
	b := gstep("b", "make")
	b.Ext = ir.Ext{"drone": map[string]any{"depends_on": []any{"a"}}}
	_, rep := emitText(t, gproject("drone", gjob("j", a, b)))
	if !slices.ContainsFunc(rep.Entries, func(e report.Entry) bool {
		return e.Level == caps.Lost && strings.Contains(e.IRPath, "step/b") && strings.Contains(e.Detail, "depends_on")
	}) {
		t.Errorf("no lost entry for the dropped depends_on: %+v", rep.Entries)
	}
}

func TestBranchFilterCarriesThePullRequestCaveat(t *testing.T) {
	cond := eq(ref(expr.CtxBranch), "main")
	for name, tc := range map[string]struct {
		mk func() *ir.Project
		f  caps.Feature
	}{
		"step": {func() *ir.Project {
			s := gstep("x", "1")
			s.If = cond
			return gproject("drone", gjob("b", s))
		}, caps.StepIf},
		"job": {func() *ir.Project {
			j := gjob("b", gstep("x", "1"))
			j.If = cond
			return gproject("drone", j)
		}, caps.JobIf},
	} {
		t.Run(name, func(t *testing.T) {
			_, rep := emitText(t, tc.mk())
			if !slices.ContainsFunc(rep.Entries, func(e report.Entry) bool {
				return e.Feature == tc.f && strings.Contains(e.Detail, "target branch")
			}) {
				t.Errorf("no pull request caveat on %s: %+v", tc.f, rep.Entries)
			}
		})
	}
}
