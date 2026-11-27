package roundtrip_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	defaultDroneSteps = "steps:\n- name: s\n  image: alpine\n  commands: [echo hi]\n"
	defaultDroneHead  = "kind: pipeline\ntype: docker\nname: p\n"
	defaultGitlabJob  = "j:\n  script: [echo hi]\n"
)

// defaultEventCase is a source with no positive event list. lost names the events that have no IR trigger: the
// report must name each one, and no target may invent a schedule for them.
type defaultEventCase struct {
	lost                  map[caps.Feature][]string
	name, from, file, src string
	fires                 []string
}

var defaultEventCases = []defaultEventCase{
	{
		name: "drone no trigger", from: provider.Drone, file: ".drone.yml", src: defaultDroneHead + defaultDroneSteps,
		lost:  map[caps.Feature][]string{caps.TriggerSchedule: {"cron"}, caps.TriggerAPI: {"promote", "rollback"}},
		fires: []string{"push", "pull_request", "tag"},
	},
	{
		name: "drone exclude push and tag", from: provider.Drone, file: ".drone.yml",
		src:   defaultDroneHead + "trigger:\n  event:\n    exclude: [push, tag]\n" + defaultDroneSteps,
		lost:  map[caps.Feature][]string{caps.TriggerSchedule: {"cron"}, caps.TriggerAPI: {"promote", "rollback"}},
		fires: []string{"pull_request"},
	},
	{
		name: "gitlab no workflow", from: provider.GitLab, file: ".gitlab-ci.yml", src: defaultGitlabJob,
		// The GitLab parser reads a file with no workflow block as push and tag. The web source adds the manual
		// trigger, and the sources with no form on the target are reported.
		lost: map[caps.Feature][]string{
			caps.TriggerSchedule: {"schedule"}, caps.TriggerAPI: {"api", "trigger"},
			caps.TriggerPipelineDone: {"pipeline", "parent_pipeline"}, caps.TriggerResource: {"external", "chat"},
		},
		fires: []string{"push", "tag"},
	},
}

// pipelineLost finds the lost entry of a feature on the pipeline itself, not on one of its triggers or jobs.
func pipelineLost(rep *report.Report, f caps.Feature) (report.Entry, bool) {
	for _, e := range rep.Entries {
		if e.Feature == f && e.Level == caps.Lost && strings.Count(e.IRPath, "/") == 1 {
			return e, true
		}
	}
	return report.Entry{}, false
}

func hasTriggerEntry(rep *report.Report, f caps.Feature) bool {
	return slices.ContainsFunc(rep.Entries, func(e report.Entry) bool {
		return e.Feature == f && strings.Contains(e.IRPath, "/trigger/")
	})
}

// TestDefaultEventsOnEventTargets: Tekton, Argo and Concourse turn the default events of a Drone or GitLab source
// into triggers, report every event with no form, and never write a schedule the source does not hold.
func TestDefaultEventsOnEventTargets(t *testing.T) {
	for _, c := range defaultEventCases {
		for _, to := range []string{provider.Tekton, provider.Argo, provider.Concourse} {
			t.Run(c.name+" to "+to, func(t *testing.T) {
				out, rep := convert(t, c.from, to, c.file, c.src)
				for f, names := range c.lost {
					e, ok := pipelineLost(rep, f)
					if !ok {
						t.Fatalf("no lost %s entry on the pipeline; report %+v", f, rep.Entries)
					}
					for _, n := range names {
						if !strings.Contains(e.Detail, n) {
							t.Errorf("%s entry %q does not name %s", f, e.Detail, n)
						}
					}
				}
				if len(c.lost) == 0 && slices.ContainsFunc(rep.Entries, func(e report.Entry) bool {
					return e.Level == caps.Lost && strings.Count(e.IRPath, "/") == 1 && strings.HasPrefix(string(e.Feature), "trigger.")
				}) {
					t.Errorf("an event was reported lost that the source file names no form for; report %+v", rep.Entries)
				}
				if strings.Contains(out, "schedule: ") || strings.Contains(out, "kind: CronJob") || strings.Contains(out, "kind: CronWorkflow") {
					t.Errorf("a schedule was invented:\n%s", out)
				}
				if slices.Contains(c.fires, "pull_request") {
					if to == provider.Concourse {
						if !hasTriggerEntry(rep, caps.TriggerPullRequest) {
							t.Errorf("trigger.pull_request is not reported; report %+v", rep.Entries)
						}
					} else if !strings.Contains(out, "pull_request") {
						t.Errorf("no pull_request trigger written:\n%s", out)
					}
				}
				if !slices.Contains(c.fires, "push") && to != provider.Concourse && strings.Contains(out, "- push") {
					t.Errorf("a push trigger was written, the source does not run on push:\n%s", out)
				}
			})
		}
	}
}

