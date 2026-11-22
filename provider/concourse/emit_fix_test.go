package concourse_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestJobConditionWithAnUnspellableRefIsAFailSafe(t *testing.T) {
	j := scriptJob("test", "make")
	j.If = eqRef("event", "push")
	d := scriptJob("deploy", "make deploy")
	d.Needs = []ir.Need{{JobID: "test"}}
	p := project(j, d)
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main")}
	files, rep := emit(t, p)
	doc := pipelineDoc(t, files)
	for _, s := range list(jobDoc(t, doc, "test")["plan"]) {
		if dict(s)["trigger"] == true {
			t.Fatalf("the job triggers though its condition has no spelling: %v", s)
		}
	}
	if strings.Contains(files[".concourse/ci.yml"], "exit 0 # pipemaster:guard") {
		t.Fatalf("a vacuous task stands for the job:\n%s", files[".concourse/ci.yml"])
	}
	if !hasFeatureEntry(rep, string(caps.JobIf), "") {
		t.Fatalf("no JobIf entry: %v", rep.Entries)
	}
}
