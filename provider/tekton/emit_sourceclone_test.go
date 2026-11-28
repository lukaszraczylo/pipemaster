package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestSourceCloneIsWrittenAsTektonClone(t *testing.T) {
	lit := func(n, v string) ir.Var { return ir.Var{Name: n, Value: ir.Value{Lit: v}} }
	tests := []struct {
		ext    ir.Ext
		name   string
		origin string
		env    []ir.Var
		clone  bool
	}{
		{name: "drone default clone", origin: "drone", clone: true},
		{name: "drone clone.disable", origin: "drone", ext: ir.Ext{"drone": map[string]any{"clone": map[string]any{"disable": true}}}},
		{name: "woodpecker default clone", origin: "woodpecker", clone: true},
		{name: "woodpecker skip_clone", origin: "woodpecker", ext: ir.Ext{"woodpecker": map[string]any{"skip_clone": true}}},
		{name: "gitlab default clone", origin: "gitlab", clone: true},
		{name: "gitlab GIT_STRATEGY none", origin: "gitlab", env: []ir.Var{lit("GIT_STRATEGY", "none")}},
		{name: "gha source keeps its own steps", origin: "gha"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := scriptJob("build", "ls")
			j.Ext = tc.ext
			p := project(j)
			p.Origin = ir.Origin{Provider: tc.origin, Dialect: tc.origin}
			p.Pipelines[0].Env = tc.env
			files, _ := emitRaw(t, p)
			text := joinFiles(files)
			if got := strings.Contains(text, "git-clone"); got != tc.clone {
				t.Errorf("clone present = %v, want %v:\n%s", got, tc.clone, text)
			}
			if strings.Contains(text, "source-checkout") && !tc.clone {
				t.Errorf("no clone must leave no checkout step:\n%s", text)
			}
			back, _ := reparse(t, files)
			if n := len(back.Pipelines[0].Jobs[0].Steps); n != 1 {
				t.Errorf("a return must drop the generated clone: %d steps", n)
			}
		})
	}
}

func TestImageNamedByContextIsLostOnSchedulesAndManualRuns(t *testing.T) {
	for _, kind := range []ir.TriggerKind{ir.TriggerSchedule, ir.TriggerManual, ir.TriggerPush} {
		t.Run(string(kind), func(t *testing.T) {
			j := scriptJob("build", "ls")
			j.Steps[0].Image.Ref = "alpine:${{ ref_name }}"
			p := project(j)
			p.Pipelines[0].Triggers = []ir.Trigger{{Kind: kind, Cron: "0 3 * * *"}}
			_, rep := emitRaw(t, p)
			var found bool
			for _, e := range rep.Entries {
				found = found || (strings.Contains(e.Detail, "pod pulls the image before any script runs") && strings.Contains(e.Detail, "ctx-ref-name"))
			}
			if want := kind != ir.TriggerPush; found != want {
				t.Fatalf("entry present = %v, want %v: %+v", found, want, rep.Entries)
			}
		})
	}
}
