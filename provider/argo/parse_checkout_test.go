package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

// withoutMarkers is the file as a person would write it: the pipemaster comment lines are gone.
func withoutMarkers(files map[string]string) map[string]string {
	out := map[string]string{}
	for n, text := range files {
		var keep []string
		for _, line := range strings.Split(text, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "# pipemaster:") {
				keep = append(keep, line)
			}
		}
		out[n] = strings.Join(keep, "\n")
	}
	return out
}

func TestParseDropsOnlyTheClonePipemasterWrote(t *testing.T) {
	p := project(scriptJob("build", "ls"))
	p.Origin = ir.Origin{Provider: "drone", Dialect: "drone"}
	files, _ := emit(t, p)
	if !strings.Contains(joinFiles(files), "source-checkout") {
		t.Fatalf("the emitter wrote no clone:\n%s", joinFiles(files))
	}

	generated := roundTrip(t, files)
	if n := len(generated.Pipelines[0].Jobs[0].Steps); n != 1 {
		t.Errorf("a marked clone must be dropped on parse: %d steps", n)
	}

	native := roundTrip(t, withoutMarkers(files))
	steps := native.Pipelines[0].Jobs[0].Steps
	if len(steps) != 2 || steps[0].Kind != ir.StepAction {
		t.Errorf("a clone with no marker is native and must stay: %d steps", len(steps))
	}
}
