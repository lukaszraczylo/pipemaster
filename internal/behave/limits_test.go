//go:build behave

package behave

import (
	"strings"
	"testing"
)

func TestLimitsCiteTheirSource(t *testing.T) {
	art := Fixture{Requires: []string{reqArtifacts}}
	for _, tc := range []struct {
		name, target, event, wantURL string
		fx                           Fixture
	}{
		{"argo artifacts need a repository", provArgo, EvPushMain, "argo-workflows.readthedocs.io", art},
		{"concourse has no pull request event", provConcourse, EvPR, "pull request", Fixture{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			why := limit(tc.fx, tc.target, tc.event)
			if !strings.Contains(why, tc.wantURL) {
				t.Fatalf("limit = %q, want a reason that holds %q", why, tc.wantURL)
			}
		})
	}
	if why := limit(art, provTekton, EvPushMain); why != "" {
		t.Fatalf("tekton has no artifact limit, got %q", why)
	}
}

func TestEveryActLimitNamesTheActSource(t *testing.T) {
	if !strings.Contains(jobContinueWhy, "nektos/act") || !strings.Contains(jobContinueWhy, "setJobResult") {
		t.Fatalf("the act limit must cite the act source: %q", jobContinueWhy)
	}
}
