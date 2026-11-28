package roundtrip_test

import (
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// droneEventOf names the DRONE_BUILD_EVENT of a scenario.
func droneEventOf(s scenario) string {
	switch {
	case s.pr:
		return "pull_request"
	case s.sched:
		return "cron"
	case s.tag:
		return "tag"
	}
	return "push"
}

// droneFilterRuns is the reference of a Drone event filter: included when listed (or no list), and not excluded.
func droneFilterRuns(include, exclude []string, event string) bool {
	return (len(include) == 0 || slices.Contains(include, event)) && !slices.Contains(exclude, event)
}

func quoteList(vs []string) string { return "[" + strings.Join(vs, ", ") + "]" }

type wpFile struct {
	When  wpWhen `yaml:"when"`
	Steps []struct {
		When wpWhen `yaml:"when"`
	} `yaml:"steps"`
}

// wpRunsOn is true when the emitted list lets the scenario run: an entry matches its event, branch and evaluate text.
func wpRunsOn(w wpWhen, s scenario) bool { return len(w) == 0 || wpRuns(w, s, true) }

// TestDroneEventFiltersRunOnWoodpeckerNeverWhereDroneSkips converts event filters of a Drone pipeline and of a Drone
// step, and runs the emitted Woodpecker conditions against every event: a run Drone skips must stay skipped.
func TestDroneEventFiltersRunOnWoodpeckerNeverWhereDroneSkips(t *testing.T) {
	forms := []struct {
		name             string
		include, exclude []string
	}{
		{"exclude pull request", nil, []string{"pull_request"}},
		{"exclude cron", nil, []string{"cron"}},
		{"exclude two", nil, []string{"pull_request", "cron"}},
		{"exclude push and tag", nil, []string{"push", "tag"}},
		{"include pull request", []string{"pull_request"}, nil},
		{"include and exclude", []string{"pull_request", "cron"}, []string{"cron"}},
	}
	for _, level := range []string{"pipeline", "step"} {
		for _, f := range forms {
			t.Run(level+"/"+f.name, func(t *testing.T) {
				filter := "event:\n"
				if len(f.include) > 0 {
					filter += "  include: " + quoteList(f.include) + "\n"
				}
				if len(f.exclude) > 0 {
					filter += "  exclude: " + quoteList(f.exclude) + "\n"
				}
				indent := func(by string) string {
					return by + strings.ReplaceAll(strings.TrimRight(filter, "\n"), "\n", "\n"+by) + "\n"
				}
				src := "kind: pipeline\ntype: docker\nname: p\n"
				if level == "pipeline" {
					src += "trigger:\n" + indent("  ")
				}
				src += "steps:\n- name: s\n  image: alpine\n  commands: [echo hi]\n"
				if level == "step" {
					src += "  when:\n" + indent("    ")
				}
				out, rep := convert(t, "drone", provider.Woodpecker, ".drone.yml", src)
				for _, e := range rep.Entries {
					if e.Level == caps.Lost && (e.Feature == caps.JobIf || e.Feature == caps.StepIf) {
						t.Fatalf("the condition is lost: %s\n%s", e.Detail, out)
					}
				}
				var file wpFile
				for name, body := range sections(out) {
					if strings.HasPrefix(name, ".woodpecker/") {
						if err := yaml.Unmarshal([]byte(body), &file); err != nil {
							t.Fatal(err)
						}
					}
				}
				when := file.When
				if level == "step" {
					when = file.Steps[0].When
				}
				ran := 0
				for _, s := range scenarios {
					if s.deleted {
						continue
					}
					want := droneFilterRuns(f.include, f.exclude, droneEventOf(s))
					got := wpRunsOn(when, s)
					if got && !want {
						t.Errorf("%s: Woodpecker runs where Drone skips\n%s", s.name, out)
					}
					if got {
						ran++
					}
				}
				if ran == 0 {
					t.Error("the condition never runs, so the comparison proves nothing")
				}
			})
		}
	}
}
