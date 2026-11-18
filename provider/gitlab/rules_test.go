package gitlab_test

import (
	"fmt"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
)

type testEnv map[string]string

func (e testEnv) Lookup(path []string) (expr.Value, bool) {
	v, ok := e[path[0]]
	if !ok || v == "" {
		return nil, false
	}
	return v, true
}

func (testEnv) Status() expr.Status { return expr.Success }

type refRule struct {
	If   string `yaml:"if"`
	When string `yaml:"when"`
}

// firstMatch is GitLab's rules semantics: the first rule whose if holds decides the job.
func firstMatch(t *testing.T, rules []refRule, env testEnv) (run, manual bool) {
	t.Helper()
	for _, r := range rules {
		match := true
		if r.If != "" {
			e, err := exprgitlab.Parse(r.If)
			if err != nil {
				t.Fatal(err)
			}
			v, err := expr.Eval(e, env, exprgitlab.Semantics)
			if err != nil {
				t.Fatal(err)
			}
			match = exprgitlab.Semantics.Truthy(v)
		}
		if !match {
			continue
		}
		switch r.When {
		case "never":
			return false, false
		case "manual":
			return true, true
		}
		return true, false
	}
	return false, false
}

func TestRulesCompileMatchesFirstMatch(t *testing.T) {
	cases := map[string][]refRule{
		"single":            {{If: `$CI_COMMIT_BRANCH == "main"`}},
		"two ifs":           {{If: `$CI_COMMIT_BRANCH == "main"`}, {If: `$CI_PIPELINE_SOURCE == "schedule"`}},
		"never catch-all":   {{If: `$CI_COMMIT_TAG`}, {When: "never"}},
		"never first":       {{If: `$CI_COMMIT_BRANCH == "dev"`, When: "never"}, {If: `$CI_COMMIT_BRANCH`}},
		"unconditional":     {{If: `$CI_COMMIT_TAG`}, {When: "on_success"}},
		"unconditional mid": {{If: `$CI_COMMIT_TAG`}, {When: "on_success"}, {If: `$CI_COMMIT_BRANCH == "main"`}},
		"all manual":        {{If: `$CI_COMMIT_BRANCH == "main"`, When: "manual"}, {If: `$CI_COMMIT_TAG`, When: "manual"}},
		"regex":             {{If: `$CI_COMMIT_BRANCH =~ /^dev/`}, {If: `$CI_COMMIT_REF_NAME != "x" && $CI_PIPELINE_SOURCE == "push"`}},
	}
	var envs []testEnv
	for _, b := range []string{"", "main", "dev"} {
		for _, src := range []string{"push", "schedule", "merge_request_event"} {
			for _, tag := range []string{"", "v1"} {
				envs = append(envs, testEnv{"branch": b, "event": src, "tag": tag, "ref_name": "x"})
			}
		}
	}
	for name, rules := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := yaml.Marshal(map[string]any{"j": map[string]any{"script": []string{"x"}, "rules": rules}})
			if err != nil {
				t.Fatal(err)
			}
			// refRule marshals empty fields; strip them by round-tripping through a generic map.
			var g map[string]any
			if err := yaml.Unmarshal(raw, &g); err != nil {
				t.Fatal(err)
			}
			for _, r := range g["j"].(map[string]any)["rules"].([]any) {
				m := r.(map[string]any)
				for k, v := range m {
					if v == "" {
						delete(m, k)
					}
				}
			}
			raw, _ = yaml.Marshal(g)
			pl, rep := pipeline(t, string(raw))
			j := job(t, pl, "j")
			if expr.HasOpaque(j.If) {
				t.Fatalf("rules must compile, got opaque %v; report %+v", j.If, rep.Entries)
			}
			for _, env := range envs {
				wantRun, wantManual := firstMatch(t, rules, env)
				got := true
				if j.If != nil {
					v, err := expr.Eval(j.If, env, exprgitlab.Semantics)
					if err != nil {
						t.Fatal(err)
					}
					got = exprgitlab.Semantics.Truthy(v)
				}
				if got != wantRun {
					t.Fatalf("env %v: If runs=%v, first-match runs=%v (If = %s)", env, got, wantRun, expr.String(j.If))
				}
				if wantRun && j.Manual != wantManual {
					t.Fatalf("env %v: Manual=%v, want %v", env, j.Manual, wantManual)
				}
			}
		})
	}
}

