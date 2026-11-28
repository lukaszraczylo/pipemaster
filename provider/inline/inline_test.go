package inline_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/provider/inline"
	"github.com/lukaszraczylo/pipemaster/report"
)

func ref(path ...string) expr.Ref { return expr.Ref{Path: path} }

func str(s string) expr.Lit { return expr.Lit{Kind: expr.LitStr, Str: s} }

func eq(l, r expr.Expr) expr.Expr { return expr.Binary{Op: expr.OpEq, L: l, R: r} }

func job(id string, needs ...string) *ir.Job {
	j := &ir.Job{ID: id, Steps: []*ir.Step{{ID: "script", Kind: ir.StepScript, Script: []string{"echo " + id}}}}
	for _, n := range needs {
		j.Needs = append(j.Needs, ir.Need{JobID: n})
	}
	return j
}

func needs(j *ir.Job) []string {
	var out []string
	for _, n := range j.Needs {
		out = append(out, n.JobID)
	}
	slices.Sort(out)
	return out
}

func find(t *testing.T, p *ir.Project, id string) *ir.Job {
	t.Helper()
	for _, pl := range p.Pipelines {
		for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
			if j.ID == id {
				return j
			}
		}
	}
	t.Fatalf("job %s missing; have %v", id, ids(p))
	return nil
}

func ids(p *ir.Project) []string {
	var out []string
	for _, pl := range p.Pipelines {
		for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
			out = append(out, j.ID)
		}
	}
	slices.Sort(out)
	return out
}

// gitlabProject has a root job a, a trigger job call that needs a, a job z that needs call, and a child
// pipeline of three jobs: s1, then s2 and s3.
func gitlabProject(trigger map[string]any) *ir.Project {
	call := job("call", "a")
	call.Steps = nil
	call.If = eq(ref("branch"), str("main"))
	call.Ext = ir.Ext{"gitlab": map[string]any{"trigger": trigger}}
	call.Env = []ir.Var{{Name: "TARGET", Value: ir.Value{Lit: "staging"}}}
	s1 := job("s1")
	s1.If = eq(ref("event"), str("parent_pipeline"))
	call.Child = &ir.Child{
		Native: []string{"gitlab"}, Files: []string{"ci/child.yml"},
		Pipeline: &ir.Pipeline{ID: "p-call", Jobs: []*ir.Job{s1, job("s2", "s1"), job("s3", "s1")}},
	}
	return &ir.Project{Origin: ir.Origin{Provider: "gitlab"}, Pipelines: []*ir.Pipeline{{
		ID: "p", Jobs: []*ir.Job{job("a"), call, job("z", "call")},
	}}}
}

func TestFlattenGitLabChild(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger map[string]any
		zNeeds  []string
		caveat  bool
	}{
		{"depend waits for the sinks of the child", map[string]any{"include": "ci/child.yml", "strategy": "depend"}, []string{"call-s2", "call-s3"}, false},
		{"no depend passes at creation", map[string]any{"include": "ci/child.yml"}, []string{"a"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := gitlabProject(tc.trigger)
			flat, res := inline.Flatten(src, "drone")
			if got, want := ids(flat), []string{"a", "call-s1", "call-s2", "call-s3", "z"}; !slices.Equal(got, want) {
				t.Fatalf("job ids = %v, want %v", got, want)
			}
			if got := needs(find(t, flat, "call-s1")); !slices.Equal(got, []string{"a"}) {
				t.Errorf("a root of the child needs %v, want what the trigger job needs", got)
			}
			if got := needs(find(t, flat, "call-s2")); !slices.Equal(got, []string{"call-s1"}) {
				t.Errorf("call-s2 needs %v, want call-s1", got)
			}
			if got := needs(find(t, flat, "z")); !slices.Equal(got, tc.zNeeds) {
				t.Errorf("z needs %v, want %v", got, tc.zNeeds)
			}
			if len(res.Entries) != 1 || res.Entries[0].Level != caps.Emulated || res.Entries[0].IRPath != "pipeline/p/job/call" {
				t.Fatalf("entries = %+v, want one emulated entry at the call job", res.Entries)
			}
			d := res.Entries[0].Detail
			if !strings.Contains(d, "ci/child.yml runs inline as jobs call-s1, call-s2, call-s3") {
				t.Errorf("detail = %q", d)
			}
			if got := strings.Contains(d, "a failed child job now fails the pipeline"); got != tc.caveat {
				t.Errorf("failure caveat present = %v, want %v: %q", got, tc.caveat, d)
			}
		})
	}
}

