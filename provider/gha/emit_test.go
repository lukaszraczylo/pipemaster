package gha_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"pgregory.net/rapid"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/ir/irtest"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/validate"
)

func emitFS(t testing.TB, p *ir.Project) (fstest.MapFS, map[string][]byte) {
	t.Helper()
	files, _, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	fsys := fstest.MapFS{}
	for name, data := range files {
		fsys[name] = &fstest.MapFile{Data: data}
	}
	return fsys, files
}

func fixtureDirs(t testing.TB) []string {
	t.Helper()
	ents, err := os.ReadDir(featuresDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

func parseFixture(t testing.TB, name string) *ir.Project {
	t.Helper()
	p, _, err := gha.New(gha.GHA).Parse(os.DirFS(filepath.Join(featuresDir, name)))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return p
}

func lintFile(t testing.TB, name string, data []byte) { lintFileIgnoring(t, name, data, "") }

// lintFileIgnoring drops findings that match ignore; a disabled construct is `if: false`, which actionlint flags as constant.
func lintFileIgnoring(t testing.TB, name string, data []byte, ignore string) {
	t.Helper()
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		if os.Getenv("PIPEMASTER_REQUIRE_LINTERS") == "1" {
			t.Fatal("actionlint not on PATH and PIPEMASTER_REQUIRE_LINTERS=1")
		}
		t.Skip("actionlint not on PATH")
	}
	dir := filepath.Join(t.TempDir(), ".github", "workflows")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filepath.Base(name))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	args := []string{"-no-color"}
	if ignore != "" {
		args = append(args, "-ignore", ignore)
	}
	out, err := exec.CommandContext(ctx, bin, append(args, path)...).CombinedOutput() //nolint:gosec // bin from LookPath, path in a test temp dir
	if err != nil {
		t.Fatalf("actionlint %s: %v\n%s\n--- file ---\n%s", name, err, out, data)
	}
}

func TestIdentity(t *testing.T) {
	for _, name := range fixtureDirs(t) {
		t.Run(name, func(t *testing.T) {
			p := parseFixture(t, name)
			fsys, files := emitFS(t, p)
			back, _, err := gha.New(gha.GHA).Parse(fsys)
			if err != nil {
				t.Fatalf("reparse: %v\n%s", err, dump(files))
			}
			if ok, diff := ir.Equal(p, back); !ok {
				t.Fatalf("identity broken:\n%s\n--- emitted ---\n%s", diff, dump(files))
			}
			for fname, data := range files {
				if err := validate.File("gha", fname, data); err != nil {
					t.Fatalf("schema: %v\n%s", err, data)
				}
				lintFile(t, fname, data)
			}
		})
	}
}

func dump(files map[string][]byte) string {
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		sb.WriteString("# " + n + "\n" + string(files[n]))
	}
	return sb.String()
}

func TestEmitValidatesSchema(t *testing.T) {
	for _, name := range fixtureDirs(t) {
		t.Run(name, func(t *testing.T) {
			_, files := emitFS(t, parseFixture(t, name))
			if len(files) == 0 {
				t.Fatal("no files emitted")
			}
			for fname, data := range files {
				if !strings.HasPrefix(fname, ".github/workflows/") {
					t.Fatalf("unexpected path %s", fname)
				}
				if err := validate.File("gha", fname, data); err != nil {
					t.Fatalf("%v\n%s", err, data)
				}
			}
		})
	}
}

func TestEmitActionlint(t *testing.T) {
	for _, name := range fixtureDirs(t) {
		t.Run(name, func(t *testing.T) {
			_, files := emitFS(t, parseFixture(t, name))
			for fname, data := range files {
				lintFile(t, fname, data)
			}
		})
	}
}

func TestEmitDeterministic(t *testing.T) {
	for _, name := range fixtureDirs(t) {
		p := parseFixture(t, name)
		_, a := emitFS(t, p)
		_, b := emitFS(t, p)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s: emit is not deterministic", name)
		}
	}
}

func TestEmitDialectDir(t *testing.T) {
	p := parseFixture(t, "step.script")
	d := gha.Gitea
	files, _, err := gha.New(d).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files[".gitea/workflows/ci.yml"]; !ok {
		t.Fatalf("want .gitea/workflows/ci.yml, got %v", reflect.ValueOf(files).MapKeys())
	}
}

// nativeFragment is every feature GHA emits without emulation; step.script and step.action are
// mutually exclusive on one step, so each property run picks one.
func nativeFragment(action bool) irtest.Fragment {
	off := map[caps.Feature]bool{
		caps.JobRetry: true, caps.JobManual: true, caps.JobCache: true, caps.JobArtifacts: true,
		caps.JobNeedsArtifacts: true, caps.PipelineFinally: true, caps.JobEnvironmentApproval: true,
		caps.StepImage: true, caps.TriggerResource: true, caps.ScriptInterpolation: true, caps.ExprOpaque: true,
		caps.StepAction: !action, caps.StepScript: action,
	}
	return func(f caps.Feature) bool { return !off[f] }
}

