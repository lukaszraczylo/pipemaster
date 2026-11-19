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

func downloadProject() *ir.Project {
	prod := glJob("build", glStep("make"), ghaUses("up", "upload-artifact", map[string]string{"name": "bin", "path": "out/"}))
	cons := glJob("test", ghaUses("down", "download-artifact", map[string]string{"name": "bin"}), glStep("make test"))
	cons.Needs = []ir.Need{{JobID: "build"}}
	return glProject("github", prod, cons)
}

func hasActionStep(j *ir.Job, id string) bool {
	for _, s := range j.Steps {
		if s.ID == id && s.Kind == ir.StepAction {
			return true
		}
	}
	return false
}

func jobNamed(p *ir.Project, id string) *ir.Job {
	for _, j := range p.Pipelines[0].Jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func TestDownloadStepRestoresWhenUnedited(t *testing.T) {
	_, back, _ := emitParse(t, downloadProject(), nil)
	if !hasActionStep(jobNamed(back, "test"), "down") {
		t.Fatal("download step not restored")
	}
}

func TestEditedNeedsKeepDownloadStepOff(t *testing.T) {
	text, _, _ := emitParse(t, downloadProject(), nil)
	if !strings.Contains(text, "needs:") {
		t.Fatalf("no needs in output:\n%s", text)
	}
	_, back, rep := emitParse(t, downloadProject(), func(s string) string {
		return strings.Replace(s, "artifacts: true", "artifacts: false", 1)
	})
	if hasActionStep(jobNamed(back, "test"), "down") {
		t.Error("download step restored over edited needs")
	}
	if !ignoredNote(rep) {
		t.Error("no marker ignored note")
	}
}

// A marker whose argument lost its cache or artifact block cannot restore anything.
func TestActionMarkerWithoutBlockIsIgnored(t *testing.T) {
	for _, field := range []string{`"cache":`, `"art":`} {
		t.Run(field, func(t *testing.T) {
			_, back, rep := emitParse(t, actionProject(), func(s string) string {
				var out []string
				for _, l := range strings.Split(s, "\n") {
					if strings.Contains(l, "pipemaster:arg") && strings.Contains(l, field) {
						l = dropJSONKey(l, field)
					}
					out = append(out, l)
				}
				return strings.Join(out, "\n")
			})
			j := back.Pipelines[0].Jobs[0]
			if field == `"cache":` && hasActionStep(j, "cache") || field == `"art":` && hasActionStep(j, "up") {
				t.Error("action step restored without its block")
			}
			if !ignoredNote(rep) {
				t.Error("no marker ignored note")
			}
		})
	}
}

// dropJSONKey removes `"key":{...}` (a flat object) from a marker line.
func dropJSONKey(line, key string) string {
	i := strings.Index(line, key)
	j := strings.Index(line[i:], "}") + i + 1
	out := line[:i] + line[j:]
	return strings.Replace(strings.Replace(out, "{,", "{", 1), ",,", ",", 1)
}
