package gitlab_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func noTriggerPipeline(id string, events map[string]any) *ir.Pipeline {
	pl := &ir.Pipeline{ID: id, Jobs: []*ir.Job{glJob("deploy", glStep("./deploy.sh"))}}
	if events != nil {
		pl.Ext = ir.Ext{"gha": map[string]any{"events": events}}
	}
	return pl
}

func TestForeignPipelineWithoutTriggersNeverRuns(t *testing.T) {
	for _, ev := range []string{"release", "issues", "merge_group"} {
		t.Run(ev, func(t *testing.T) {
			p := &ir.Project{Origin: ir.Origin{Provider: "gha", Dialect: "gha"},
				Pipelines: []*ir.Pipeline{noTriggerPipeline("d", map[string]any{ev: map[string]any{}})}}
			files, rep := emitFiles(t, p)
			doc := string(files[rootFile])
			if !strings.Contains(doc, "workflow:\n  rules:\n    - when: never") {
				t.Errorf("no workflow guard:\n%s", doc)
			}
			if !hasEntry(rep, ev) {
				t.Errorf("report does not name the event %q: %+v", ev, rep.Entries)
			}
		})
	}
}

func TestGitlabPipelineWithoutTriggersKeepsNoWorkflow(t *testing.T) {
	p := &ir.Project{Origin: ir.Origin{Provider: "gitlab", Dialect: "gitlab"}, Pipelines: []*ir.Pipeline{noTriggerPipeline("d", nil)}}
	doc := emitDoc(t, p)
	if strings.Contains(doc, "workflow:") {
		t.Errorf("unexpected workflow:\n%s", doc)
	}
}

func TestFoldedForeignPipelineWithoutTriggersIsDisabled(t *testing.T) {
	ci := &ir.Pipeline{ID: "ci", Triggers: []ir.Trigger{{Kind: ir.TriggerPush}}, Jobs: []*ir.Job{glJob("build", glStep("make"))}}
	rel := noTriggerPipeline("rel", map[string]any{"release": map[string]any{}})
	p := &ir.Project{Origin: ir.Origin{Provider: "gha", Dialect: "gha"}, Pipelines: []*ir.Pipeline{ci, rel}}
	files, rep := emitFiles(t, p)
	doc := string(files[rootFile])
	i := strings.Index(doc, "rel:deploy:")
	if i < 0 || !strings.Contains(doc[i:], "when: never") {
		t.Errorf("rel:deploy is not disabled:\n%s", doc)
	}
	if !hasEntry(rep, "release") {
		t.Errorf("report does not name the event: %+v", rep.Entries)
	}
}

func TestDroneSourceWithoutTriggerKeepsItsEventsOnGitLab(t *testing.T) {
	p := &ir.Project{Origin: ir.Origin{Provider: "drone", Dialect: "drone"}, Pipelines: []*ir.Pipeline{noTriggerPipeline("d", nil)}}
	files, rep := emitFiles(t, p)
	doc := string(files[rootFile])
	if strings.Contains(doc, "when: never") {
		t.Errorf("the pipeline never starts:\n%s", doc)
	}
	for _, src := range []string{`"push"`, `"merge_request_event"`, `"web"`} {
		if !strings.Contains(doc, src) {
			t.Errorf("pipeline source %s missing:\n%s", src, doc)
		}
	}
	if !hasEntry(rep, "rollback") {
		t.Errorf("no entry names the events GitLab lacks: %+v", rep.Entries)
	}
}