// parseBack emits proj to target and parses the files back with no metadata.
func parseBack(t *testing.T, proj *ir.Project, target string) (map[string][]byte, *ir.Project, *report.Report) {
	t.Helper()
	files, rep, err := prov(t, target).Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := prov(t, target).Parse(toFS(files))
	if err != nil {
		t.Fatal(err)
	}
	return files, back, rep
}

// TestDefaultEventsSurviveParseWithoutMeta: a file read back with no metadata keeps the triggers that the emitter made
// from the default events of the source, so a person who commits only the target directory still gets a pipeline
// that starts, and the file emits again as it was.
func TestDefaultEventsSurviveParseWithoutMeta(t *testing.T) {
	targets := []string{provider.GHA, provider.GitLab, provider.Tekton, provider.Argo, provider.Concourse}
	for _, c := range defaultEventCases[:2] {
		for _, to := range targets {
			if provider.SameFamily(c.from, to) {
				continue
			}
			t.Run(c.name+" via "+to, func(t *testing.T) {
				proj, _, err := prov(t, c.from).Parse(toFS(map[string][]byte{c.file: []byte(c.src)}))
				if err != nil {
					t.Fatal(err)
				}
				_, back, _ := parseBack(t, proj, to)
				for _, pl := range back.Pipelines {
					if len(pl.Triggers) == 0 {
						t.Errorf("pipeline %s parsed back with no trigger", pl.ID)
					}
				}
				again, _, err := prov(t, to).Emit(back)
				if err != nil {
					t.Fatal(err)
				}
				back2, _, err := prov(t, to).Parse(toFS(again))
				if err != nil {
					t.Fatal(err)
				}
				got, want := triggerKinds(back2), triggerKinds(back)
				// Concourse has no event form for a pull request or a tag, and the emit reports both as lost.
				if to == provider.Concourse {
					if len(got) == 0 {
						t.Errorf("the pipeline has no trigger after re-emit; it had %v", want)
					}
				} else if !slices.Equal(got, want) {
					t.Errorf("the triggers change on re-emit: %v, then %v", want, got)
				}
			})
		}
	}
}

// TestDefaultEventsParseBackAsNoTriggerWithMeta: with the metadata that names the Drone or GitLab source, the marker of
// a synthesised trigger set brings the pipeline back with no trigger, so a trip A to B to A keeps the source.
func TestDefaultEventsParseBackAsNoTriggerWithMeta(t *testing.T) {
	targets := []string{provider.GHA, provider.GitLab, provider.Tekton, provider.Argo, provider.Concourse}
	for _, c := range defaultEventCases[:2] {
		for _, to := range targets {
			if provider.SameFamily(c.from, to) {
				continue
			}
			t.Run(c.name+" via "+to, func(t *testing.T) {
				proj, _, err := prov(t, c.from).Parse(toFS(map[string][]byte{c.file: []byte(c.src)}))
				if err != nil {
					t.Fatal(err)
				}
				_, back, rep := parseBack(t, proj, to)
				roundtrip.Restore(back, roundtrip.NewMeta(c.from, proj, prov(t, to), rep))
				for _, pl := range back.Pipelines {
					if len(pl.Triggers) != 0 {
						t.Errorf("pipeline %s restored with triggers %+v", pl.ID, pl.Triggers)
					}
				}
			})
		}
	}
}

// triggerKinds lists the trigger kinds of every pipeline, sorted.
func triggerKinds(p *ir.Project) []string {
	var out []string
	for _, pl := range p.Pipelines {
		for _, tr := range pl.Triggers {
			out = append(out, pl.ID+":"+string(tr.Kind))
		}
	}
	slices.Sort(out)
	return out
}
