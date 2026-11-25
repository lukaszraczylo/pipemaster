package argo_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/internal/linttool"
)

// changesWhenRE is the form of Argo's expression tag in a `when`
// (https://argo-workflows.readthedocs.io/en/v4.1.6/variables/, "{{= ... }}") over the output parameter of the
// generated task that computes the path predicate.
var changesWhenRE = regexp.MustCompile(`^\{\{= tasks\['pipemaster-changes'\]\.outputs\.parameters\.[A-Za-z0-9_-]+ == 'true' \}\}$`)

// TestChangesWorkflowPassesArgoLintOffline renders a job with a path predicate and lints the workflow offline. The
// linter checks the structure (the task, its dependency, the output parameter), but not the text of a `when`: it
// also accepts a malformed one. So the form of the expression stands on the pattern above and the documented syntax,
// not on a run in a cluster, which the Kiri node cannot host (DiskPressure).
func TestChangesWorkflowPassesArgoLintOffline(t *testing.T) {
	linttool.RequireArgo(t)
	j := scriptJob("build", "make")
	j.If = expr.Changes("src/**")
	files, _ := emit(t, project(j))
	path := ".argo/workflows/ci.yaml"
	if out, err := linttool.ArgoLint(t, path, []byte(files[path])); err != nil {
		t.Fatalf("argo lint: %v\n%s\n%s", err, out, files[path])
	}
	task := named(t, tasks(t, template(t, files)), "build")
	when, _ := task["when"].(string)
	if !changesWhenRE.MatchString(when) {
		t.Errorf("when = %q, want the {{= tasks['pipemaster-changes'].outputs.parameters.<name> == 'true' }} form", when)
	}
	bad := strings.Replace(files[path], when, "{{= tasks[ == }}", 1)
	if _, err := linttool.ArgoLint(t, path, []byte(bad)); err != nil {
		t.Skipf("argo lint now rejects a malformed when, so it can also vouch for the expression: %v", err)
	}
}
