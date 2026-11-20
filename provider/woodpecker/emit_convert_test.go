package woodpecker_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	_ "github.com/lukaszraczylo/pipemaster/provider/gitlab"
)

const gitlabEntrypoint = `build:
  image:
    name: alpine:3
    entrypoint: ["/bin/sh", "-c"]
  script:
    - make
`

func TestGitLabImageEntrypointIsReportedLost(t *testing.T) {
	p := parseProject(t, "gitlab", map[string]string{".gitlab-ci.yml": gitlabEntrypoint})
	_, rep := emitFiles(t, p)
	found := false
	for _, e := range rep.Entries {
		found = found || (e.Feature == caps.StepImage && e.Level == caps.Lost && strings.Contains(e.Detail, "entrypoint"))
	}
	if !found {
		t.Errorf("a job entrypoint that Woodpecker overrides must be reported: %+v", rep.Entries)
	}
}

// parseProject reads files with the source provider.
func parseProject(t *testing.T, from string, files map[string]string) *ir.Project {
	t.Helper()
	src, err := provider.Get(from)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := src.Parse(repo(files))
	if err != nil {
		t.Fatalf("Parse %s: %v", from, err)
	}
	return p
}
