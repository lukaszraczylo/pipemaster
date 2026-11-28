package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
)

func TestArtifactsNameTheRepositoryTheyNeed(t *testing.T) {
	_, rep := emit(t, artifactChain())
	for _, f := range []caps.Feature{caps.JobArtifacts, caps.JobNeedsArtifacts} {
		var found bool
		for _, e := range rep.Entries {
			found = found || e.Feature == f && e.Level == caps.External && strings.Contains(e.Detail, "artifact repository")
		}
		if !found {
			t.Errorf("no external %s entry that names the artifact repository: %+v", f, rep.Entries)
		}
	}
}

func TestNeededArtifactsNameTheirNewPath(t *testing.T) {
	_, rep := emit(t, artifactChain())
	for _, e := range rep.Entries {
		if e.Feature == caps.JobNeedsArtifacts && strings.Contains(e.Detail, "/workspace/artifacts/<name>") {
			return
		}
	}
	t.Fatalf("no job.needs.artifacts entry that names the artifacts directory: %+v", rep.Entries)
}
