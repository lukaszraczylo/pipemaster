package argo_test

import (
	"testing"
)

func TestEditedArtifactPathDoesNotRestoreTheJobArtifact(t *testing.T) {
	pl, rep := editedMarked(t, "path: /workspace/out/bin", "path: /workspace/out/other")
	for _, a := range jobOf(pl, "build").Artifacts {
		if len(a.Paths) > 0 && a.Paths[0] == "out/bin" {
			t.Errorf("artifact came back with the old path: %+v", a)
		}
	}
	wantIgnored(t, rep)
}