func TestIdentityProperty(t *testing.T) {
	for _, action := range []bool{false, true} {
		name := "script"
		if action {
			name = "action"
		}
		t.Run(name, func(t *testing.T) {
			rapid.Check(t, func(t *rapid.T) {
				p := irtest.GenProject(t, "gha", nativeFragment(action))
				nativize(t, p)
				files, rep, err := gha.New(gha.GHA).Emit(p)
				if err != nil {
					t.Fatalf("Emit: %v", err)
				}
				fsys := fstest.MapFS{}
				for n, d := range files {
					fsys[n] = &fstest.MapFile{Data: d}
				}
				back, _, err := gha.New(gha.GHA).Parse(fsys)
				if err != nil {
					t.Fatalf("reparse: %v\n%s", err, dump(files))
				}
				dropLedgeredTriggers(p, back, rep)
				if ok, diff := ir.Equal(p, back); !ok {
					t.Fatalf("identity broken:\n%s\n--- emitted ---\n%s", diff, dump(files))
				}
			})
		})
	}
}

// dropLedgeredTriggers removes the triggers of every pipeline whose triggers the report lists as
// lost or emulated from both sides, so the rest of that pipeline is still compared exactly.
func dropLedgeredTriggers(p, back *ir.Project, rep *report.Report) {
	skip := map[string]bool{}
	for _, e := range rep.Entries {
		if id, _, ok := strings.Cut(strings.TrimPrefix(e.IRPath, "pipeline/"), "/trigger/"); ok && e.Level != caps.Native {
			skip[id] = true
		}
	}
	for _, proj := range []*ir.Project{p, back} {
		for _, pl := range proj.Pipelines {
			if skip[pl.ID] {
				pl.Triggers = nil
			}
		}
	}
}

// nativize narrows generated IR to what one GHA file can state: no runner platform, no image entrypoints, no ${{ in plain text, and expressions GHA can spell.
func nativize(t *rapid.T, p *ir.Project) {
	prefixExtKeys(p)
	for _, pl := range p.Pipelines {
		scrubStrings(reflect.ValueOf(pl))
		if pl.Permissions != nil {
			scopes := map[string]string{}
			for k, v := range pl.Permissions.Scopes {
				if k != "*" && k != "id-token" {
					scopes[k] = v
				}
			}
			pl.Permissions.Scopes = scopes
		}
		pl.Env = nativeVars(pl.Env)
		for _, j := range pl.Jobs {
			nativizeJob(t, j)
		}
	}
}

func nativizeJob(t *rapid.T, j *ir.Job) {
	j.Runner.Platform = ""
	if len(j.Runner.Selector) > 0 {
		var group string
		for _, v := range j.Runner.Selector {
			group = v
		}
		j.Runner.Selector = map[string]string{"group": group}
	}
	if j.Container != nil {
		j.Container.Entrypoint = nil
	}
	for i := range j.Services {
		j.Services[i].Image.Entrypoint = nil
		j.Services[i].Env = nativeVars(j.Services[i].Env)
	}
	for i := range j.Needs {
		j.Needs[i].Optional = false
	}
	j.If = nativeExpr(j.If)
	j.Env = nativeVars(j.Env)
	for _, s := range j.Steps {
		s.If = nativeExpr(s.If)
		s.Env = nativeVars(s.Env)
		if s.Action != nil {
			s.Action = actionRefs[rapid.IntRange(0, len(actionRefs)-1).Draw(t, "action")]
		}
		for k, v := range s.With {
			s.With[k] = nativeValue(v)
		}
	}
}

var actionRefs = []*ir.ActionRef{
	{Kind: ir.ActionRepo, Host: "https://github.com", Owner: "o", Repo: "r", Ref: "v1"},
	{Kind: ir.ActionRepo, Host: "https://github.com", Owner: "o", Repo: "r", Path: "sub/dir", Ref: "v2"},
	{Kind: ir.ActionDocker, Image: "alpine:3"},
	{Kind: ir.ActionLocal, Path: "./local/action"},
}

// nativeVars makes env names unique, since a mapping holds each key once.
func nativeVars(vs []ir.Var) []ir.Var {
	for i := range vs {
		vs[i].Name += strconv.Itoa(i)
		vs[i].Value = nativeValue(vs[i].Value)
	}
	return vs
}

// nativeValue maps a lone secrets.X reference to Secret, the form the GHA parser reads it back as.
func nativeValue(v ir.Value) ir.Value {
	if v.Kind != ir.ValueExpr {
		return v
	}
	v.Expr = nativeExpr(v.Expr)
	if r, ok := v.Expr.(expr.Ref); ok && len(r.Path) == 2 && r.Path[0] == expr.PrefixSecrets {
		return ir.Value{Kind: ir.ValueSecret, Secret: r.Path[1]}
	}
	return v
}