func TestRulesEmptyListNeverRuns(t *testing.T) {
	pl, _ := pipeline(t, "j:\n  script: [x]\n  rules: []\n")
	if l, ok := job(t, pl, "j").If.(expr.Lit); !ok || l.Bool {
		t.Errorf("If = %v, want false", job(t, pl, "j").If)
	}
}

func TestRulesShape(t *testing.T) {
	pl, _ := pipeline(t, "j:\n  script: [x]\n  rules:\n    - if: $CI_COMMIT_BRANCH == \"main\"\n")
	if got := expr.String(job(t, pl, "j").If); got != "branch == 'main'" {
		t.Errorf("If = %s", got)
	}
}

func TestRulesFallbackOpaque(t *testing.T) {
	for name, rules := range map[string]string{
		"changes":     "- changes: [Dockerfile]",
		"exists":      "- exists: [Makefile]",
		"variables":   "- if: $X\n    variables: {A: b}",
		"mixed whens": "- if: $A\n    when: manual\n  - if: $B",
		"always":      "- if: $A\n    when: always",
		"allow":       "- if: $A\n    allow_failure: true",
	} {
		t.Run(name, func(t *testing.T) {
			pl, rep := pipeline(t, "j:\n  script: [x]\n  rules:\n  "+rules+"\n")
			j := job(t, pl, "j")
			o, ok := j.If.(expr.Opaque)
			if !ok || o.Dialect != "gitlab" {
				t.Fatalf("If = %#v, want gitlab Opaque", j.If)
			}
			if ext(j.Ext)["rules"] == nil {
				t.Errorf("original rules must stay in Ext: %v", j.Ext)
			}
			if !hasEntry(rep, "rules use features") {
				t.Errorf("no report entry: %+v", rep.Entries)
			}
		})
	}
}

func TestManualDefaults(t *testing.T) {
	pl, _ := pipeline(t, `
kw:
  script: [x]
  when: manual
kw-blocking:
  script: [x]
  when: manual
  allow_failure: false
via-rules:
  script: [x]
  rules:
    - if: $CI_COMMIT_BRANCH
      when: manual
`)
	if j := job(t, pl, "kw"); !j.Manual || !j.AllowFailure {
		t.Errorf("when: manual allows failure by default: %+v", j)
	}
	if j := job(t, pl, "kw-blocking"); !j.Manual || j.AllowFailure {
		t.Errorf("explicit allow_failure false: %+v", j)
	}
	if j := job(t, pl, "via-rules"); !j.Manual || j.AllowFailure {
		t.Errorf("rules:when manual blocks by default: %+v", j)
	}
}

func TestWhenForms(t *testing.T) {
	pl, rep := pipeline(t, `
stages: [build, test]
always-job:
  stage: test
  script: [x]
  when: always
fail-job:
  stage: test
  script: [x]
  when: on_failure
never-job:
  script: [x]
  when: never
delay-job:
  script: [x]
  when: delayed
  start_in: 5 minutes
`)
	if len(pl.Finally) != 2 {
		t.Fatalf("finally = %d, want 2", len(pl.Finally))
	}
	if j := job(t, pl, "always-job"); j.If != nil {
		t.Errorf("always If = %v", j.If)
	}
	if got := expr.String(job(t, pl, "fail-job").If); got != "failure()" {
		t.Errorf("on_failure If = %s", got)
	}
	if l, ok := job(t, pl, "never-job").If.(expr.Lit); !ok || l.Bool {
		t.Errorf("never If = %v", job(t, pl, "never-job").If)
	}
	d := job(t, pl, "delay-job")
	if ext(d.Ext)["when"] != "delayed" || ext(d.Ext)["start_in"] != "5 minutes" {
		t.Errorf("delayed must stay in Ext: %v", d.Ext)
	}
	if !hasEntry(rep, "when: delayed") {
		t.Errorf("report = %+v", rep.Entries)
	}
}

func TestOnlyExceptToExt(t *testing.T) {
	pl, rep := pipeline(t, "j:\n  script: [x]\n  only: [main]\n  except:\n    - tags\n")
	x := ext(job(t, pl, "j").Ext)
	if x["only"] == nil || x["except"] == nil {
		t.Errorf("Ext = %v", x)
	}
	if !hasEntry(rep, "only is kept") {
		t.Errorf("report = %+v", rep.Entries)
	}
}

var _ = fmt.Sprint
