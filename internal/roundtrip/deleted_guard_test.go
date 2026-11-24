package roundtrip_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprgha "github.com/lukaszraczylo/pipemaster/expr/gha"
)

// payload is a GHA run seen by the canonical refs of the emitted condition.
type payload map[string]any

func (p payload) Lookup(path []string) (expr.Value, bool) {
	v, ok := p[strings.Join(path, ".")]
	return v, ok
}

func (p payload) Status() expr.Status { return expr.Success }

var ghaIf = regexp.MustCompile(`(?m)^\s+if: (.+)$`)

// ghaCondition reads the first emitted if back and parses it.
func ghaCondition(t *testing.T, out string) expr.Expr {
	t.Helper()
	m := ghaIf.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no if in the output:\n%s", out)
	}
	text := strings.Trim(m[1], "'")
	text = strings.ReplaceAll(text, "''", "'")
	x, err := exprgha.Parse(text)
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	return x
}

func evalGHA(t *testing.T, x expr.Expr, p payload) bool {
	t.Helper()
	v, err := expr.Eval(x, p, exprgha.Semantics)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := v.(bool)
	return b
}

// TestNegatedPushConditionNeverRunsOnADeletionPush: a source has no run for a branch deletion, and a GHA push event
// fires for one, so no translated condition may hold on it, whatever negations the source uses.
func TestNegatedPushConditionNeverRunsOnADeletionPush(t *testing.T) {
	deletion := payload{"event": "push", "ref_type": "branch", "ref_name": "main", "deleted": true, "created": false}
	update := payload{"event": "push", "ref_type": "branch", "ref_name": "main", "deleted": false, "created": false}
	other := payload{"event": "pull_request", "ref_type": "branch", "ref_name": "1/merge"}
	other2 := payload{"event": "push", "ref_type": "branch", "ref_name": "dev", "deleted": false, "created": false}
	glDoc := func(rule string) string {
		return "job:\n  script: [echo hi]\n  rules:\n    - if: '" + rule + "'\n"
	}
	for _, c := range []struct {
		from, name, doc, file string
		pushMain, pushDev     bool
		pr                    bool
	}{
		{"drone", "event exclude push", "kind: pipeline\nname: p\nsteps:\n- name: s\n  image: alpine\n  commands: [echo hi]\n  when:\n    event:\n      exclude: [push]\n", ".drone.yml", false, false, true},
		{"gitlab", "negated conjunction", glDoc(`!($CI_PIPELINE_SOURCE == "push" && $CI_COMMIT_BRANCH == "main")`), ".gitlab-ci.yml", false, true, true},
		{"gitlab", "!= push", glDoc(`$CI_PIPELINE_SOURCE != "push"`), ".gitlab-ci.yml", false, false, true},
		{"gitlab", "push on main", glDoc(`$CI_PIPELINE_SOURCE == "push" && $CI_COMMIT_BRANCH == "main"`), ".gitlab-ci.yml", true, false, false},
		{"gitlab", "push or schedule", glDoc(`$CI_PIPELINE_SOURCE == "push" || $CI_PIPELINE_SOURCE == "schedule"`), ".gitlab-ci.yml", true, true, false},
	} {
		out, _ := convert(t, c.from, "gha", c.file, c.doc)
		if strings.Contains(out, "if: false") {
			t.Fatalf("%s: the condition was disabled:\n%s", c.name, out)
		}
		x := ghaCondition(t, out)
		if evalGHA(t, x, deletion) {
			t.Errorf("%s: runs on a deletion push: %s", c.name, expr.String(x))
		}
		if got := evalGHA(t, x, update); got != c.pushMain {
			t.Errorf("%s: push to main runs=%v, want %v: %s", c.name, got, c.pushMain, expr.String(x))
		}
		if got := evalGHA(t, x, other2); got != c.pushDev {
			t.Errorf("%s: push to dev runs=%v, want %v: %s", c.name, got, c.pushDev, expr.String(x))
		}
		if got := evalGHA(t, x, other); got != c.pr {
			t.Errorf("%s: pull request runs=%v, want %v: %s", c.name, got, c.pr, expr.String(x))
		}
	}
}
