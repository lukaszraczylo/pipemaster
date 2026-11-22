package woodpecker_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
)

func TestDisabledStepReportsItsLostCondition(t *testing.T) {
	st := wpStep("main", "true")
	st.WorkDir = "${{ hashFiles('**/go.sum') }}"
	text, rep := one(t, wpProject("gha", wpJob("a", st)))
	for _, e := range rep.Entries {
		if e.Feature == caps.StepIf && e.IRPath == "pipeline/woodpecker/job/a/step/main" {
			return
		}
	}
	t.Errorf("no lost step.if entry for the disabled step: %v\n%s", rep.Entries, text)
}
