package actions_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
)

func TestTargetWritesEquivalent(t *testing.T) {
	cases := map[string]struct {
		t    actions.Target
		want bool
	}{
		"native implicit":   {actions.Target{Kind: actions.KindImplicit, Support: caps.Support{Level: caps.Native}}, true},
		"emulated implicit": {actions.Target{Kind: actions.KindImplicit, Support: caps.Support{Level: caps.Emulated}}, true},
		"native image":      {actions.Target{Kind: actions.KindImage, Support: caps.Support{Level: caps.Native}}, false},
		"external implicit": {actions.Target{Kind: actions.KindImplicit, Support: caps.Support{Level: caps.External}}, false},
		"native upload":     {actions.Target{Kind: actions.KindUpload, Support: caps.Support{Level: caps.Native}}, false},
		"lost image":        {actions.Target{Kind: actions.KindImage, Support: caps.Support{Level: caps.Lost}}, false},
	}
	for name, c := range cases {
		if got := c.t.WritesEquivalent(); got != c.want {
			t.Errorf("%s: WritesEquivalent = %v, want %v", name, got, c.want)
		}
	}
}