func TestFlattenDoesNotChangeTheSource(t *testing.T) {
	src := gitlabProject(map[string]any{"include": "ci/child.yml"})
	want := ir.Clone(src)
	inline.Flatten(src, "drone")
	if same, diff := ir.Equal(src, want); !same {
		t.Fatalf("Flatten changed its input:\n%s", diff)
	}
}

func TestFlattenKeepsANativeChild(t *testing.T) {
	src := gitlabProject(map[string]any{"include": "ci/child.yml"})
	flat, res := inline.Flatten(src, "gitlab")
	if flat != src || len(res.Entries) != 0 || len(res.Calls) != 0 {
		t.Fatalf("a native target must get the project as it is: %v %+v", flat == src, res)
	}
}

func TestFlattenGatesEveryChildJob(t *testing.T) {
	src := gitlabProject(map[string]any{"include": "ci/child.yml"})
	call := find(t, src, "call")
	call.Manual = true
	other := job("pushonly")
	other.If = eq(ref("event"), str("push"))
	call.Child.Pipeline.Jobs = append(call.Child.Pipeline.Jobs, other)
	flat, _ := inline.Flatten(src, "woodpecker")
	for _, id := range []string{"call-s1", "call-s2", "call-s3"} {
		j := find(t, flat, id)
		if got := expr.String(j.If); got != "branch == 'main'" {
			t.Errorf("%s if = %q, want the condition of the trigger job alone: the parent_pipeline test is true", id, got)
		}
		if !j.Manual {
			t.Errorf("%s must be manual like the trigger job", id)
		}
	}
	j := find(t, flat, "call-pushonly")
	if l, ok := expr.FoldConst(expr.Binary{Op: expr.OpAnd, L: j.If, R: expr.Lit{Kind: expr.LitBool, Bool: true}}).(expr.Lit); !ok || expr.LitTruthy(l) {
		t.Errorf("a child job for the push source never runs in a child pipeline; if = %q", expr.String(j.If))
	}
}

func TestFlattenTriggerVariables(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger map[string]any
		want    string
	}{
		{"forwarded by default", map[string]any{"include": "ci/child.yml"}, "staging"},
		{"forward switched off", map[string]any{"include": "ci/child.yml", "forward": map[string]any{"yaml_variables": false}}, "own"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := gitlabProject(tc.trigger)
			child := find(t, src, "call").Child.Pipeline
			child.Jobs[1].Env = []ir.Var{{Name: "TARGET", Value: ir.Value{Lit: "own"}}}
			flat, _ := inline.Flatten(src, "drone")
			var got []string
			for _, v := range find(t, flat, "call-s2").Env {
				if v.Name == "TARGET" {
					got = append(got, v.Value.Lit)
				}
			}
			if !slices.Equal(got, []string{tc.want}) {
				t.Fatalf("TARGET = %v, want one value %q: a trigger variable wins over a child variable", got, tc.want)
			}
		})
	}
}

