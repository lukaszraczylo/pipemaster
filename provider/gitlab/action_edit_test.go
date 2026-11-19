package gitlab_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func actionProject() *ir.Project {
	return glProject("github", glJob("build",
		ghaUses("cache", "cache", map[string]string{"path": "vendor", "key": "k"}),
		glStep("make"),
		ghaUses("up", "upload-artifact", map[string]string{"name": "bin", "path": "out/"}),
	))
}

func stepIDs(j *ir.Job) []string {
	var ids []string
	for _, s := range j.Steps {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestActionStepsRestoreWhenUnedited(t *testing.T) {
	_, back, _ := emitParse(t, actionProject(), nil)
	if got := stepIDs(back.Pipelines[0].Jobs[0]); strings.Join(got, ",") != "cache,script,up" {
		t.Fatalf("steps = %v", got)
	}
}

func TestEditedCacheBlockKeepsParsedFieldsAndNotes(t *testing.T) {
	_, back, rep := emitParse(t, actionProject(), func(s string) string {
		return strings.Replace(s, "- vendor", "- vendor-edited", 1)
	})
	j := back.Pipelines[0].Jobs[0]
	if len(j.Cache) != 1 || j.Cache[0].Paths[0] != "vendor-edited" {
		t.Errorf("cache = %+v", j.Cache)
	}
	for _, s := range j.Steps {
		if s.ID == "cache" && s.Kind == ir.StepAction {
			t.Error("cache action step restored over an edited block")
		}
	}
	if !ignoredNote(rep) {
		t.Error("no marker ignored note")
	}
}

func TestEditedArtifactsBlockKeepsParsedFieldsAndNotes(t *testing.T) {
	_, back, rep := emitParse(t, actionProject(), func(s string) string {
		return strings.Replace(s, "- out/", "- out-edited/", 1)
	})
	j := back.Pipelines[0].Jobs[0]
	if len(j.Artifacts) != 1 || j.Artifacts[0].Paths[0] != "out-edited/" {
		t.Errorf("artifacts = %+v", j.Artifacts)
	}
	for _, s := range j.Steps {
		if s.ID == "up" && s.Kind == ir.StepAction {
			t.Error("upload action step restored over an edited block")
		}
	}
	if !ignoredNote(rep) {
		t.Error("no marker ignored note")
	}
}
