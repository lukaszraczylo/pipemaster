package gitlab

import (
	"errors"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"go.yaml.in/yaml/v3"
)

// A cache block that cannot be read back is reported lost and written nowhere, with no marker to restore it.
func TestCacheActionUnreadableBlockIsLostAndAbsent(t *testing.T) {
	old := readCacheBlock
	readCacheBlock = func(*yaml.Node) (ir.Cache, error) { return ir.Cache{}, errors.New("boom") }
	t.Cleanup(func() { readCacheBlock = old })

	step := &ir.Step{ID: "cache", Kind: ir.StepAction,
		With:   map[string]ir.Value{"path": {Lit: "vendor"}, "key": {Lit: "k"}},
		Action: &ir.ActionRef{Kind: ir.ActionRepo, Owner: "actions", Repo: "cache", Ref: "v4"}}
	p := &ir.Project{Origin: ir.Origin{Provider: "github", Dialect: "github"}, Pipelines: []*ir.Pipeline{{
		ID: "gitlab-ci", Triggers: []ir.Trigger{{Kind: ir.TriggerPush}}, Jobs: []*ir.Job{{ID: "build", Steps: []*ir.Step{step}}},
	}}}
	files, rep, err := New().Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	out := string(files[rootFile])
	if strings.Contains(out, "cache:") || strings.Contains(out, "pipemaster:emulated step.action") {
		t.Errorf("block or marker written:\n%s", out)
	}
	for _, e := range rep.Entries {
		if e.Level == caps.Lost && e.Feature == caps.StepAction && strings.Contains(e.Detail, "cannot be read back") {
			return
		}
	}
	t.Error("no lost entry")
}