func nativeExpr(e expr.Expr) expr.Expr {
	switch v := e.(type) {
	case expr.Ref:
		if len(v.Path) == 1 && (v.Path[0] == expr.CtxBranch || v.Path[0] == expr.CtxTag) {
			return expr.Ref{Path: []string{expr.CtxRefName}}
		}
	case expr.Unary:
		v.X = nativeExpr(v.X)
		return v
	case expr.Binary:
		v.L, v.R = nativeExpr(v.L), nativeExpr(v.R)
		switch v.Op {
		case expr.OpMatch:
			v.Op = expr.OpEq
		case expr.OpNoMatch:
			v.Op = expr.OpNe
		}
		return v
	case expr.Call:
		args := make([]expr.Expr, len(v.Args))
		for i, a := range v.Args {
			args[i] = nativeExpr(a)
		}
		v.Args = args
		return v
	}
	return e
}

var stringType = reflect.TypeFor[string]()

// scrubStrings removes "${{" from every plain string field so random text cannot open a template.
func scrubStrings(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			scrubStrings(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if f := v.Field(i); f.CanSet() && f.Type() != reflect.TypeFor[ir.Ext]() {
				scrubStrings(f)
			}
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			scrubStrings(v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(v.MapIndex(k))
			scrubStrings(e)
			v.SetMapIndex(k, e)
		}
	case reflect.String:
		if v.Type() == stringType || v.Type().ConvertibleTo(stringType) {
			v.SetString(strings.ReplaceAll(v.String(), "${{", "$ {{"))
		}
	}
}

// prefixExtKeys keeps random Ext keys from shadowing keys the emitter writes itself, such as a step's id.
func prefixExtKeys(p *ir.Project) {
	for _, n := range ir.ExtNodes(p) {
		inner, _ := (*n.Ext)["gha"].(map[string]any)
		for k, v := range inner {
			delete(inner, k)
			inner["x-"+k] = v
		}
	}
}

func TestTaskStepEmitsAPlaceholderAndReportsLoss(t *testing.T) {
	p := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Triggers: []ir.Trigger{{Kind: ir.TriggerPush}}, Jobs: []*ir.Job{{
		ID: "b", Steps: []*ir.Step{{ID: "t", Kind: ir.StepTask}},
	}}}}}
	files, rep, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	if !strings.Contains(string(files[".github/workflows/ci.yml"]), "run: ") {
		t.Fatalf("no placeholder step:\n%s", files[".github/workflows/ci.yml"])
	}
	if e := entry(rep, caps.StepAction); e == nil || e.Level != caps.Lost {
		t.Fatalf("no lost entry: %v", rep.Entries)
	}
}

func TestShellOfAnImageStepIsReportedLost(t *testing.T) {
	p := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Triggers: []ir.Trigger{{Kind: ir.TriggerPush}}, Jobs: []*ir.Job{{
		ID: "b", Steps: []*ir.Step{{ID: "s", Kind: ir.StepScript, Script: []string{"true"}, Image: &ir.Image{Ref: "alpine:3"}, Shell: "/bin/sh"}},
	}}}}}
	_, rep, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	if e := entry(rep, caps.StepShell); e == nil || e.Level != caps.Lost {
		t.Fatalf("no lost entry: %v", rep.Entries)
	}
}

func TestInputTypeWithNoGHAFormIsReportedLost(t *testing.T) {
	p := &ir.Project{Origin: ir.Origin{Provider: "tekton", Dialect: "tekton"}, Pipelines: []*ir.Pipeline{{ID: "ci", Triggers: []ir.Trigger{{Kind: ir.TriggerManual}},
		Inputs: []ir.Input{{Name: "branches", Type: "array"}},
		Jobs:   []*ir.Job{{ID: "b", Steps: []*ir.Step{{ID: "s", Kind: ir.StepScript, Script: []string{"true"}}}}}}}}
	files, rep, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(files[".github/workflows/ci.yml"]), "type: array") {
		t.Fatalf("array type written:\n%s", files[".github/workflows/ci.yml"])
	}
	if e := entry(rep, caps.PipelineInputs); e == nil || e.Level != caps.Lost {
		t.Fatalf("no lost entry: %v", rep.Entries)
	}
}

func TestActionStepWithNoActionEmitsAPlaceholderAndReportsLoss(t *testing.T) {
	p := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Triggers: []ir.Trigger{{Kind: ir.TriggerPush}}, Jobs: []*ir.Job{{
		ID: "b", Steps: []*ir.Step{{ID: "a", Kind: ir.StepAction}},
	}}}}}
	files, rep, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	if !strings.Contains(string(files[".github/workflows/ci.yml"]), "run: ") {
		t.Fatalf("no placeholder step:\n%s", files[".github/workflows/ci.yml"])
	}
	if e := entry(rep, caps.StepAction); e == nil || e.Level != caps.Lost {
		t.Fatalf("no lost entry: %v", rep.Entries)
	}
}
