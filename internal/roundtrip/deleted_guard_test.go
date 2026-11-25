package roundtrip_test

import (
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprgha "github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/ir"
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
	return parseIf(t, m[1])
}

// parseIf reads one if: scalar as YAML does, drops a ${{ }} wrapper and parses the expression.
func parseIf(t *testing.T, scalar string) expr.Expr {
	t.Helper()
	var text string
	if err := yaml.Unmarshal([]byte(scalar), &text); err != nil {
		t.Fatalf("yaml %q: %v", scalar, err)
	}
	text = strings.TrimSpace(text)
	if in, ok := strings.CutPrefix(text, "${{"); ok {
		text = strings.TrimSpace(strings.TrimSuffix(in, "}}"))
	}
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

// ghaJobIf matches a job-level if; a step inside a guarded job needs none.
var ghaJobIf = regexp.MustCompile(`(?m)^    if: (.+)$`)

// ghaConditions reads every emitted if back and parses it.
func ghaConditions(t *testing.T, out string) []expr.Expr {
	t.Helper()
	var xs []expr.Expr
	for _, m := range ghaJobIf.FindAllStringSubmatch(out, -1) {
		xs = append(xs, parseIf(t, m[1]))
	}
	return xs
}

// TestNoJobRunsOnADeletionPushWhateverTheSourceCondition: GHA starts a push workflow when a branch is deleted and no
// other provider starts anything, so each job carries the guard even when its source never names push.
func TestNoJobRunsOnADeletionPushWhateverTheSourceCondition(t *testing.T) {
	deletion := payload{"event": "push", "ref_type": "branch", "ref_name": "main", "default_branch": "main", "deleted": true, "created": false}
	update := payload{"event": "push", "ref_type": "branch", "ref_name": "main", "default_branch": "main", "deleted": false, "created": false}
	glDoc := func(rule string) string {
		return "job:\n  script: [echo hi]\n  rules:\n    - if: '" + rule + "'\n"
	}
	const drone = "kind: pipeline\nname: p\ntrigger:\n  event: [push, pull_request]\nsteps:\n- name: s\n  image: alpine\n  commands: [echo hi]\n  when:\n    event:\n      exclude: [pull_request]\n"
	const concourse = "resources:\n- {name: repo, type: git, source: {uri: https://example.com/r.git}}\njobs:\n- name: test\n  plan:\n  - {get: repo, trigger: true}\n  - task: t\n    config: {platform: linux, image_resource: {type: registry-image, source: {repository: alpine}}, run: {path: echo, args: [hi]}}\n"
	for _, c := range []struct {
		from, name, doc, file string
		pushMain              bool
	}{
		{"gitlab", "branch equals main", glDoc(`$CI_COMMIT_BRANCH == "main"`), ".gitlab-ci.yml", true},
		{"gitlab", "not a merge request", glDoc(`$CI_PIPELINE_SOURCE != "merge_request_event"`), ".gitlab-ci.yml", true},
		{"gitlab", "no rules", "job:\n  script: [echo hi]\n", ".gitlab-ci.yml", true},
		{"drone", "event exclude pull_request", drone, ".drone.yml", true},
		{"concourse", "per-job trigger", concourse, ".concourse/pipeline.yml", true},
	} {
		out, _ := convert(t, c.from, "gha", c.file, c.doc)
		xs := ghaConditions(t, out)
		if len(xs) == 0 {
			t.Fatalf("%s: no job condition in\n%s", c.name, out)
		}
		for _, x := range xs {
			if evalGHA(t, x, deletion) {
				t.Errorf("%s: runs on a deletion push: %s", c.name, expr.String(x))
			}
			if got := evalGHA(t, x, update); got != c.pushMain {
				t.Errorf("%s: push to main runs=%v, want %v: %s", c.name, got, c.pushMain, expr.String(x))
			}
		}
	}
}

// TestFinallyJobNeverRunsOnADeletionPush: always() runs the job after a skipped needs, so it needs the guard too.
func TestFinallyJobNeverRunsOnADeletionPush(t *testing.T) {
	step := func() []*ir.Step { return []*ir.Step{{ID: "s", Kind: ir.StepScript, Script: []string{"echo hi"}}} }
	proj := &ir.Project{Origin: ir.Origin{Provider: "gitlab", Dialect: "gitlab"}, Pipelines: []*ir.Pipeline{{
		ID:       "p",
		Triggers: []ir.Trigger{{Kind: ir.TriggerPush}},
		Jobs:     []*ir.Job{{ID: "a", Steps: step()}},
		Finally:  []*ir.Job{{ID: "z", Steps: step()}},
	}}}
	files, _, err := prov(t, "gha").Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	out := string(files[".github/workflows/p.yml"])
	xs := ghaConditions(t, out)
	if len(xs) != 2 {
		t.Fatalf("want an if on both jobs:\n%s", out)
	}
	for _, x := range xs {
		if evalGHA(t, x, payload{"event": "push", "deleted": true}) {
			t.Errorf("runs on a deletion push: %s", expr.String(x))
		}
		if !evalGHA(t, x, payload{"event": "push", "deleted": false}) {
			t.Errorf("does not run on a normal push: %s", expr.String(x))
		}
	}
}

const ghaPlain = "on: push\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"
const ghaHand = "on: push\njobs:\n  a:\n    runs-on: ubuntu-latest\n    if: ${{ !github.event.deleted }}\n    steps:\n      - run: echo hi\n"

// TestGHASourceKeepsItsOwnDeletionSemantics: a GHA source runs on a deletion already, so GHA to GHA adds no guard,
// and a guard the author wrote stays.
func TestGHASourceKeepsItsOwnDeletionSemantics(t *testing.T) {
	out, _ := convert(t, "gha", "gha", ".github/workflows/ci.yml", ghaPlain)
	if strings.Contains(out, "deleted") {
		t.Errorf("guard added to a GHA source:\n%s", out)
	}
	out, _ = convert(t, "gha", "gha", ".github/workflows/ci.yml", ghaHand)
	if strings.Count(out, "github.event.deleted") != 1 {
		t.Errorf("hand-written guard lost or doubled:\n%s", out)
	}
}

// TestDeletionGuardIsStrippedOnTheWayBack: gitlab to gha to gitlab is the same as gitlab alone.
func TestDeletionGuardIsStrippedOnTheWayBack(t *testing.T) {
	for _, doc := range []string{
		"job:\n  script: [echo hi]\n",
		"job:\n  script: [echo hi]\n  rules:\n    - if: '$CI_COMMIT_BRANCH == \"main\"'\n",
		"job:\n  script: [echo hi]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE != \"merge_request_event\"'\n",
	} {
		direct, _ := convert(t, "gitlab", "gitlab", ".gitlab-ci.yml", doc)
		viaGHA, _ := convert(t, "gitlab", "gha", ".gitlab-ci.yml", doc)
		proj, _, err := prov(t, "gha").Parse(fstest.MapFS{".github/workflows/gitlab-ci.yml": &fstest.MapFile{Data: []byte(strings.TrimPrefix(viaGHA, "### .github/workflows/gitlab-ci.yml\n"))}})
		if err != nil {
			t.Fatal(err)
		}
		files, _, err := prov(t, "gitlab").Emit(proj)
		if err != nil {
			t.Fatal(err)
		}
		// A GHA push never fires on web, api or schedule, so the way back scopes the default to the push source.
		if got := string(files[".gitlab-ci.yml"]); strings.HasPrefix(got, "workflow:") {
			direct = strings.Replace(direct, "job:", "workflow:\n  rules:\n    - if: $CI_PIPELINE_SOURCE == \"push\" && $CI_COMMIT_BRANCH\n    - if: $CI_PIPELINE_SOURCE == \"push\" && $CI_COMMIT_TAG\njob:", 1)
		}
		if got := "### .gitlab-ci.yml\n" + string(files[".gitlab-ci.yml"]); got != direct {
			t.Errorf("not exact for %q:\n--- direct\n%s\n--- via gha\n%s", doc, direct, got)
		}
	}
}

const (
	ghaNativeIf = "on: push\njobs:\n  native:\n    runs-on: ubuntu-latest\n    if: ${{ github.ref_name == 'main' }}\n    steps:\n      - run: echo hi\n"
	ghaNativeNo = "on: push\njobs:\n  native:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"
	glMain      = "job:\n  script: [echo hi]\n  rules:\n    - if: '$CI_COMMIT_BRANCH == \"main\"'\n"
)

// mixedDir holds a gitlab file converted to GHA beside a hand-written workflow.
func mixedDir(t *testing.T, native string) fstest.MapFS {
	t.Helper()
	viaGHA, _ := convert(t, "gitlab", "gha", ".gitlab-ci.yml", glMain)
	return fstest.MapFS{
		".github/workflows/gitlab-ci.yml": &fstest.MapFile{Data: []byte(strings.TrimPrefix(viaGHA, "### .github/workflows/gitlab-ci.yml\n"))},
		".github/workflows/native.yml":    &fstest.MapFile{Data: []byte(native)},
	}
}

func reEmit(t *testing.T, fsys fstest.MapFS, from, to string) fstest.MapFS {
	t.Helper()
	proj, _, err := prov(t, from).Parse(fsys)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := prov(t, to).Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for n, b := range files {
		out[n] = &fstest.MapFile{Data: b}
	}
	return out
}

// alone is how GHA to GHA writes a hand-written workflow with nothing beside it.
func alone(t *testing.T, native string) string {
	t.Helper()
	out := reEmit(t, fstest.MapFS{".github/workflows/native.yml": &fstest.MapFile{Data: []byte(native)}}, "gha", "gha")
	return string(out[".github/workflows/native.yml"].Data)
}

var mixedNatives = []struct{ name, native string }{{"native job with if", ghaNativeIf}, {"native job without if", ghaNativeNo}}

// TestMixedDirectoryGuardsPerJob: the guard belongs to the job that carried it, whatever its sibling files hold.
func TestMixedDirectoryGuardsPerJob(t *testing.T) {
	for _, c := range mixedNatives {
		out := reEmit(t, mixedDir(t, c.native), "gha", "gha")
		conv := string(out[".github/workflows/gitlab-ci.yml"].Data)
		if !strings.Contains(conv, "!github.event.deleted") {
			t.Errorf("%s: converted job lost its deletion guard:\n%s", c.name, conv)
		}
		if got := string(out[".github/workflows/native.yml"].Data); strings.Contains(got, "deleted") {
			t.Errorf("%s: hand-written job gained a guard:\n%s", c.name, got)
		}
		if c.native == ghaNativeIf {
			if got, want := string(out[".github/workflows/native.yml"].Data), alone(t, c.native); got != want {
				t.Errorf("%s: native workflow changed:\n--- want\n%s\n--- got\n%s", c.name, want, got)
			}
		}
	}
}

// TestMixedDirectoryThroughGitLabAndBack: after GitLab the jobs are foreign and each guard is written once.
func TestMixedDirectoryThroughGitLabAndBack(t *testing.T) {
	for _, c := range mixedNatives {
		back := reEmit(t, reEmit(t, mixedDir(t, c.native), "gha", "gitlab"), "gitlab", "gha")
		for n, f := range back {
			if strings.Count(string(f.Data), "!github.event.deleted") > 1 {
				t.Errorf("%s: guard doubled in %s:\n%s", c.name, n, f.Data)
			}
		}
	}
}
