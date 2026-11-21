package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
)

func TestEmitNilProject(t *testing.T) {
	files, rep, err := tekton.New().Emit(nil)
	if err != nil || len(files) != 0 || rep == nil {
		t.Fatalf("Emit(nil) = %v, %v, %v", files, rep, err)
	}
}

func TestLayoutAndKustomization(t *testing.T) {
	p := project(scriptJob("build", "go build ./..."), scriptJob("test", "go test ./..."))
	p.Pipelines[0].Jobs[1].Needs = []ir.Need{{JobID: "build"}}
	files, _ := emit(t, p)
	want := []string{".tekton/kustomization.yaml", ".tekton/pipeline.yaml", ".tekton/tasks/ci-build.yaml", ".tekton/tasks/ci-test.yaml"}
	if got := fileNames(files); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", got, want)
	}
	k := docs(t, files[".tekton/kustomization.yaml"])[0]
	res := list(k["resources"])
	wantRes := []string{"pipeline.yaml", "tasks/ci-build.yaml", "tasks/ci-test.yaml"}
	if len(res) != len(wantRes) {
		t.Fatalf("resources = %v", res)
	}
	for i, r := range wantRes {
		if res[i] != r {
			t.Fatalf("resources = %v, want %v", res, wantRes)
		}
	}
	pl := pipelineDoc(t, files, "ci")
	if pl["apiVersion"] != "tekton.dev/v1" || pl["kind"] != "Pipeline" {
		t.Fatalf("pipeline header = %v", pl)
	}
	tasks := list(get(pl, "spec", "tasks"))
	if len(tasks) != 2 {
		t.Fatalf("tasks = %v", tasks)
	}
	second := tasks[1].(doc)
	if get(second, "taskRef", "name") != "ci-test" || second["name"] != "test" {
		t.Fatalf("second task = %v", second)
	}
	if ra := list(second["runAfter"]); len(ra) != 1 || ra[0] != "build" {
		t.Fatalf("runAfter = %v", second["runAfter"])
	}
	task := taskDoc(t, files, "ci-build")
	if get(task, "metadata", "name") != "ci-build" || task["kind"] != "Task" {
		t.Fatalf("task = %v", task)
	}
	step := list(get(task, "spec", "steps"))[0].(doc)
	if step["image"] != "golang:1.27" || step["script"] != "go build ./..." {
		t.Fatalf("step = %v", step)
	}
}

func TestTaskNameCollisionIsResolved(t *testing.T) {
	p := project(scriptJob("Build", "a"), scriptJob("build", "b"))
	files, rep := emit(t, p)
	if len(files) != 4 {
		t.Fatalf("files = %v", fileNames(files))
	}
	pl := pipelineDoc(t, files, "ci")
	tasks := list(get(pl, "spec", "tasks"))
	n0, n1 := tasks[0].(doc)["name"], tasks[1].(doc)["name"]
	if n0 == n1 {
		t.Fatalf("both tasks are called %v", n0)
	}
	wantEntry(t, rep, caps.ExtPassthrough, caps.Emulated, "collides")
	back, _ := reparse(t, files)
	if back.Pipelines[0].Jobs[0].ID != "Build" || back.Pipelines[0].Jobs[1].ID != "build" {
		t.Fatalf("ids after parse: %s, %s", back.Pipelines[0].Jobs[0].ID, back.Pipelines[0].Jobs[1].ID)
	}
}

// Review Focus 1: shell command substitution reaches the shell unchanged; a Tekton variable form is reported.
func TestShellSubstitutionReachesTheShell(t *testing.T) {
	p := project(scriptJob("a", "echo $(date)", "echo $(params.x)"))
	files, rep := emit(t, p)
	step := list(get(taskDoc(t, files, "ci-a"), "spec", "steps"))[0].(doc)
	if s := step["script"].(string); s != "echo $(date)\necho $(params.x)" {
		t.Fatalf("script = %q", s)
	}
	e := entry(rep, caps.ScriptInterpolation, "$(params.x)")
	if e == nil || entry(rep, caps.ScriptInterpolation, "$(date)") != nil {
		t.Fatalf("interpolation entries = %v", rep.Entries)
	}
}

