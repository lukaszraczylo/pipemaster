package drone_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
)

func TestDisabledStepReportsItsLostCondition(t *testing.T) {
	st := gstep("main", "true")
	st.WorkDir = "${{ hashFiles('**/go.sum') }}"
	text, rep := emitText(t, gproject(ghaOrigin, gjob("a", st)))
	for _, e := range rep.Entries {
		if e.Feature == caps.StepIf && e.IRPath == "pipeline/drone/job/a/step/main" {
			return
		}
	}
	t.Errorf("no lost step.if entry for the disabled step: %v\n%s", rep.Entries, text)
}
