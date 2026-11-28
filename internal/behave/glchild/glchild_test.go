package glchild_test

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/behave/glchild"
)

const parent = `variables:
  GLOBAL: g
  SHARED: global
stages: [one, two]
setup:
  stage: one
  script: [echo setup]
call:
  stage: two
  variables:
    SHARED: job
    TARGET: staging
  trigger:
    include:
      - local: ci/a.yml
      - ci/b.yml
    strategy: depend
`

func only(t *testing.T, doc string, pipelineVars map[string]string) glchild.Trigger {
	t.Helper()
	ts, err := glchild.Triggers([]byte(doc), pipelineVars)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 {
		t.Fatalf("triggers = %+v, want one", ts)
	}
	return ts[0]
}

func TestTriggerPlan(t *testing.T) {
	tr := only(t, parent, nil)
	if tr.Job != "call" || !tr.Depend || tr.Reason != "" {
		t.Fatalf("trigger = %+v", tr)
	}
	if !slices.Equal(tr.Files, []string{"ci/a.yml", "ci/b.yml"}) {
		t.Errorf("files = %v, want both includes in order", tr.Files)
	}
	want := map[string]string{"GLOBAL": "g", "SHARED": "job", "TARGET": "staging"}
	for k, v := range want {
		if tr.Vars[k] != v {
			t.Errorf("%s = %q, want %q: default variables and trigger variables are forwarded, the job wins", k, tr.Vars[k], v)
		}
	}
}

func TestForwardAndInherit(t *testing.T) {
	for _, tc := range []struct {
		name, trigger, job string
		pipeline           map[string]string
		want               map[string]string
		absent             []string
	}{
		{name: "yaml off", trigger: "    forward:\n      yaml_variables: false\n", absent: []string{"GLOBAL", "TARGET"}},
		{name: "pipeline variables are not forwarded by default", pipeline: map[string]string{"MANUAL": "1"}, absent: []string{"MANUAL"}},
		{name: "pipeline variables forwarded", trigger: "    forward:\n      pipeline_variables: true\n", pipeline: map[string]string{"MANUAL": "1"},
			want: map[string]string{"MANUAL": "1", "GLOBAL": "g"}},
		{name: "inherit false blocks defaults", job: "  inherit:\n    variables: false\n", want: map[string]string{"TARGET": "staging"}, absent: []string{"GLOBAL"}},
		{name: "inherit list", job: "  inherit:\n    variables: [GLOBAL]\n", want: map[string]string{"GLOBAL": "g"}, absent: []string{"SHARED"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := "variables:\n  GLOBAL: g\n  SHARED: s\ncall:\n  variables:\n    TARGET: staging\n" + tc.job + "  trigger:\n    include: ci/a.yml\n" + tc.trigger
			tr := only(t, doc, tc.pipeline)
			for k, v := range tc.want {
				if tr.Vars[k] != v {
					t.Errorf("%s = %q, want %q", k, tr.Vars[k], v)
				}
			}
			for _, k := range tc.absent {
				if _, ok := tr.Vars[k]; ok {
					t.Errorf("%s reaches the child, want it hidden", k)
				}
			}
		})
	}
}

func TestUnsupportedTriggers(t *testing.T) {
	for _, tc := range []struct{ name, trigger, reason string }{
		{"project", "    project: g/p", glchild.ReasonProject},
		{"project string", "  trigger: g/p", glchild.ReasonProject},
		{"artifact", "    include:\n      - artifact: gen.yml\n        job: gen", glchild.ReasonArtifact},
		{"remote", "    include:\n      - remote: https://example.com/a.yml", glchild.ReasonRemote},
		{"template", "    include:\n      - template: T.gitlab-ci.yml", glchild.ReasonTemplate},
		{"glob", "    include: ci/*.yml", glchild.ReasonGlob},
		{"four files", "    include: [a.yml, b.yml, c.yml, d.yml]", glchild.ReasonFiles},
		{"cross project include", "    include:\n      - project: g/p\n        file: a.yml", glchild.ReasonProject},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trig := "  trigger:\n" + tc.trigger + "\n"
			if strings.HasPrefix(tc.trigger, "  trigger:") {
				trig = tc.trigger + "\n"
			}
			tr := only(t, "call:\n"+trig, nil)
			if tr.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", tr.Reason, tc.reason)
			}
		})
	}
}