func TestContextReadsFromStepEnv(t *testing.T) {
	p := project(scriptJob("a", "echo ${{ branch }} ${{ sha }}"))
	files, _ := emit(t, p)
	pl := pipelineDoc(t, files, "ci")
	params := get(pl, "spec", "params")
	for _, n := range []string{"ctx-branch", "ctx-sha"} {
		if !hasNamed(params, n) {
			t.Fatalf("pipeline param %s missing: %v", n, params)
		}
	}
	if hasNamed(params, "ctx-tag") {
		t.Fatalf("ctx-tag is declared but unused: %v", params)
	}
	task := taskDoc(t, files, "ci-a")
	step := list(get(task, "spec", "steps"))[0].(doc)
	script := step["script"].(string)
	if strings.Contains(script, "$(params.") || !strings.Contains(script, "${PM_CTX_BRANCH}") {
		t.Fatalf("script = %q", script)
	}
	env := named(t, step["env"], "PM_CTX_BRANCH")
	if env["value"] != "$(params.ctx-branch)" {
		t.Fatalf("env = %v", env)
	}
	if !hasNamed(get(task, "spec", "params"), "ctx-branch") {
		t.Fatalf("task params = %v", get(task, "spec", "params"))
	}
	pt := list(get(pl, "spec", "tasks"))[0].(doc)
	if b := named(t, pt["params"], "ctx-branch"); b["value"] != "$(params.ctx-branch)" {
		t.Fatalf("binding = %v", b)
	}
	back, _ := reparse(t, files)
	st := back.Pipelines[0].Jobs[0].Steps[0]
	if st.Script[0] != "echo ${{ branch }} ${{ sha }}" || len(st.Env) != 0 {
		t.Fatalf("parsed step = %+v", st)
	}
}

func TestInputsMatrixAndNeedsBecomeTaskParams(t *testing.T) {
	build := scriptJob("build", "true")
	build.Outputs = []ir.Output{{Name: "digest"}}
	test := scriptJob("test", "echo ${{ inputs.ver }} ${{ matrix.os }} ${{ needs.build.outputs.digest }}")
	test.Needs = []ir.Need{{JobID: "build"}}
	test.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "os", Values: []string{"linux", "mac"}}}}
	p := project(build, test)
	p.Pipelines[0].Inputs = []ir.Input{{Name: "ver", Default: "1"}}
	files, _ := emit(t, p)
	task := taskDoc(t, files, "ci-test")
	script := list(get(task, "spec", "steps"))[0].(doc)["script"].(string)
	if !strings.Contains(script, "$(params.ver)") || !strings.Contains(script, "$(params.os)") || !strings.Contains(script, "$(params.needs-build-digest)") {
		t.Fatalf("script = %q", script)
	}
	pl := pipelineDoc(t, files, "ci")
	pt := list(get(pl, "spec", "tasks"))[1].(doc)
	if v := named(t, pt["params"], "needs-build-digest"); v["value"] != "$(tasks.build.results.digest)" {
		t.Fatalf("needs binding = %v", v)
	}
	if v := named(t, pt["params"], "ver"); v["value"] != "$(params.ver)" {
		t.Fatalf("input binding = %v", v)
	}
	if hasNamed(pt["params"], "os") {
		t.Fatalf("matrix param bound by hand: %v", pt["params"])
	}
	back, _ := reparse(t, files)
	if got := back.Pipelines[0].Jobs[1].Steps[0].Script[0]; got != "echo ${{ inputs.ver }} ${{ matrix.os }} ${{ needs.build.outputs.digest }}" {
		t.Fatalf("parsed script = %q", got)
	}
}

// Review Focus 2 and ruling 4: an input named like a context param does not shadow it.
func TestInputNamedLikeContextParamIsRenamed(t *testing.T) {
	p := project(scriptJob("a", "echo ${{ inputs.ctx-branch }} ${{ branch }}"))
	p.Pipelines[0].Inputs = []ir.Input{{Name: "ctx-branch", Default: "x"}, {Name: "branch", Default: "y"}}
	files, rep := emit(t, p)
	pl := pipelineDoc(t, files, "ci")
	params := get(pl, "spec", "params")
	ctx := named(t, params, "ctx-branch")
	if ctx["default"] != "" {
		t.Fatalf("ctx-branch is the user's input: %v", ctx)
	}
	if !hasNamed(params, "branch") {
		t.Fatalf("input branch is gone: %v", params)
	}
	if len(list(params)) != 3 {
		t.Fatalf("params = %v", params)
	}
	wantEntry(t, rep, caps.PipelineInputs, caps.Emulated, "ctx-branch")
	back, _ := reparse(t, files)
	var names []string
	for _, in := range back.Pipelines[0].Inputs {
		names = append(names, in.Name)
	}
	if len(names) != 2 || names[1] != "branch" || names[0] == "ctx-branch" {
		t.Fatalf("parsed inputs = %v", names)
	}
}