func TestFlattenReportsUncarriedKeys(t *testing.T) {
	src := gitlabProject(map[string]any{"include": "ci/child.yml"})
	call := find(t, src, "call")
	call.Child.Pipeline.Concurrency = &ir.Concurrency{Group: "g"}
	call.Child.Pipeline.Ext = ir.Ext{"gitlab": map[string]any{"workflow": 1, "variables": map[string]any{"X": 1}}}
	call.Ext["gitlab"].(map[string]any)["parallel"] = 3
	_, res := inline.Flatten(src, "drone")
	var text []string
	for _, e := range res.Entries {
		if e.Level == caps.Lost {
			text = append(text, string(e.Feature)+": "+e.Detail)
		}
	}
	all := strings.Join(text, "\n")
	for _, want := range []string{"pipeline.concurrency", "gitlab.variables", "gitlab.parallel"} {
		if !strings.Contains(all, want) {
			t.Errorf("no lost entry names %s:\n%s", want, all)
		}
	}
	if strings.Contains(all, "gitlab.workflow") {
		t.Errorf("workflow is folded into the job conditions and needs no lost entry:\n%s", all)
	}
}

func TestFlattenNestedChild(t *testing.T) {
	src := gitlabProject(map[string]any{"include": "ci/child.yml", "strategy": "depend"})
	inner := job("deep")
	innerCall := job("inner", "s1")
	innerCall.Steps = nil
	innerCall.Ext = ir.Ext{"gitlab": map[string]any{"trigger": map[string]any{"include": "ci/inner.yml", "strategy": "depend"}}}
	innerCall.Child = &ir.Child{Native: []string{"gitlab"}, Pipeline: &ir.Pipeline{ID: "p-call-inner", Jobs: []*ir.Job{inner}}}
	child := find(t, src, "call").Child.Pipeline
	child.Jobs = append(child.Jobs, innerCall)
	flat, res := inline.Flatten(src, "tekton")
	if !slices.Contains(ids(flat), "call-inner-deep") || slices.Contains(ids(flat), "call-inner") {
		t.Fatalf("ids = %v, want the nested child inlined under both prefixes", ids(flat))
	}
	if got := needs(find(t, flat, "call-inner-deep")); !slices.Equal(got, []string{"call-s1"}) {
		t.Errorf("deep needs %v, want call-s1", got)
	}
	if len(res.Calls) != 1 || len(res.Calls["pipeline/p/job/call"].Inlined) != 4 {
		t.Errorf("one record for the outer call with the inlined jobs, got %+v", res.Calls)
	}
}

func TestFlattenUniqueIDs(t *testing.T) {
	src := gitlabProject(map[string]any{"include": "ci/child.yml"})
	src.Pipelines[0].Jobs = append(src.Pipelines[0].Jobs, job("call-s1"))
	flat, _ := inline.Flatten(src, "tekton")
	if got := ids(flat); !slices.Contains(got, "call-s1-2") || len(got) != 6 {
		t.Fatalf("ids = %v, want the inlined call-s1 renamed call-s1-2", got)
	}
}

// reusable builds a caller with one call job and a called workflow that declares the inputs.
func reusable(with map[string]ir.Value, secrets any, inputs map[string]any) *ir.Project {
	call := &ir.Job{ID: "release", Needs: []ir.Need{{JobID: "build"}}}
	st := &ir.Step{ID: "release-step-1", Kind: ir.StepPipelineCall, Action: &ir.ActionRef{Kind: ir.ActionLocal, Path: "./.github/workflows/deploy.yml"}, With: with}
	if secrets != nil {
		st.Ext = ir.Ext{"gha": map[string]any{"secrets": secrets}}
	}
	call.Steps = []*ir.Step{st}
	apply := job("apply", "plan")
	apply.If = expr.Unary{Op: expr.OpNot, X: ref("inputs", "dry-run")}
	apply.Env = []ir.Var{{Name: "TOKEN", Value: ir.Value{Kind: ir.ValueSecret, Secret: "token"}}}
	apply.Steps[0].Script = []string{`deploy --env "${{ inputs.environment }}" ${{ github.sha }}`}
	child := &ir.Pipeline{
		ID:       "deploy",
		Triggers: []ir.Trigger{{Kind: ir.TriggerAPI, Ext: ir.Ext{"gha": map[string]any{"event": "workflow_call", "inputs": inputs}}}},
		Jobs:     []*ir.Job{job("plan"), apply},
	}
	call.Child = &ir.Child{Native: []string{"gha"}, Files: []string{".github/workflows/deploy.yml"}, Pipeline: child}
	return &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: []*ir.Job{job("build"), call, job("notify", "release")}}}}
}

