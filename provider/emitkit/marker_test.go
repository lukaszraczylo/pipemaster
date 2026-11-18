package emitkit

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestMarkerRoundTrip(t *testing.T) {
	m := Marker(caps.TriggerManual, "jobs.build.steps[0]")
	if m != "# pipemaster:emulated trigger.manual jobs.build.steps[0]" {
		t.Fatalf("marker = %q", m)
	}
	src := []byte("a: 1\n  " + m + "\n" +
		"# pipemaster:emulated onlyfeature\n" +
		"# pipemaster:emulated a b c\n" +
		"x: # pipemaster:emulated trigger.api p\n" +
		Marker(caps.TriggerAPI, "p2") + "\n")
	want := []ir.FeatureUse{
		{IRPath: "jobs.build.steps[0]", Feature: caps.TriggerManual},
		{IRPath: "p2", Feature: caps.TriggerAPI},
	}
	if d := cmp.Diff(want, ParseMarkers(src)); d != "" {
		t.Fatal(d)
	}
}
