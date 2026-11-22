package argo_test

import (
	"testing"
)

func TestEditedDisabledTaskKeepsTheFileCondition(t *testing.T) {
	pl, rep := editedMarked(t, "when: '{{= false }}'", "when: '{{= workflow.parameters[\"ctx-branch\"] == \"dev\" }}'")
	if c := jobOf(pl, "d").If; c == nil || cond(c) == "ctx.branch == 'main'" {
		t.Errorf("condition = %v, want the edited one", c)
	}
	wantIgnored(t, rep)
}
