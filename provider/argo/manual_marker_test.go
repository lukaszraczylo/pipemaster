package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestManualTriggerComesBackFromTheMarkerAtItsIndex(t *testing.T) {
	cases := map[string][]ir.Trigger{
		"first":  {{Kind: ir.TriggerManual}, {Kind: ir.TriggerPR}, {Kind: ir.TriggerPush}},
		"middle": {{Kind: ir.TriggerPR}, {Kind: ir.TriggerManual}, {Kind: ir.TriggerPush}},
		"last":   {{Kind: ir.TriggerPR}, {Kind: ir.TriggerPush}, {Kind: ir.TriggerManual}},
		"alone":  {{Kind: ir.TriggerManual}},
	}
	for name, trs := range cases {
		t.Run(name, func(t *testing.T) {
			p := triggerProject(trs...)
			files, _ := emit(t, p)
			back := roundTrip(t, files)
			stripExt(back)
			ir.Normalize(p)
			if ok, diff := ir.Equal(p, back); !ok {
				t.Fatalf("the manual trigger moved:\n%s\n%s", diff, joinFiles(files))
			}
		})
	}
}

func TestManualMarkerOfAnotherPipelineIsIgnored(t *testing.T) {
	files, _ := emit(t, triggerProject(ir.Trigger{Kind: ir.TriggerManual}, ir.Trigger{Kind: ir.TriggerPush}))
	const file = ".argo/workflows/ci.yaml"
	files[file] = strings.Replace(files[file], "trigger.manual pipeline/ci/trigger/0", "trigger.manual pipeline/other/trigger/0", 1)
	p, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range p.Pipelines[0].Triggers {
		if tr.Kind == ir.TriggerManual {
			t.Fatalf("an edited marker still made a manual trigger: %+v", p.Pipelines[0].Triggers)
		}
	}
	if !hasEntry(rep, "marker ignored") {
		t.Errorf("no marker ignored entry: %v", rep.Entries)
	}
}
