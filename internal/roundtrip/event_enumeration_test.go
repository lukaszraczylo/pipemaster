package roundtrip_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	wpexpr "github.com/lukaszraczylo/pipemaster/expr/woodpecker"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// woodpeckerEvents is every event of the pinned Woodpecker schema (https://woodpecker-ci.org/docs/usage/workflow-syntax),
// mapped to the Drone event of the same meaning. An empty name marks an event Drone has no counterpart for.
var woodpeckerEvents = map[string]string{
	"push": "push", "pull_request": "pull_request", "tag": "tag", "cron": "cron", "manual": "custom",
	"pull_request_closed": "", "pull_request_metadata": "", "release": "", "deployment": "",
}

// wpAdmits is true when some entry of the emitted when list lets a run of the event start: its event list names
// the event and its evaluate text holds for it.
func wpAdmits(w wpWhen, event string) bool {
	if len(w) == 0 {
		return true
	}
	for _, e := range w {
		if ev := anyList(e.Event); len(ev) > 0 && !slices.Contains(ev, event) {
			continue
		}
		if e.Evaluate != "" {
			x, err := wpexpr.Parse(e.Evaluate)
			if err != nil {
				panic(err)
			}
			v, err := expr.Eval(x, wpexpr.UnsetEnv{Env: glRun{"event": event, "branch": "main"}}, wpexpr.Semantics)
			if err != nil {
				panic(err)
			}
			if b, _ := v.(bool); !b {
				continue
			}
		}
		return true
	}
	return false
}

// TestDroneEventFiltersAgainstEveryWoodpeckerEvent runs the emitted Woodpecker condition against each of the nine
// Woodpecker events. Drone reports five of them. The other four are target-only and must never start a run, whatever
// the filter: Drone would not have started one.
func TestDroneEventFiltersAgainstEveryWoodpeckerEvent(t *testing.T) {
	forms := []struct {
		name             string
		include, exclude []string
	}{
		{"exclude pull request", nil, []string{"pull_request"}},
		{"exclude push", nil, []string{"push"}},
		{"exclude tag", nil, []string{"tag"}},
		{"exclude cron", nil, []string{"cron"}},
		{"exclude custom", nil, []string{"custom"}},
		{"exclude two", nil, []string{"pull_request", "cron"}},
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
				out, _ := convert(t, "drone", provider.Woodpecker, ".drone.yml", src)
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
				for wp, drone := range woodpeckerEvents {
					want := drone != "" && droneFilterRuns(f.include, f.exclude, drone)
					if got := wpAdmits(when, wp); got && !want {
						t.Errorf("Woodpecker event %s runs, Drone (%q) skips\n%s", wp, drone, out)
					}
				}
			})
		}
	}
}

// TestWoodpeckerEntriesCanHold converts every corpus entry to Woodpecker and checks each emitted when entry: when
// the entry lists events and holds an evaluate test, some listed event must be able to satisfy that test. A
// condition no listed event satisfies disables the workflow without a word.
func TestWoodpeckerEntriesCanHold(t *testing.T) {
	to := prov(t, provider.Woodpecker)
	for _, f := range corpusEntries(t) {
		if f.provider == provider.Woodpecker {
			continue
		}
		t.Run(f.name, func(t *testing.T) {
			src, _, err := prov(t, f.provider).Parse(os.DirFS(f.dir))
			if err != nil {
				t.Fatal(err)
			}
			files, _, err := to.Emit(src)
			if err != nil {
				t.Fatal(err)
			}
			for name, body := range files {
				if !strings.HasPrefix(name, ".woodpecker/") {
					continue
				}
				var file wpFile
				if err := yaml.Unmarshal(body, &file); err != nil {
					t.Fatal(err)
				}
				whens := []wpWhen{file.When}
				for _, s := range file.Steps {
					whens = append(whens, s.When)
				}
				for _, w := range whens {
					for _, e := range w {
						events := anyList(e.Event)
						if len(events) == 0 || e.Evaluate == "" || e.Evaluate == "false" {
							continue
						}
						x, err := wpexpr.Parse(e.Evaluate)
						if err != nil {
							t.Fatalf("%s: %v", name, err)
						}
						if len(expr.EventsPossible(x, events)) == 0 {
							t.Errorf("%s: no event of %v can satisfy %s", name, events, e.Evaluate)
						}
					}
				}
			}
		})
	}
}

// TestGitLabWebRunsOnWoodpeckerManual: a rule on the web pipeline source needs the manual event in the trigger set;
// the old output paired event push with a manual-only test, which no run satisfies.
func TestGitLabWebRunsOnWoodpeckerManual(t *testing.T) {
	src := "job:\n  image: alpine\n  script: [echo hi]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"web\"'\n"
	out, _ := convert(t, "gitlab", provider.Woodpecker, ".gitlab-ci.yml", src)
	var file wpFile
	for name, body := range sections(out) {
		if strings.HasPrefix(name, ".woodpecker/") {
			if err := yaml.Unmarshal([]byte(body), &file); err != nil {
				t.Fatal(err)
			}
		}
	}
	for wp := range woodpeckerEvents {
		if got, want := wpAdmits(file.When, wp), wp == "manual"; got != want {
			t.Errorf("event %s: runs = %v, GitLab web means manual only\n%s", wp, got, out)
		}
	}
}