// Review Focus 4: matrix values are strings; include keys beside the axes become params.
func TestMatrixValuesAreStrings(t *testing.T) {
	j := scriptJob("a", "echo ${{ matrix.n }} ${{ matrix.extra }}")
	j.Matrix = &ir.Matrix{
		Axes:    []ir.Axis{{Name: "n", Values: []string{"1", "true", "2.5"}}},
		Include: []ir.Combo{{{Key: "extra", Value: "x"}, {Key: "n", Value: "1"}}},
	}
	files, _ := emit(t, project(j))
	pt := list(get(pipelineDoc(t, files, "ci"), "spec", "tasks"))[0].(doc)
	mp := named(t, get(pt, "matrix", "params"), "n")
	vals := list(mp["value"])
	if len(vals) != 3 || vals[0] != "1" || vals[1] != "true" || vals[2] != "2.5" {
		t.Fatalf("axis values = %#v", vals)
	}
	inc := list(get(pt, "matrix", "include"))
	if len(inc) != 1 || named(t, inc[0].(doc)["params"], "extra")["value"] != "x" {
		t.Fatalf("include = %v", inc)
	}
	task := taskDoc(t, files, "ci-a")
	for _, n := range []string{"n", "extra"} {
		if !hasNamed(get(task, "spec", "params"), n) {
			t.Fatalf("task param %s missing: %v", n, get(task, "spec", "params"))
		}
	}
	back, _ := reparse(t, files)
	if ok, diff := ir.Equal(project(j), withOrigin(back)); !ok {
		t.Fatalf("matrix did not round trip:\n%s", diff)
	}
}

func withOrigin(p *ir.Project) *ir.Project {
	p.Origin = ir.Origin{Provider: "gha", Dialect: "gha"}
	return p
}

func TestMatrixExcludeIsExpandedAndReported(t *testing.T) {
	j := scriptJob("a", "true")
	j.Matrix = &ir.Matrix{
		Axes:    []ir.Axis{{Name: "os", Values: []string{"l", "m"}}, {Name: "v", Values: []string{"1", "2"}}},
		Exclude: []ir.Combo{{{Key: "os", Value: "m"}, {Key: "v", Value: "2"}}},
	}
	j.Matrix.FailFast = new(bool)
	files, rep := emit(t, project(j))
	pt := list(get(pipelineDoc(t, files, "ci"), "spec", "tasks"))[0].(doc)
	if got := list(get(pt, "matrix", "include")); len(got) != 3 || get(pt, "matrix", "params") != nil {
		t.Fatalf("matrix = %v", pt["matrix"])
	}
	wantEntry(t, rep, caps.JobMatrix, caps.Lost, "fail-fast")
}

// Review Focus 3: finally tasks cannot be ordered among themselves.
func TestFinallyEdgeIsLost(t *testing.T) {
	a, b := scriptJob("a", "true"), scriptJob("b", "true")
	b.Needs = []ir.Need{{JobID: "a"}}
	p := project(scriptJob("main", "true"))
	p.Pipelines[0].Finally = []*ir.Job{a, b}
	files, rep := emit(t, p)
	spec := get(pipelineDoc(t, files, "ci"), "spec")
	fin := list(get(spec, "finally"))
	if len(fin) != 2 || fin[1].(doc)["runAfter"] != nil {
		t.Fatalf("finally = %v", fin)
	}
	wantEntry(t, rep, caps.JobNeeds, caps.Lost, "finally")
}

func TestFinallyStatusIsWrittenAsTektonVariables(t *testing.T) {
	a := scriptJob("notify", "true")
	a.If = eqStr([]string{"needs", "main", "result"}, "failure")
	p := project(scriptJob("main", "true"))
	p.Pipelines[0].Finally = []*ir.Job{a}
	files, _ := emit(t, p)
	fin := list(get(pipelineDoc(t, files, "ci"), "spec", "finally"))[0].(doc)
	w := list(fin["when"])
	if len(w) != 1 || w[0].(doc)["input"] != "$(tasks.main.status)" || w[0].(doc)["operator"] != "in" {
		t.Fatalf("when = %v", fin["when"])
	}
	if v := list(w[0].(doc)["values"]); len(v) != 1 || v[0] != "Failed" {
		t.Fatalf("values = %v", w)
	}
	back, _ := reparse(t, files)
	if got := cond(back.Pipelines[0].Finally[0].If); got != "needs.main.result == 'failure'" {
		t.Fatalf("parsed condition = %q", got)
	}
}

