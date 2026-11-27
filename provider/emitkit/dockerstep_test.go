package emitkit_test

import (
	"slices"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

func dockerProject(with map[string]ir.Value) *ir.Project {
	st := &ir.Step{ID: "s", Kind: ir.StepAction, Action: &ir.ActionRef{Kind: ir.ActionDocker, Image: "alpine:3.20"}, With: with}
	return &ir.Project{Pipelines: []*ir.Pipeline{{ID: "p", Jobs: []*ir.Job{{ID: "j", Steps: []*ir.Step{st}}}}}}
}

func lit(s string) ir.Value { return ir.Value{Lit: s} }

func TestDockerSteps(t *testing.T) {
	tests := []struct {
		name       string
		with       map[string]ir.Value
		wantKind   ir.StepKind
		wantScript []string
		rewritten  bool
	}{
		{name: "entrypoint and quoted args", with: map[string]ir.Value{"entrypoint": lit("/bin/sh"), "args": lit(`-c "echo a b"`)},
			wantKind: ir.StepScript, wantScript: []string{"/bin/sh -c 'echo a b'"}, rewritten: true},
		{name: "entrypoint only", with: map[string]ir.Value{"entrypoint": lit("/bin/true")},
			wantKind: ir.StepScript, wantScript: []string{"/bin/true"}, rewritten: true},
		{name: "a quote in an argument", with: map[string]ir.Value{"entrypoint": lit("echo"), "args": lit(`"it's"`)},
			wantKind: ir.StepScript, wantScript: []string{`echo 'it'\''s'`}, rewritten: true},
		{name: "bare image stays an action", wantKind: ir.StepAction},
		{name: "args without entrypoint stay an action", with: map[string]ir.Value{"args": lit("x")}, wantKind: ir.StepAction},
		{name: "another input stays an action", with: map[string]ir.Value{"entrypoint": lit("sh"), "other": lit("x")}, wantKind: ir.StepAction},
		{name: "a secret input stays an action", with: map[string]ir.Value{"entrypoint": lit("sh"), "args": {Kind: ir.ValueSecret, Secret: "S"}}, wantKind: ir.StepAction},
		{name: "an open quote stays an action", with: map[string]ir.Value{"entrypoint": lit("sh"), "args": lit(`"x`)}, wantKind: ir.StepAction},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := dockerProject(tt.with)
			out, entries, orig := emitkit.DockerSteps(src)
			if src.Pipelines[0].Jobs[0].Steps[0].Kind != ir.StepAction {
				t.Fatal("the source project changed")
			}
			st := out.Pipelines[0].Jobs[0].Steps[0]
			if st.Kind != tt.wantKind || !slices.Equal(st.Script, tt.wantScript) {
				t.Fatalf("step = %v %q, want %v %q", st.Kind, st.Script, tt.wantKind, tt.wantScript)
			}
			if got := len(entries) == 1 && len(orig) == 1; got != tt.rewritten {
				t.Fatalf("entries %d, stored %d, want rewritten=%v", len(entries), len(orig), tt.rewritten)
			}
			if tt.rewritten && (st.Action != nil || st.Image == nil || st.Image.Ref != "alpine:3.20") {
				t.Fatalf("image step = %+v", st)
			}
			if tt.rewritten && !emitkit.DockerEmitted(st, orig["pipeline/p/job/j/step/s"]) {
				t.Fatal("the stored step does not match the step it produced")
			}
		})
	}
}

func TestDockerEmittedRejectsEditedStep(t *testing.T) {
	_, _, orig := emitkit.DockerSteps(dockerProject(map[string]ir.Value{"entrypoint": lit("/bin/sh"), "args": lit("-c x")}))
	o := orig["pipeline/p/job/j/step/s"]
	edited := &ir.Step{Kind: ir.StepScript, Image: &ir.Image{Ref: "alpine:3.20", Entrypoint: []string{""}}, Script: []string{"other"}}
	if emitkit.DockerEmitted(edited, o) {
		t.Fatal("an edited script must not restore")
	}
}
