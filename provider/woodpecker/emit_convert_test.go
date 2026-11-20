package woodpecker_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	_ "github.com/lukaszraczylo/pipemaster/provider/gha"
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

func roundTripTriggers(t *testing.T, on string) (want, got []ir.Trigger, back *ir.Project) {
	t.Helper()
	p := parseProject(t, "gha", map[string]string{".github/workflows/ci.yml": "on:\n" + on + "jobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make\n"})
	files, _ := emitFiles(t, p)
	raw := map[string]string{}
	for n, b := range files {
		raw[n] = b
	}
	back = parseProject(t, "woodpecker", raw)
	return p.Pipelines[0].Triggers, back.Pipelines[0].Triggers, back
}

func TestGHATagFilterSurvivesWoodpecker(t *testing.T) {
	for name, on := range map[string]string{
		"tags include": "  push:\n    tags: [\"v*\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			want, got, _ := roundTripTriggers(t, on)
			if !reflect.DeepEqual(want, got) {
				t.Errorf("triggers changed:\nwant %+v\ngot  %+v", want, got)
			}
		})
	}
}

func TestBarePushSurvivesWoodpecker(t *testing.T) {
	want, got, _ := roundTripTriggers(t, "  push:\n")
	if len(want) != 2 || !reflect.DeepEqual(want, got) {
		t.Errorf("a bare push is push plus tag:\nwant %+v\ngot  %+v", want, got)
	}
}

func TestTagIncludeAndExcludeIdentity(t *testing.T) {
	p := wpProject("woodpecker", wpJob("ci", wpStep("s", "x")))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}, Exclude: []string{"v0*"}}}}
	files, _ := emitFiles(t, p)
	back := reparse(t, files)
	if !reflect.DeepEqual(p.Pipelines[0].Triggers, back.Pipelines[0].Triggers) {
		t.Errorf("want %+v, got %+v", p.Pipelines[0].Triggers, back.Pipelines[0].Triggers)
	}
}