func TestOpaqueCELIsNeverEmitted(t *testing.T) {
	j := scriptJob("a", "true")
	j.If = celOpaque()
	files, rep := emit(t, project(j))
	if strings.Contains(joinFiles(files), "cel:") {
		t.Fatalf("a cel when entry was emitted:\n%s", joinFiles(files))
	}
	pt := list(get(pipelineDoc(t, files, "ci"), "spec", "tasks"))[0].(doc)
	w := list(pt["when"])[0].(doc)
	if w["input"] != "pipemaster-disabled" {
		t.Fatalf("when = %v", pt["when"])
	}
	wantEntry(t, rep, caps.ExprOpaque, caps.Lost, "cel")
}

func TestConditionsBecomeWhenOrGuard(t *testing.T) {
	j := scriptJob("a", "echo hi")
	j.If = eqStr([]string{"branch"}, "main")
	files, _ := emit(t, project(j))
	pt := list(get(pipelineDoc(t, files, "ci"), "spec", "tasks"))[0].(doc)
	w := list(pt["when"])[0].(doc)
	if w["input"] != "$(params.ctx-branch)" || w["operator"] != "in" {
		t.Fatalf("when = %v", pt["when"])
	}

	s := scriptJob("b", "echo hi")
	s.Steps[0].If = eqStr([]string{"event"}, "push")
	files, _ = emit(t, project(s))
	step := list(get(taskDoc(t, files, "ci-b"), "spec", "steps"))[0].(doc)
	if w := list(step["when"]); len(w) != 1 || w[0].(doc)["input"] != "$(params.ctx-event)" {
		t.Fatalf("step when = %v", step)
	}
	back, _ := reparse(t, files)
	if got := cond(back.Pipelines[0].Jobs[0].Steps[0].If); got != "event == 'push'" {
		t.Fatalf("parsed step condition = %q", got)
	}
}

func TestUnexpressibleJobConditionIsGuardedInTheShell(t *testing.T) {
	j := scriptJob("a", "echo hi")
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "os", Values: []string{"l"}}}}
	j.If = eqStr([]string{"matrix", "os"}, "l")
	files, _ := emit(t, project(j))
	pt := list(get(pipelineDoc(t, files, "ci"), "spec", "tasks"))[0].(doc)
	if pt["when"] != nil {
		t.Fatalf("a matrix condition became a when: %v", pt["when"])
	}
	step := list(get(taskDoc(t, files, "ci-a"), "spec", "steps"))[0].(doc)
	script := step["script"].(string)
	if !strings.Contains(script, "# pipemaster:guard") || strings.Contains(script, "$(params.os)") {
		t.Fatalf("script = %q", script)
	}
	if named(t, step["env"], "PM_MATRIX_OS")["value"] != "$(params.os)" {
		t.Fatalf("env = %v", step["env"])
	}
}

func TestSecretsAndVarsAreEnvReferences(t *testing.T) {
	j := scriptJob("a", "curl -u ${{ secrets.TOKEN }} ${{ vars.HOST }}")
	j.Env = []ir.Var{{Name: "K", Value: ir.Value{Kind: ir.ValueSecret, Secret: "KEY"}}, {Name: "L", Value: ir.Value{Lit: "x"}}}
	files, rep := emit(t, project(j))
	task := taskDoc(t, files, "ci-a")
	step := list(get(task, "spec", "steps"))[0].(doc)
	if s := step["script"].(string); s != "curl -u ${TOKEN} ${HOST}" {
		t.Fatalf("script = %q", s)
	}
	tok := named(t, step["env"], "TOKEN")
	if get(tok, "valueFrom", "secretKeyRef", "name") != "ci-secrets" || get(tok, "valueFrom", "secretKeyRef", "key") != "TOKEN" {
		t.Fatalf("TOKEN = %v", tok)
	}
	host := named(t, step["env"], "HOST")
	if get(host, "valueFrom", "configMapKeyRef", "name") != "ci-vars" {
		t.Fatalf("HOST = %v", host)
	}
	tpl := get(task, "spec", "stepTemplate", "env")
	if get(named(t, tpl, "K"), "valueFrom", "secretKeyRef", "key") != "KEY" || named(t, tpl, "L")["value"] != "x" {
		t.Fatalf("stepTemplate env = %v", tpl)
	}
	wantEntry(t, rep, caps.ConfigName, caps.Emulated, "secrets.TOKEN")
}

