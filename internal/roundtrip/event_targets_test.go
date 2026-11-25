package roundtrip_test

import (
	"os/exec"
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

// TestDroneEventFiltersAgainstTektonAndArgoTriggers: the Tekton trigger and the Argo sensor name no push, tag or pull
// request test for an event the Drone filter leaves out. A Drone event with no webhook form (cron, custom, promote,
// rollback) starts nothing there.
func TestDroneEventFiltersAgainstTektonAndArgoTriggers(t *testing.T) {
	// A job condition that excludes an event also names it, so each signature is the trigger side only.
	sigs := map[string]map[string]string{
		provider.Tekton: {"push": "refs/heads/", "tag": "refs/tags/", "pull_request": "body.pull_request"},
		provider.Argo:   {"push": "refs/heads/", "tag": "refs/tags/", "pull_request": "eventName: pull_request"},
	}
	for _, target := range []string{provider.Tekton, provider.Argo} {
		tests := sigs[target]
		for _, f := range droneForms {
			t.Run(target+"/"+f.name, func(t *testing.T) {
				out, _ := convert(t, "drone", target, ".drone.yml", droneSource(f.include, f.exclude))
				var live []string
				for _, line := range strings.Split(out, "\n") {
					if !strings.HasPrefix(strings.TrimSpace(line), "#") {
						live = append(live, line)
					}
				}
				body := strings.Join(live, "\n")
				for ev, sig := range tests {
					if !droneFilterRuns(f.include, f.exclude, ev) && strings.Contains(body, sig) {
						t.Errorf("%s names %q although Drone does not fire %s\n%s", target, sig, ev, out)
					}
				}
			})
		}
	}
}

// TestNoEventFilterOnWoodpeckerStartsOnlySourceEvents: a source with no event filter gives Woodpecker the events it
// fires, with a Woodpecker form where there is one, and none that only Woodpecker has.
func TestNoEventFilterOnWoodpeckerStartsOnlySourceEvents(t *testing.T) {
	// GitLab creates no merge request pipeline for a job without merge request rules
	// (https://docs.gitlab.com/ci/pipelines/merge_request_pipelines/), so its default is push and tag.
	sources := map[string]struct {
		from, name, text string
		fires            []string
	}{
		"drone":  {"drone", ".drone.yml", droneSource(nil, nil), []string{"push", "pull_request", "tag", "cron", "manual", "deployment"}},
		"gitlab": {"gitlab", ".gitlab-ci.yml", "job:\n  image: alpine\n  script: [echo hi]\n", []string{"push", "tag", "cron", "manual"}},
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
			for _, ev := range []string{"pull_request_closed", "pull_request_metadata", "release"} {
				if wpAdmits(file.When, ev) {
					t.Errorf("Woodpecker event %s runs, the source never fires it\n%s", ev, out)
				}
			}
			for _, ev := range []string{"push", "pull_request", "tag", "cron", "manual", "deployment"} {
				if got, want := wpAdmits(file.When, ev), slices.Contains(s.fires, ev); got != want {
					t.Errorf("Woodpecker event %s runs = %v, source fires = %v\n%s", ev, got, want, out)
				}
			}
		})
	}
}

// stepWhenOf is the when of the first step of the Woodpecker file in an emission.
func stepWhenOf(t *testing.T, out string) wpWhen {
	t.Helper()
	var file wpFile
	for name, body := range sections(out) {
		if strings.HasPrefix(name, ".woodpecker/") {
			if err := yaml.Unmarshal([]byte(body), &file); err != nil {
				t.Fatal(err)
			}
		}
	}
	return file.Steps[0].When
}

// TestTagStepOfANoTriggerSourceRunsOnATagPushOnly: a Drone step on the tag event does not run on a manual, cron or
// deployment run, even when such a run sits on a tag ref and the workflow starts on those events.
func TestTagStepOfANoTriggerSourceRunsOnATagPushOnly(t *testing.T) {
	src := "kind: pipeline\ntype: docker\nname: p\nsteps:\n- name: s\n  image: alpine\n  commands: [echo hi]\n  when:\n    event: [tag]\n"
	out, _ := convert(t, "drone", provider.Woodpecker, ".drone.yml", src)
	when := stepWhenOf(t, out)
	for ev, want := range map[string]bool{"tag": true, "push": false, "manual": false, "cron": false, "deployment": false, "pull_request": false} {
		if got := wpAdmits(when, ev); got != want {
			t.Errorf("Woodpecker event %s: step runs = %v, want %v\n%s", ev, got, want, out)
		}
	}
}

