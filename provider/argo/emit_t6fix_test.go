package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestEditedOutputArtifactPathDoesNotRestoreTheUpload(t *testing.T) {
	j, rep := editedParse(t, func(s string) string {
		return replaceOnce(t, s, "path: /workspace/out\n", "path: /workspace/other\n")
	})
	if st := stepByID(j, "up"); st != nil && st.Kind == ir.StepAction {
		t.Errorf("the upload came back although the artifact path was edited")
	}
	if !hasEntry(rep, "marker ignored") {
		t.Errorf("no marker ignored entry: %v", rep.Entries)
	}
}