func TestMergeJoinsFilesByKey(t *testing.T) {
	out, err := glchild.Merge([][]byte{
		[]byte("stages: [a]\nvariables:\n  X: 1\njob1:\n  script: [one]\n"),
		[]byte("variables:\n  Y: 2\njob2:\n  script: [two]\njob1:\n  stage: a\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"job1:", "job2:", "X: 1", "Y: 2", "script:", "stage: a"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("merged file lacks %q:\n%s", want, out)
		}
	}
}

func TestOutcomeFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    glchild.Outcome
		want bool
	}{
		{"created, passed, depend", glchild.Outcome{Created: true, Depend: true}, false},
		{"created, failed, depend", glchild.Outcome{Created: true, Depend: true, Exit: 1}, true},
		{"created, failed, no depend", glchild.Outcome{Created: true, Exit: 1}, false},
		{"not created", glchild.Outcome{Depend: true}, true},
		{"not created, no depend", glchild.Outcome{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.o.Fails(); got != tc.want {
				t.Errorf("Fails = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRewriteSplicesTheChildIntoTheTriggerJob(t *testing.T) {
	out, err := glchild.Rewrite([]byte(parent), map[string]glchild.Outcome{
		"call": {Created: true, Depend: true, Markers: []string{"first", "it's $x"}, Exit: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "trigger:") {
		t.Errorf("the trigger key must go:\n%s", s)
	}
	for _, want := range []string{"MARK first", `MARK it'\''s $x`, "exit 1", "stage: two"} {
		if !strings.Contains(s, want) {
			t.Errorf("rewritten job lacks %q:\n%s", want, s)
		}
	}
	if !strings.Contains(s, "[echo setup]") {
		t.Errorf("a normal job must stay as it is:\n%s", s)
	}
}

func TestRewritePassesWithoutDependWhenTheChildFails(t *testing.T) {
	out, err := glchild.Rewrite([]byte(strings.Replace(parent, "strategy: depend", "", 1)), map[string]glchild.Outcome{
		"call": {Created: true, Markers: []string{"m"}, Exit: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "exit 1") {
		t.Errorf("without depend the trigger job passes once the child is created:\n%s", out)
	}
}

func TestRewriteFailsAChildThatIsNotCreated(t *testing.T) {
	out, _ := glchild.Rewrite([]byte(parent), map[string]glchild.Outcome{"call": {Depend: true}})
	if !strings.Contains(string(out), "exit 1") {
		t.Errorf("a child that GitLab does not create fails the trigger job:\n%s", out)
	}
}

const twoChildren = `stages: [one, two]
second:
  stage: two
  needs: [first]
  rules:
    - when: manual
  allow_failure: true
  trigger:
    include: ci/b.yml
first:
  stage: one
  trigger:
    include: ci/a.yml
plain:
  script: [echo hi]
.hidden:
  trigger:
    include: ci/c.yml
`

func TestTriggersInFileOrderSkippingPlainAndHiddenJobs(t *testing.T) {
	ts, err := glchild.Triggers([]byte(twoChildren), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tr := range ts {
		got = append(got, tr.Job+"="+strings.Join(tr.Files, ","))
	}
	if want := []string{"second=ci/b.yml", "first=ci/a.yml"}; !slices.Equal(got, want) {
		t.Fatalf("triggers = %v, want %v", got, want)
	}
}

func TestRewriteKeepsWhatDecidesWhenTheChildStarts(t *testing.T) {
	out, err := glchild.Rewrite([]byte(twoChildren), map[string]glchild.Outcome{"second": {Created: true, Markers: []string{"m"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stage: two", "needs:", "when: manual", "allow_failure: true", "MARK m"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the trigger job must keep %q so the engine decides when its child starts:\n%s", want, out)
		}
	}
	if !strings.Contains(string(out), "ci/a.yml") {
		t.Errorf("a trigger job without an outcome stays as it is:\n%s", out)
	}
}

func TestRunning(t *testing.T) {
	out := "WARN Avoid overriding [CI_PIPELINE_SOURCE]\nparsing finished\n[\n" +
		`{"name":"a","stage":"one","when":"on_success"},{"name":"b","when":"manual"},{"name":"c","when":"never"},{"name":"d","when":"always"}` + "\n]\n"
	got, err := glchild.Running(out)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"a": true, "b": false, "c": false, "d": true}
	if !maps.Equal(got, want) {
		t.Errorf("Running = %v, want %v", got, want)
	}
	if _, err := glchild.Running("no list here"); err == nil {
		t.Error("output without a job list must fail loudly")
	}
}

func TestMergeKeepsJobOrderAndTags(t *testing.T) {
	out, err := glchild.Merge([][]byte{
		[]byte("z_job:\n  script: [z]\na_job:\n  script: [a]\n"),
		[]byte("m_job:\n  script:\n    - !reference [.tpl, script]\na_job:\n  stage: s\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Index(s, "z_job:") >= strings.Index(s, "a_job:") || strings.Index(s, "a_job:") >= strings.Index(s, "m_job:") {
		t.Errorf("job order changed:\n%s", s)
	}
	if !strings.Contains(s, "!reference") || !strings.Contains(s, "stage: s") {
		t.Errorf("tag or merged key lost:\n%s", s)
	}
}

func TestMergeKeepsASingleFileUnchanged(t *testing.T) {
	in := "# comment\nb:\n  script: [x]\na: &anc\n  script: [y]\nc:\n  <<: *anc\n"
	out, err := glchild.Merge([][]byte{[]byte(in)})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Errorf("a single file changed:\n%s", out)
	}
}

func TestInheritedTriggerIsFlagged(t *testing.T) {
	for name, doc := range map[string]string{
		"extends":      ".tpl:\n  trigger:\n    include: ci/c.yml\ncall:\n  extends: .tpl\n",
		"extends list": ".tpl:\n  trigger:\n    include: ci/c.yml\ncall:\n  extends: [.other, .tpl]\n.other:\n  script: [x]\n",
		"chain":        ".base:\n  trigger:\n    include: ci/c.yml\n.mid:\n  extends: .base\ncall:\n  extends: .mid\n",
		"merge key":    ".tpl: &tpl\n  trigger:\n    include: ci/c.yml\ncall:\n  <<: *tpl\n  stage: x\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := glchild.Triggers([]byte(doc), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Job != "call" || got[0].Reason != glchild.ReasonInherit {
				t.Errorf("triggers = %+v, want one blocked inherited trigger for call", got)
			}
		})
	}
	got, _ := glchild.Triggers([]byte("a:\n  extends: .t\n.t:\n  script: [x]\n"), nil)
	if len(got) != 0 {
		t.Errorf("a job that inherits no trigger is flagged: %+v", got)
	}
}