func TestJobFieldsMapToTektonKeys(t *testing.T) {
	j := scriptJob("a", "true")
	d := ir.Duration(90_000_000_000)
	j.Timeout, j.Retry, j.AllowFailure = &d, &ir.Retry{Attempts: 3}, true
	j.Name = "Build it"
	j.Container = &ir.Image{Ref: "alpine:3.22"}
	j.Steps[0].Image = nil
	j.Steps[0].ContinueOnError = true
	j.Steps[0].WorkDir = "/src"
	j.Steps[0].Shell = "/bin/bash"
	j.Outputs = []ir.Output{{Name: "out"}}
	j.Services = []ir.Service{{Name: "db", Image: ir.Image{Ref: "postgres:17"}, Ports: []string{"5432"}, Env: []ir.Var{{Name: "A", Value: ir.Value{Lit: "b"}}}}}
	files, _ := emit(t, project(j))
	pt := list(get(pipelineDoc(t, files, "ci"), "spec", "tasks"))[0].(doc)
	if pt["retries"] != 2 || pt["timeout"] != "1m30s" || pt["onError"] != "continue" || pt["displayName"] != "Build it" {
		t.Fatalf("pipeline task = %v", pt)
	}
	spec := get(taskDoc(t, files, "ci-a"), "spec")
	if get(spec, "stepTemplate", "image") != "alpine:3.22" || !hasNamed(get(spec, "results"), "out") {
		t.Fatalf("spec = %v", spec)
	}
	step := list(get(spec, "steps"))[0].(doc)
	if step["onError"] != "continue" || step["workingDir"] != "/src" || !strings.HasPrefix(step["script"].(string), "#!/bin/bash\n") || step["image"] != nil {
		t.Fatalf("step = %v", step)
	}
	if sc := named(t, get(spec, "sidecars"), "db"); sc["image"] != "postgres:17" {
		t.Fatalf("sidecar = %v", sc)
	}
	back, _ := reparse(t, files)
	want := project(j)
	if ok, diff := ir.Equal(want, withOrigin(back)); !ok {
		t.Fatalf("not identical:\n%s", diff)
	}
}

func TestForeignExtIsReported(t *testing.T) {
	j := scriptJob("a", "true")
	j.Ext = ir.Ext{"gitlab": map[string]any{"tags": []any{"x"}}}
	_, rep := emit(t, project(j))
	wantEntry(t, rep, caps.ExtPassthrough, caps.Lost, "gitlab")
}

func TestLostFeaturesAreReported(t *testing.T) {
	j := scriptJob("a", "true")
	j.Environment = &ir.Environment{Name: "prod"}
	j.Runner = ir.Runner{Labels: []string{"x"}}
	_, rep := emit(t, project(j))
	wantEntry(t, rep, caps.JobEnvironment, caps.Lost, "")
	wantEntry(t, rep, caps.JobRunner, caps.Lost, "")
}

func TestStepWithoutImageGetsTheDefault(t *testing.T) {
	j := scriptJob("a", "true")
	j.Steps[0].Image = nil
	files, rep := emit(t, project(j))
	step := list(get(taskDoc(t, files, "ci-a"), "spec", "steps"))[0].(doc)
	if step["image"] == nil || step["image"] == "" {
		t.Fatalf("step = %v", step)
	}
	wantEntry(t, rep, caps.StepImage, caps.Emulated, "default image")
}

func TestLongNamesStayUniqueAfterTruncation(t *testing.T) {
	long := strings.Repeat("a", 80)
	p := project(scriptJob(long+"-one", "true"), scriptJob(long+"-two", "true"))
	files, _ := emit(t, p)
	seen := map[string]bool{}
	for _, jt := range list(get(pipelineDoc(t, files, "ci"), "spec", "tasks")) {
		n := jt.(doc)["name"].(string)
		ref := get(jt, "taskRef", "name").(string)
		if len(n) > 63 || len(ref) > 63 || seen[n] || seen[ref] {
			t.Fatalf("names %q / %q are too long or repeat", n, ref)
		}
		seen[n], seen[ref] = true, true
	}
	back, _ := reparse(t, files)
	if back.Pipelines[0].Jobs[0].ID != long+"-one" && back.Pipelines[0].Jobs[1].ID != long+"-one" {
		t.Fatalf("ids after parse = %s, %s", back.Pipelines[0].Jobs[0].ID, back.Pipelines[0].Jobs[1].ID)
	}
}
