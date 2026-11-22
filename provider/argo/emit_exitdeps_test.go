package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestExitTaskKeepsADependsExpressionThatOnlyEndsLikeAnEntryTask(t *testing.T) {
	a := scriptJob("a", "echo a")
	data := scriptJob("data", "echo data")
	report := scriptJob("report", "echo report")
	report.Needs = []ir.Need{{JobID: "data"}}
	report.If = eqStr([]string{"needs", "data", "result"}, "success")
	p := project(a)
	p.Pipelines[0].Finally = []*ir.Job{data, report}
	files, rep := emit(t, p)
	if got := entries(rep, "exit handler task cannot wait"); len(got) > 0 {
		t.Errorf("the task data was taken for a task of the entry DAG: %v", got)
	}
	var exit doc
	for _, tm := range list(get(template(t, files), "spec", "templates")) {
		if m, ok := tm.(doc); ok && m["name"] == "exit-handler" {
			exit = m
		}
	}
	task := named(t, get(exit, "dag", "tasks"), "report")
	if d, _ := task["depends"].(string); !strings.Contains(d, "data.Succeeded") {
		t.Errorf("report depends = %v, want the data.Succeeded test", task["depends"])
	}
}