// TestPushStepOfANoTriggerSourceStartsOnPush: a Drone step on the push event gives the workflow a push start on GHA and
// on GitLab, and the file never starts nothing.
func TestPushStepOfANoTriggerSourceStartsOnPush(t *testing.T) {
	src := "kind: pipeline\ntype: docker\nname: p\nsteps:\n- name: s\n  image: alpine\n  commands: [echo hi]\n  when:\n    event: [push]\n"
	out, _ := convert(t, "drone", provider.GHA, ".drone.yml", src)
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
	if _, ok := wf.On["push"]; !ok {
		t.Errorf("the GHA file has no push start: %v\n%s", wf.On, out)
	}
	if _, ok := wf.On["pull_request"]; ok {
		t.Errorf("the GHA file starts on pull_request, which no step runs on\n%s", out)
	}
	gl, _ := convert(t, "drone", provider.GitLab, ".drone.yml", src)
	if strings.Contains(gl, "when: never") {
		t.Errorf("the GitLab file never starts\n%s", gl)
	}
}

const dronePushStep = "kind: pipeline\ntype: docker\nname: p\nsteps:\n- name: s\n  image: alpine\n  commands: [echo ran]\n  when:\n    event: [push]\n"

// TestPushStepRunsOnABranchPushOnWoodpecker: a Drone step on the push event is a branch push, so Woodpecker keeps its
// push event and the step does not run on a tag, pull request or manual run.
func TestPushStepRunsOnABranchPushOnWoodpecker(t *testing.T) {
	out, _ := convert(t, "drone", provider.Woodpecker, ".drone.yml", dronePushStep)
	if strings.Contains(out, "[lost]") && strings.Contains(out, "step.if") {
		t.Errorf("the push step is lost\n%s", out)
	}
	when := stepWhenOf(t, out)
	for ev, want := range map[string]bool{"push": true, "tag": false, "pull_request": false, "manual": false} {
		if got := wpAdmits(when, ev); got != want {
			t.Errorf("Woodpecker event %s: step runs = %v, want %v\n%s", ev, got, want, out)
		}
	}
}

// TestPushStepRunsOnABranchPushOnGitLab: the converted step guard holds on a branch push only.
func TestPushStepRunsOnABranchPushOnGitLab(t *testing.T) {
	out, _ := convert(t, "drone", provider.GitLab, ".drone.yml", dronePushStep)
	_, body, ok := strings.Cut(out, "- |\n")
	if !ok {
		t.Fatalf("no guarded step\n%s", out)
	}
	var script []string
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(l, "      ") {
			break
		}
		script = append(script, strings.TrimPrefix(l, "      "))
	}
	runs := []struct {
		name string
		env  []string
		want bool
	}{
		{"branch push", []string{"CI_PIPELINE_SOURCE=push", "CI_COMMIT_BRANCH=main"}, true},
		{"tag push", []string{"CI_PIPELINE_SOURCE=push", "CI_COMMIT_TAG=v1"}, false},
		{"merge request", []string{"CI_PIPELINE_SOURCE=merge_request_event", "CI_MERGE_REQUEST_TARGET_BRANCH_NAME=main"}, false},
		{"web", []string{"CI_PIPELINE_SOURCE=web", "CI_COMMIT_BRANCH=main"}, false},
	}
	for _, r := range runs {
		cmd := exec.Command("sh", "-c", strings.Join(script, "\n")) //nolint:gosec // runs the guard the converter emitted, in a fixed environment
		cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, r.env...)
		got, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v\n%s", r.name, err, out)
		}
		if ran := strings.Contains(string(got), "ran"); ran != r.want {
			t.Errorf("GitLab %s: step runs = %v, want %v\n%s", r.name, ran, r.want, out)
		}
	}
}
