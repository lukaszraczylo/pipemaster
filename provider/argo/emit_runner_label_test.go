package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestRunnerTagBecomesNodeSelector(t *testing.T) {
	j := scriptJob("build", "make")
	j.Runner = ir.Runner{Labels: []string{"gitlab-org"}}
	bad := scriptJob("bad", "make")
	bad.Runner = ir.Runner{Labels: []string{"has space"}}
	files, rep := emit(t, project(j, bad))
	all := joinFiles(files)
	if !strings.Contains(all, "nodeSelector:") || !strings.Contains(all, "runner.pipemaster.io/gitlab-org: \"true\"") {
		t.Errorf("a tag must become a template nodeSelector key:\n%s", all)
	}
	if strings.Contains(all, "has space") {
		t.Errorf("an invalid tag must not be written:\n%s", all)
	}
	var external, lost bool
	for _, e := range rep.Entries {
		if e.Feature != caps.JobRunner {
			continue
		}
		external = external || (e.Level == caps.External && strings.Contains(e.Detail, "runner.pipemaster.io/gitlab-org=true"))
		lost = lost || (e.Level == caps.Lost && strings.Contains(e.Detail, "has space"))
	}
	if !external || !lost {
		t.Errorf("want external for the valid tag and lost for the invalid one: %+v", rep.Entries)
	}
}

func TestRunnerSelectorBecomesNodeSelectorEntry(t *testing.T) {
	j := scriptJob("build", "make")
	j.Runner = ir.Runner{Selector: map[string]string{"region": "eu"}}
	bad := scriptJob("bad", "make")
	bad.Runner = ir.Runner{Selector: map[string]string{"bad key": "x"}}
	files, rep := emit(t, project(j, bad))
	all := joinFiles(files)
	if !strings.Contains(all, "region: eu") || strings.Contains(all, "bad key") {
		t.Errorf("a valid selector entry is a nodeSelector entry and an invalid one is not written:\n%s", all)
	}
	wantLevels := map[caps.Level]string{caps.External: "region=eu", caps.Lost: "bad key=x"}
	for lv, sub := range wantLevels {
		found := false
		for _, e := range rep.Entries {
			found = found || (e.Feature == caps.JobRunner && e.Level == lv && strings.Contains(e.Detail, sub))
		}
		if !found {
			t.Errorf("no %v job.runner entry naming %q: %+v", lv, sub, rep.Entries)
		}
	}
}
