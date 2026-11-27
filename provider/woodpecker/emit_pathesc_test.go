package woodpecker_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// A GitLab path glob with a dollar sign has no safe Woodpecker spelling: the job must be disabled and reported, and
// the glob must never reach the file unescaped.
func TestPathGlobWithDollarIsRefusedOrEscaped(t *testing.T) {
	j := wpJob("ci", wpStep("s", "true"))
	j.If = expr.Changes("src/$x/*.go")
	body, rep := one(t, wpProject(provider.GitLab, j))
	if strings.Contains(body, "src/$x") {
		t.Errorf("the glob is written unescaped:\n%s", body)
	}
	if !strings.Contains(body, `evaluate: "false"`) && !strings.Contains(body, "src/$$x/*.go") {
		t.Errorf("the glob is neither refused nor escaped:\n%s", body)
	}
	if len(rep.Entries) == 0 {
		t.Error("the refusal is not reported")
	}
}
