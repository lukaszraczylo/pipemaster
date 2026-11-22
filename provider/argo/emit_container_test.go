package argo_test

import "testing"

func TestUneditedHeldStepsComeBackAndKeepTheJobContainer(t *testing.T) {
	j, rep := editedParse(t, nil)
	if j.Container == nil || j.Container.Ref != "golang:1.22-alpine" {
		t.Errorf("job container = %v, want golang:1.22-alpine", j.Container)
	}
	for _, id := range []string{"co", "go", "compile", "up"} {
		if stepByID(j, id) == nil {
			t.Errorf("step %s is missing: %v", id, j.Steps)
		}
	}
	if hasEntry(rep, "marker ignored") {
		t.Errorf("the markers were ignored although nothing was edited: %v", ignoredMarkers(rep))
	}
}
