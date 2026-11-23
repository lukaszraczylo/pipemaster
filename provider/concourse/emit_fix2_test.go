package concourse_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestUnspellableConditionOnAnEnvironmentJobReportsNoFalseEdit(t *testing.T) {
	j := scriptJob("deploy", "make deploy")
	j.If = eqRef("event", "push")
	j.Environment = &ir.Environment{Name: "prod"}
	p := project(j)
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main")}
	files, _ := emit(t, p)
	_, rep := reparse(t, files)
	if hasFeatureEntry(rep, string(caps.JobManual), "marker ignored") {
		t.Fatalf("an unedited file reported an edit: %v", rep.Entries)
	}
}

// editedFiles emits p, replaces old with replacement and returns the edited file.
func editedFile(t *testing.T, p *ir.Project, old, replacement string) map[string]string {
	t.Helper()
	files, _ := emit(t, p)
	src := files[".concourse/ci.yml"]
	if !strings.Contains(src, old) {
		t.Fatalf("%q is not in the file:\n%s", old, src)
	}
	return map[string]string{"ci.yml": strings.Replace(src, old, replacement, 1)}
}
