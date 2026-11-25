package roundtrip_test

import (
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// droneFires is every event a Drone pipeline can start, by the event name that Drone gives it
// (https://docs.drone.io/pipeline/docker/syntax/trigger/).
var droneFires = []string{"push", "pull_request", "tag", "cron", "custom", "promote", "rollback"}

// droneForms are the event filters of a Drone source: none, positive, and negated.
var droneForms = []struct {
	name             string
	include, exclude []string
}{
	{"no filter", nil, nil},
	{"include push", []string{"push"}, nil},
	{"include push and pull request", []string{"push", "pull_request"}, nil},
	{"exclude pull request", nil, []string{"pull_request"}},
	{"exclude push and tag", nil, []string{"push", "tag"}},
	{"exclude cron", nil, []string{"cron"}},
}

func droneSource(include, exclude []string) string {
	src := "kind: pipeline\ntype: docker\nname: p\n"
	if len(include)+len(exclude) > 0 {
		src += "trigger:\n  event:\n"
		if len(include) > 0 {
			src += "    include: " + quoteList(include) + "\n"
		}
		if len(exclude) > 0 {
			src += "    exclude: " + quoteList(exclude) + "\n"
		}
	}
	return src + "steps:\n- name: s\n  image: alpine\n  commands: [echo hi]\n"
}

// glRuns are the runs GitLab can start: every pipeline source, and a push of a tag. The drone value is the Drone
// event of the same meaning, empty when Drone has none.
var glRuns = []struct{ name, source, branch, tag, drone string }{
	{"branch push", "push", "main", "", "push"},
	{"tag push", "push", "", "v1", "tag"},
	{"merge request", "merge_request_event", "", "", "pull_request"},
	{"schedule", "schedule", "main", "", "cron"},
	{"web", "web", "main", "", "custom"},
	{"api", "api", "main", "", ""},
	{"trigger", "trigger", "main", "", ""},
	{"pipeline", "pipeline", "main", "", ""},
	{"parent pipeline", "parent_pipeline", "main", "", ""},
	{"external", "external", "main", "", ""},
}

// glAdmits is true when the workflow rules of the file let a run start. A file with no workflow:rules admits every source.
func glAdmits(t *testing.T, doc map[string]any, source, branch, tag string) bool {
	t.Helper()
	wf, _ := doc["workflow"].(map[string]any)
	rules, _ := wf["rules"].([]any)
	if len(rules) == 0 {
		return true
	}
	for _, r := range rules {
		m, _ := r.(map[string]any)
		cond, _ := m["if"].(string)
		if cond == "" {
			return m["when"] != "never"
		}
		x, err := exprgitlab.Parse(cond)
		if err != nil {
			t.Fatalf("workflow rule %q: %v", cond, err)
		}
		v, err := expr.Eval(x, glNative{source: source, branch: branch, tag: tag}, exprgitlab.Semantics)
		if err != nil {
			t.Fatal(err)
		}
		if exprgitlab.Semantics.Truthy(v) {
			return m["when"] != "never"
		}
	}
	return false
}

// glNative reads a GitLab run by the canonical refs the GitLab parser produces.
type glNative struct{ source, branch, tag string }

func (g glNative) Lookup(path []string) (expr.Value, bool) {
	var v string
	switch strings.Join(path, ".") {
	case expr.CtxEvent:
		v = g.source
	case expr.CtxBranch:
		v = g.branch
	case expr.CtxTag:
		v = g.tag
	}
	return v, v != ""
}
func (glNative) Status() expr.Status { return expr.Success }

// TestDroneEventFiltersAgainstEveryGitLabSource: whatever the Drone filter, the GitLab file starts no pipeline for a
// source that Drone has no event for (api, trigger, pipeline, ...), and none for an event the filter leaves out.
func TestDroneEventFiltersAgainstEveryGitLabSource(t *testing.T) {
	for _, f := range droneForms {
		t.Run(f.name, func(t *testing.T) {
			out, _ := convert(t, "drone", provider.GitLab, ".drone.yml", droneSource(f.include, f.exclude))
			var doc map[string]any
			for name, body := range sections(out) {
				if name == ".gitlab-ci.yml" {
					if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, r := range glRuns {
				want := r.drone != "" && droneFilterRuns(f.include, f.exclude, r.drone)
				if got := glAdmits(t, doc, r.source, r.branch, r.tag); got && !want {
					t.Errorf("GitLab starts a pipeline for %s, Drone (%q) does not\n%s", r.name, r.drone, out)
				}
			}
		})
	}
}

// ghaEvents are the events a GHA workflow file can name that no source here fires.
var ghaOnlyEvents = []string{"release", "workflow_dispatch", "pull_request_target", "issue_comment", "workflow_run", "repository_dispatch"}

// TestDroneEventFiltersAgainstGHAEvents: the on: block of the GHA file names only events that the Drone filter can fire.
func TestDroneEventFiltersAgainstGHAEvents(t *testing.T) {
	droneToGHA := map[string]string{"push": "push", "tag": "push", "pull_request": "pull_request", "cron": "schedule", "custom": "workflow_dispatch"}
	for _, f := range droneForms {
		t.Run(f.name, func(t *testing.T) {
			out, _ := convert(t, "drone", provider.GHA, ".drone.yml", droneSource(f.include, f.exclude))
			var wf struct {
				On map[string]any `yaml:"on"`
			}
			for name, body := range sections(out) {
				if strings.HasPrefix(name, ".github/workflows/") {
					if err := yaml.Unmarshal([]byte(body), &wf); err != nil {
						t.Fatal(err)
					}
				}
			}
			var fired []string
			for _, ev := range droneFires {
				if g, ok := droneToGHA[ev]; ok && droneFilterRuns(f.include, f.exclude, ev) {
					fired = append(fired, g)
				}
			}
			for ev := range wf.On {
				if slices.Contains(ghaOnlyEvents, ev) && !slices.Contains(fired, ev) {
					t.Errorf("the GHA file runs on %s, which Drone does not fire here\n%s", ev, out)
				}
				if !slices.Contains(fired, ev) && ev != "workflow_dispatch" {
					t.Errorf("the GHA file runs on %s, outside %v\n%s", ev, fired, out)
				}
			}
		})
	}
}

// TestNoEventFilterOnWoodpeckerStartsOnlySourceEvents: a source with no event filter gives Woodpecker its default events and
// none of the four that only Woodpecker has.
func TestNoEventFilterOnWoodpeckerStartsOnlySourceEvents(t *testing.T) {
	// GitLab creates no merge request pipeline for a job without merge request rules
	// (https://docs.gitlab.com/ci/pipelines/merge_request_pipelines/), so its default is push and tag.
	sources := map[string]struct {
		from, name, text string
		fires            []string
	}{
		"drone":  {"drone", ".drone.yml", droneSource(nil, nil), []string{"push", "pull_request", "tag"}},
		"gitlab": {"gitlab", ".gitlab-ci.yml", "job:\n  image: alpine\n  script: [echo hi]\n", []string{"push", "tag"}},
	}
	for name, s := range sources {
		t.Run(name, func(t *testing.T) {
			out, _ := convert(t, s.from, provider.Woodpecker, s.name, s.text)
			var file wpFile
			for fname, body := range sections(out) {
				if strings.HasPrefix(fname, ".woodpecker/") {
					if err := yaml.Unmarshal([]byte(body), &file); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, ev := range []string{"pull_request_closed", "pull_request_metadata", "release", "deployment"} {
				if wpAdmits(file.When, ev) {
					t.Errorf("Woodpecker event %s runs, the source never fires it\n%s", ev, out)
				}
			}
			for _, ev := range []string{"push", "pull_request", "tag"} {
				if got, want := wpAdmits(file.When, ev), slices.Contains(s.fires, ev); got != want {
					t.Errorf("Woodpecker event %s runs = %v, source fires = %v\n%s", ev, got, want, out)
				}
			}
		})
	}
}