var deployInputs = map[string]any{
	"environment": map[string]any{"type": "string"},
	"dry-run":     map[string]any{"type": "boolean", "default": true},
}

func TestFlattenReusableInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		with   map[string]ir.Value
		ifWant string
		script string
	}{
		{"default keeps the job off", map[string]ir.Value{"environment": {Lit: "staging"}}, "false", `deploy --env "staging" ${{ github.sha }}`},
		{"a false input turns the job on", map[string]ir.Value{"environment": {Lit: "prod"}, "dry-run": {Lit: "false"}}, "", `deploy --env "prod" ${{ github.sha }}`},
		{"an expression is passed on", map[string]ir.Value{"environment": {Kind: ir.ValueExpr, Expr: ref("branch")}, "dry-run": {Lit: "false"}}, "", `deploy --env "${{ branch }}" ${{ github.sha }}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flat, res := inline.Flatten(reusable(tc.with, nil, deployInputs), "gitlab")
			a := find(t, flat, "release-apply")
			got := ""
			if a.If != nil {
				got = expr.String(a.If)
			}
			if got != tc.ifWant {
				t.Errorf("if = %q, want %q", got, tc.ifWant)
			}
			if a.Steps[0].Script[0] != tc.script {
				t.Errorf("script = %q, want %q", a.Steps[0].Script[0], tc.script)
			}
			if got := needs(find(t, flat, "notify")); !slices.Equal(got, []string{"release-apply"}) {
				t.Errorf("a reusable workflow blocks: notify needs %v, want the sink of the child", got)
			}
			if got := needs(find(t, flat, "release-plan")); !slices.Equal(got, []string{"build"}) {
				t.Errorf("release-plan needs %v, want build", got)
			}
			if len(res.Entries) != 1 {
				t.Errorf("entries = %+v, want one", res.Entries)
			}
		})
	}
}

func TestFlattenReusableSecrets(t *testing.T) {
	mapped := map[string]any{"token": "${{ secrets.DEPLOY_TOKEN }}"} // #nosec G101 -- test fixture, no credential
	for _, tc := range []struct {
		name    string
		secrets any
		want    ir.Value
	}{
		{"a map renames the secret", mapped, ir.Value{Kind: ir.ValueSecret, Secret: "DEPLOY_TOKEN"}},
		{"inherit keeps the name", "inherit", ir.Value{Kind: ir.ValueSecret, Secret: "token"}},
		{"a secret that the call omits is empty", map[string]any{}, ir.Value{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flat, _ := inline.Flatten(reusable(map[string]ir.Value{"environment": {Lit: "x"}}, tc.secrets, deployInputs), "gitlab")
			if got := find(t, flat, "release-apply").Env[0].Value; got != tc.want {
				t.Fatalf("TOKEN = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestFlattenReusableUndeclaredInput(t *testing.T) {
	_, res := inline.Flatten(reusable(map[string]ir.Value{"environment": {Lit: "x"}, "stray": {Lit: "1"}}, nil, deployInputs), "gitlab")
	var got bool
	for _, e := range res.Entries {
		got = got || (e.Level == caps.Lost && strings.Contains(e.Detail, "stray"))
	}
	if !got {
		t.Fatalf("an input that the workflow does not declare needs a lost entry: %+v", res.Entries)
	}
}

func TestFlattenMatrix(t *testing.T) {
	src := reusable(map[string]ir.Value{"environment": {Lit: "x"}}, nil, deployInputs)
	call := find(t, src, "release")
	call.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "os", Values: []string{"a", "b"}}}}
	flat, res := inline.Flatten(src, "gitlab")
	for _, id := range []string{"release-plan", "release-apply"} {
		if find(t, flat, id).Matrix == nil {
			t.Errorf("%s must run once per combination of the call matrix", id)
		}
	}
	if !strings.Contains(res.Entries[0].Detail, "matrix") {
		t.Errorf("detail = %q, want the matrix caveat", res.Entries[0].Detail)
	}

	src = reusable(map[string]ir.Value{"environment": {Lit: "x"}}, nil, deployInputs)
	find(t, src, "release").Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "os", Values: []string{"a"}}}}
	find(t, src, "release").Child.Pipeline.Jobs[0].Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "x", Values: []string{"1"}}}}
	flat, res = inline.Flatten(src, "gitlab")
	if find(t, flat, "release").Child != nil || slices.Contains(ids(flat), "release-plan") {
		t.Errorf("two matrices cannot meet in one job: the call must stay: %v", ids(flat))
	}
	if len(res.Entries) != 1 || res.Entries[0].Level != caps.Lost {
		t.Errorf("entries = %+v, want one lost entry", res.Entries)
	}
	if rec, ok := res.Calls["pipeline/ci/job/release"]; !ok || rec.Job.Child == nil || len(rec.Inlined) != 0 {
		t.Errorf("the record must keep the call with its child so a return restores the file: %+v", res.Calls)
	}
	var step *ir.Step
	for _, st := range find(t, flat, "release").Steps {
		if st.Kind == ir.StepPipelineCall {
			step = st
		}
	}
	if step == nil {
		t.Fatalf("the dropped call must keep its call step: %+v", find(t, flat, "release").Steps)
	}
	if why := emitkit.CallReason(step, "GitLab"); !strings.Contains(why, "matrix") || strings.Contains(why, "remote") {
		t.Errorf("call reason = %q, want the matrix reason, not the remote-file one", why)
	}
	for _, st := range res.Calls["pipeline/ci/job/release"].Job.Steps {
		if _, ok := st.Ext[emitkit.ExtCallUnrunnable]; ok {
			t.Errorf("the stored call must not carry the marker: %+v", st.Ext)
		}
	}
}

func TestRemapPaths(t *testing.T) {
	in := []string{"pipeline/c", "pipeline/c/job/x/step/s", "pipeline/c/trigger/0", "pipeline/cc/job/x", "pipeline/other"}
	entries := make([]report.Entry, len(in))
	for i, p := range in {
		entries[i] = report.Entry{IRPath: p}
	}
	got := inline.RemapPaths(entries, "c", "p", "call")
	want := []string{"pipeline/p/job/call", "pipeline/p/job/call-x/step/s", "pipeline/p/job/call/trigger/0", "pipeline/cc/job/x", "pipeline/other"}
	for i := range want {
		if got[i].IRPath != want[i] {
			t.Errorf("%s -> %s, want %s", in[i], got[i].IRPath, want[i])
		}
	}
}

func TestFlattenHidesParentVariablesFromTheChild(t *testing.T) {
	for _, tc := range []struct {
		trigger  map[string]any
		name     string
		wantRuns bool
		wantEnv  bool
	}{
		{name: "forwarded by default", trigger: map[string]any{"include": "ci/child.yml"}, wantRuns: true},
		{name: "yaml variables off", trigger: map[string]any{"include": "ci/child.yml", "forward": map[string]any{"yaml_variables": false}}, wantEnv: true},
		{name: "pipeline variables on", trigger: map[string]any{"include": "ci/child.yml", "forward": map[string]any{"yaml_variables": false, "pipeline_variables": true}}, wantRuns: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := gitlabProject(tc.trigger)
			src.Pipelines[0].Env = []ir.Var{{Name: "PARENT", Value: ir.Value{Lit: "x"}}}
			src.Pipelines[0].Jobs[1].Env = nil
			s2 := find(t, src, "call").Child.Pipeline.Jobs[1]
			s2.If = eq(ref("env", "PARENT"), str("x"))
			flat, _ := inline.Flatten(src, "drone")
			j := find(t, flat, "call-s2")
			folded := expr.FoldConst(j.If)
			runs := folded == nil || expr.String(folded) != "false"
			if runs != tc.wantRuns {
				t.Errorf("condition on a parent variable = %v, want runs=%v", expr.String(j.If), tc.wantRuns)
			}
			empty := slices.ContainsFunc(j.Env, func(v ir.Var) bool { return v.Name == "PARENT" && v.Value == (ir.Value{}) })
			if empty != tc.wantEnv {
				t.Errorf("PARENT set empty in the job = %v, want %v", empty, tc.wantEnv)
			}
		})
	}
}

func TestFlattenGatesChildJobsWithTheChildGate(t *testing.T) {
	for _, tc := range []struct {
		gate  expr.Expr
		check func(t *testing.T, cond string)
		name  string
	}{
		{name: "tag gate", gate: eq(ref("tag"), str("v1")), check: func(t *testing.T, cond string) {
			if !strings.Contains(cond, "tag") {
				t.Errorf("condition %q must hold the gate", cond)
			}
		}},
		{name: "push source gate never starts from a parent", gate: eq(ref("event"), str("push")), check: func(t *testing.T, cond string) {
			if cond != "false" {
				t.Errorf("condition %q, want false", cond)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := gitlabProject(map[string]any{"include": "ci/child.yml"})
			find(t, src, "call").Child.Gate = tc.gate
			flat, _ := inline.Flatten(src, "drone")
			for _, id := range []string{"call-s1", "call-s2", "call-s3"} {
				tc.check(t, expr.String(expr.FoldConst(expr.Binary{Op: expr.OpAnd, L: expr.Lit{Kind: expr.LitBool, Bool: true}, R: find(t, flat, id).If})))
			}
		})
	}
}

func TestFlattenGateJobFailsWhenTheChildIsNotCreated(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger map[string]any
		zNeeds  []string
	}{
		{"depend", map[string]any{"include": "ci/child.yml", "strategy": "depend"}, []string{"call-created", "call-s2", "call-s3"}},
		{"no depend", map[string]any{"include": "ci/child.yml"}, []string{"a", "call-created"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := gitlabProject(tc.trigger)
			call := find(t, src, "call")
			call.Manual = true
			call.Child.Gate = eq(ref("tag"), str("v1"))
			flat, res := inline.Flatten(src, "drone")
			gate := find(t, flat, "call-created")
			if !gate.Manual || gate.If == nil || expr.String(gate.If) != expr.String(call.If) {
				t.Errorf("gate job manual=%v if=%v, want the manual flag and the condition of the call", gate.Manual, gate.If)
			}
			if got := needs(gate); !slices.Equal(got, []string{"a"}) {
				t.Errorf("gate job needs %v, want what the call needs", got)
			}
			if len(gate.Steps) == 0 || gate.Steps[0].If == nil || !strings.Contains(strings.Join(gate.Steps[0].Script, ""), "exit 1") {
				t.Fatalf("gate steps = %+v, want a guarded exit 1", gate.Steps)
			}
			if got := expr.String(gate.Steps[0].If); !strings.Contains(got, "tag") || !strings.Contains(got, "!") {
				t.Errorf("exit guard = %q, want the negated child gate", got)
			}
			if got := needs(find(t, flat, "call-s1")); !slices.Equal(got, []string{"a", "call-created"}) {
				t.Errorf("child root needs %v, want a and call-created", got)
			}
			if got := needs(find(t, flat, "z")); !slices.Equal(got, tc.zNeeds) {
				t.Errorf("z needs %v, want %v", got, tc.zNeeds)
			}
			if !strings.Contains(res.Entries[0].Detail, "call-created fails when the workflow rules") || res.Entries[0].Level != caps.Emulated {
				t.Errorf("entry = %+v, want an emulated verdict with the gate caveat", res.Entries[0])
			}
		})
	}
}

func TestFlattenWithoutGateAddsNoGateJob(t *testing.T) {
	flat, _ := inline.Flatten(gitlabProject(map[string]any{"include": "ci/child.yml"}), "drone")
	if slices.Contains(ids(flat), "call-created") {
		t.Errorf("job ids = %v, want no gate job", ids(flat))
	}
}
