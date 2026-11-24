package roundtrip_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprgha "github.com/lukaszraczylo/pipemaster/expr/gha"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	wpexpr "github.com/lukaszraczylo/pipemaster/expr/woodpecker"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

const zeroSHA = "0000000000000000000000000000000000000000"

// scenario is one event seen by GitLab and by GitHub Actions at once.
type scenario struct {
	gl      map[string]string
	gh      map[string]any
	name    string
	push    bool
	pr      bool
	tag     bool
	sched   bool
	created bool
	deleted bool
}

var scenarios = []scenario{
	{name: "push main", push: true, gl: map[string]string{"branch": "main", "event": "push"},
		gh: map[string]any{"event": "push", "ref_type": "branch", "ref_name": "main", "deleted": false, "created": false}},
	{name: "push dev", push: true, gl: map[string]string{"branch": "dev", "event": "push"},
		gh: map[string]any{"event": "push", "ref_type": "branch", "ref_name": "dev", "deleted": false, "created": false}},
	{name: "push new branch", push: true, created: true, gl: map[string]string{"branch": "dev", "event": "push"},
		gh: map[string]any{"event": "push", "ref_type": "branch", "ref_name": "dev", "deleted": false, "created": true}},
	{name: "merge request", pr: true, gl: map[string]string{"event": "merge_request_event", "pr.number": "7"},
		gh: map[string]any{"event": "pull_request", "ref_type": "branch", "ref_name": "7/merge"}},
	{name: "tag", push: true, tag: true, gl: map[string]string{"tag": "v1", "event": "push"},
		gh: map[string]any{"event": "push", "ref_type": "tag", "ref_name": "v1", "deleted": false, "created": true}},
	{name: "schedule", sched: true, gl: map[string]string{"branch": "main", "event": "schedule"},
		gh: map[string]any{"event": "schedule", "ref_type": "branch", "ref_name": "main"}},
	{name: "branch deletion", push: true, deleted: true,
		gh: map[string]any{"event": "push", "ref_type": "branch", "ref_name": "dev", "deleted": true, "created": false}},
}

type glRun map[string]string

func (g glRun) Lookup(path []string) (expr.Value, bool) {
	v, ok := g[strings.Join(path, ".")]
	return v, ok && v != ""
}
func (glRun) Status() expr.Status { return expr.Success }

// arm is one rule of the reference model.
type arm struct {
	If      string
	When    string
	Changes []string
}

func (a arm) matches(t *testing.T, env glRun, changed bool) bool {
	t.Helper()
	if a.If != "" {
		x, err := exprgitlab.Parse(a.If)
		if err != nil {
			t.Fatal(err)
		}
		v, err := expr.Eval(x, env, exprgitlab.Semantics)
		if err != nil {
			t.Fatal(err)
		}
		if !exprgitlab.Semantics.Truthy(v) {
			return false
		}
	}
	return len(a.Changes) == 0 || changed
}

// glDecision is the documented GitLab behaviour: workflow:rules admit the pipeline, the first matching job rule
// decides, and a manual start is not a run. A pipeline for a branch deletion does not exist.
func glDecision(t *testing.T, s scenario, workflow, rules []arm, changedFiles bool) bool {
	t.Helper()
	if s.deleted {
		return false
	}
	env := glRun(s.gl)
	// rules:changes is true without a push event (tag, schedule) and for a new branch.
	changed := changedFiles || s.tag || s.sched || s.created
	if len(workflow) > 0 {
		admitted := false
		for _, a := range workflow {
			if a.matches(t, env, changed) {
				admitted = a.When != "never"
				break
			}
		}
		if !admitted {
			return false
		}
	}
	for _, a := range rules {
		if !a.matches(t, env, changed) {
			continue
		}
		return a.When != "never" && a.When != "manual"
	}
	return false
}

func armsYAML(arms []arm) string {
	var sb strings.Builder
	for _, a := range arms {
		lead := "    - "
		put := func(s string) {
			sb.WriteString(lead + s + "\n")
			lead = "      "
		}
		if a.If != "" {
			put("if: '" + a.If + "'")
		}
		if len(a.Changes) > 0 {
			put("changes:")
			for _, c := range a.Changes {
				sb.WriteString("        - '" + c + "'\n")
			}
		}
		if a.When != "" {
			put("when: " + a.When)
		} else if a.If == "" && len(a.Changes) == 0 {
			put("when: always")
		}
	}
	return sb.String()
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // fixed binary, test temp dir
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// history builds a repository whose second commit changes the given files and returns both SHAs.
func history(t *testing.T, files map[string]string) (dir, before, after string) {
	t.Helper()
	dir = t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write := func(m map[string]string) {
		for name, body := range m {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(map[string]string{"README.md": "a", "go.mod": "module x", "Makefile": "all:"})
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "one")
	before = git(t, dir, "rev-parse", "HEAD")
	write(files)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "two")
	return dir, before, git(t, dir, "rev-parse", "HEAD")
}

type ghaWorkflowDoc struct {
	On   any `yaml:"on"`
	Jobs map[string]struct {
		If      string `yaml:"if"`
		Needs   any    `yaml:"needs"`
		Outputs map[string]string
		Steps   []struct {
			Env map[string]string `yaml:"env"`
			ID  string            `yaml:"id"`
			Run string            `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

const changesJob = "pipemaster-changes"

// admits is true when the workflow's on: block starts a run for the scenario.
func (d ghaWorkflowDoc) admits(s scenario) bool {
	var events map[string]any
	switch v := d.On.(type) {
	case string:
		events = map[string]any{v: nil}
	case []any:
		events = map[string]any{}
		for _, e := range v {
			events[e.(string)] = nil
		}
	case map[string]any:
		events = v
	}
	switch {
	case s.sched:
		_, ok := events["schedule"]
		return ok
	case s.pr:
		_, ok := events["pull_request"]
		return ok
	case s.push:
		f, ok := events["push"]
		if !ok {
			return false
		}
		cfg, _ := f.(map[string]any)
		globs := func(k string) []string {
			var out []string
			l, _ := cfg[k].([]any)
			for _, g := range l {
				out = append(out, g.(string))
			}
			return out
		}
		branches, tags := globs("branches"), globs("tags")
		if s.tag {
			return len(tags) > 0 || len(branches) == 0
		}
		if len(branches) == 0 {
			return len(tags) == 0
		}
		for _, b := range branches {
			if b == "**" || b == s.gh["ref_name"] {
				return true
			}
		}
		return false
	}
	return false
}

var ghaExprRE = strings.NewReplacer("${{", "", "}}", "")

// runChanges runs the step the emitter wrote for the changed-paths job, as GitHub would, and returns its outputs.
func runChanges(t *testing.T, doc ghaWorkflowDoc, s scenario, dir, before, after string) map[string]string {
	t.Helper()
	res, out, err := execChanges(t, doc, s, dir, before, after)
	if err != nil {
		t.Fatalf("%s: step failed: %v\n%s", s.name, err, out)
	}
	return res
}

// execChanges runs the changed-paths step and returns its outputs, its console text and its error.
func execChanges(t *testing.T, doc ghaWorkflowDoc, s scenario, dir, before, after string) (map[string]string, string, error) {
	t.Helper()
	job, ok := doc.Jobs[changesJob]
	if !ok {
		return nil, "", nil
	}
	values := map[string]string{
		"github.event_name":                  s.gh["event"].(string),
		"github.ref_type":                    s.gh["ref_type"].(string),
		"github.ref_name":                    s.gh["ref_name"].(string),
		"github.sha":                         after,
		"github.event.after":                 after,
		"github.event.before":                before,
		"github.event.pull_request.base.sha": before,
		"github.event.pull_request.head.sha": after,
	}
	if s.created {
		values["github.event.before"] = zeroSHA
	}
	for _, st := range job.Steps {
		if st.Run == "" {
			continue
		}
		env := os.Environ()
		for k, v := range st.Env {
			val := v
			if strings.Contains(v, "${{") {
				key := strings.TrimSpace(ghaExprRE.Replace(v))
				var known bool
				if val, known = values[key]; !known {
					t.Fatalf("env %s uses %q, which the harness has no value for", k, v)
				}
			}
			env = append(env, k+"="+val)
		}
		out := filepath.Join(t.TempDir(), "output")
		env = append(env, "GITHUB_OUTPUT="+out, "RUNNER_TEMP="+t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null")
		cmd := exec.Command("bash", "-e", "-c", st.Run) //nolint:gosec // runs the emitted script in a temp repo
		cmd.Dir, cmd.Env = dir, env
		b, err := cmd.CombinedOutput()
		raw, _ := os.ReadFile(out) //nolint:gosec // path in the test temp dir
		res := map[string]string{}
		for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if k, v, ok := strings.Cut(l, "="); ok {
				res[k] = v
			}
		}
		return res, string(b), err
	}
	return nil, "", nil
}

type changesCase struct {
	name     string
	workflow []arm
	rules    []arm
	// onlyAdmitted is true when the case has no workflow rules, so the emitted triggers are the default push only
	// and a merge request is not admitted at all.
}

var (
	goGlobs   = []string{"go.mod", "**/*.go"}
	onMain    = `$CI_COMMIT_BRANCH == "main"`
	onMR      = `$CI_PIPELINE_SOURCE == "merge_request_event"`
	onPush    = `$CI_PIPELINE_SOURCE == "push"`
	onTag     = `$CI_COMMIT_TAG`
	mrOrMain  = []arm{{If: `$CI_MERGE_REQUEST_IID`}, {If: onMain}, {If: onTag}}
	changeSet = map[string]map[string]string{"README.md only": {"README.md": "b"}, "go file": {"src/a.go": "package a"}}
)

func TestGitlabRulesRunOnGHAExactlyWhereGitLabRuns(t *testing.T) {
	cases := []changesCase{
		{name: "if only", rules: []arm{{If: onMain}}},
		{name: "if when never", rules: []arm{{If: onMR, When: "never"}, {If: onPush}}},
		{name: "if when manual", rules: []arm{{If: onMain, When: "manual"}}},
		{name: "changes only", rules: []arm{{Changes: goGlobs}}},
		{name: "changes and if", rules: []arm{{If: onMain, Changes: goGlobs}}},
		{name: "two changes arms", rules: []arm{{Changes: []string{"**/*.go"}}, {Changes: []string{"Makefile"}}}},
		{name: "never then changes", rules: []arm{{If: onTag, When: "never"}, {If: onMain, Changes: goGlobs}}},
		{name: "mixed auto and manual", rules: []arm{{If: onMain}, {If: onMR, When: "manual"}}},
		{name: "workflow rules anded", workflow: []arm{{If: onTag, When: "never"}, {}}, rules: []arm{{If: onMain, Changes: goGlobs}}},
		{name: "workflow triggers and changes", workflow: mrOrMain, rules: []arm{{Changes: goGlobs}}},
		{name: "job rules equal workflow rules", workflow: mrOrMain, rules: mrOrMain},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sb strings.Builder
			if len(c.workflow) > 0 {
				sb.WriteString("workflow:\n  rules:\n" + armsYAML(c.workflow))
			}
			sb.WriteString("j:\n  script: [echo hi]\n  rules:\n" + armsYAML(c.rules))
			out, _ := convert(t, provider.GitLab, provider.GHA, gitlabCI, sb.String())
			_, wf, _ := strings.Cut(out, "### .github/workflows/")
			_, wf, _ = strings.Cut(wf, "\n")
			var doc ghaWorkflowDoc
			if err := yaml.Unmarshal([]byte(wf), &doc); err != nil {
				t.Fatalf("emitted workflow: %v\n%s", err, wf)
			}
			if strings.TrimSpace(doc.Jobs["j"].If) == "false" {
				t.Fatalf("the job is disabled:\n%s", wf)
			}
			exactRuns := 0
			for fname, files := range changeSet {
				dir, before, after := history(t, files)
				for _, s := range scenarios {
					want := glDecision(t, s, c.workflow, c.rules, fname == "go file")
					got := false
					if doc.admits(s) {
						got = ghaJobRuns(t, doc, "j", s, runChanges(t, doc, s, dir, before, after))
					}
					switch {
					case got && !want:
						t.Errorf("%s / %s: GHA runs the job and GitLab does not\n%s", s.name, fname, wf)
					case want && !got && doc.admits(s):
						t.Errorf("%s / %s: GitLab runs the job on an event the workflow admits and GHA does not\n%s", s.name, fname, wf)
					case want && got:
						exactRuns++
					}
				}
			}
			if exactRuns == 0 && c.name != "if when manual" {
				t.Errorf("the job never runs on GHA; the matrix proves nothing:\n%s", wf)
			}
		})
	}
}

// ghaJobRuns evaluates the job's if under GHA semantics, with the outputs of the changed-paths job as needs.
func ghaJobRuns(t *testing.T, doc ghaWorkflowDoc, id string, s scenario, outputs map[string]string) bool {
	t.Helper()
	env := payload{}
	for k, v := range s.gh {
		env[k] = v
	}
	for k, v := range outputs {
		env["needs."+changesJob+".outputs."+k] = v
	}
	if gen, ok := doc.Jobs[changesJob]; ok && gen.If != "" && !evalGHA(t, parseCond(t, gen.If), env) {
		return false
	}
	j := doc.Jobs[id]
	if j.If == "" || j.If == "true" {
		return true
	}
	return evalGHA(t, parseCond(t, j.If), env)
}

func parseCond(t *testing.T, text string) expr.Expr {
	t.Helper()
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

func ghaFor(t *testing.T, glob string) (string, ghaWorkflowDoc) {
	t.Helper()
	src := "j:\n  script: [echo hi]\n  rules:\n    - changes:\n        - " + glob + "\n"
	out, _ := convert(t, provider.GitLab, provider.GHA, gitlabCI, src)
	_, wf, _ := strings.Cut(out, "### .github/workflows/")
	_, wf, _ = strings.Cut(wf, "\n")
	var doc ghaWorkflowDoc
	if err := yaml.Unmarshal([]byte(wf), &doc); err != nil {
		t.Fatalf("emitted workflow: %v\n%s", err, wf)
	}
	return wf, doc
}

var pushMain = scenarios[0]

// TestChangesGlobsNeverReachTheShell: a glob is data. Hostile globs either stay out of the workflow (the job is
// disabled and reported) or run with no side effect, and no glob text appears in the script body.
func TestChangesGlobsNeverReachTheShell(t *testing.T) {
	hostile := []string{
		`"a[';touch PWNED;']b"`, `"$(touch PWNED)"`, "\"`touch PWNED`\"", `"x;touch PWNED"`, `"a[;touch PWNED]b"`,
		`"a']b[']"`, `"*.go' ; touch PWNED ; '"`, `'a"b'`, `"a\\b"`, `"a[!b]c"`, `"a[]]b"`, `"{a,b;touch PWNED}"`,
	}
	for _, g := range hostile {
		t.Run(g, func(t *testing.T) {
			wf, doc := ghaFor(t, g)
			dir, before, after := history(t, map[string]string{"a/x.go": "package a"})
			if job, ok := doc.Jobs[changesJob]; ok {
				for _, st := range job.Steps {
					if strings.Contains(st.Run, "touch") {
						t.Fatalf("glob text is in the script body:\n%s", st.Run)
					}
				}
				runChanges(t, doc, pushMain, dir, before, after)
			}
			if _, err := os.Stat(filepath.Join(dir, "PWNED")); err == nil {
				t.Fatalf("the glob ran a command:\n%s", wf)
			}
			if _, ok := doc.Jobs[changesJob]; !ok && !strings.Contains(doc.Jobs["j"].If, "false") {
				t.Fatalf("no changed-paths job and the job is not disabled:\n%s", wf)
			}
		})
	}
}

// TestChangesMatchesAwkwardFileNames: a name with a quote, a backslash, a tab or a newline is a path like any other.
func TestChangesMatchesAwkwardFileNames(t *testing.T) {
	_, doc := ghaFor(t, "'**/*.go'")
	for _, name := range []string{"a/we\"ird.go", "a/back\\slash.go", "a/tab\there.go", "a/new\nline.go", "a/semi;colon.go"} {
		dir, before, after := history(t, map[string]string{name: "package a"})
		got := runChanges(t, doc, pushMain, dir, before, after)
		if len(got) != 1 {
			t.Fatalf("outputs = %v", got)
		}
		for _, v := range got {
			if v != "true" {
				t.Errorf("%q: changes is %s, want true", name, v)
			}
		}
	}
}

// TestChangesForcePushFailsLoudly: a commit before the push that the clone cannot get fails the job with its reason,
// so no gated job runs; it never reports false.
func TestChangesForcePushFailsLoudly(t *testing.T) {
	_, doc := ghaFor(t, "'**/*.go'")
	dir, _, after := history(t, map[string]string{"a/x.go": "package a"})
	_, out, err := execChanges(t, doc, pushMain, dir, "1111111111111111111111111111111111111111", after)
	if err == nil || !strings.Contains(out, "is not in the clone") {
		t.Fatalf("want a failure naming the reason, got err=%v out=%q", err, out)
	}
}

type wpWhen []struct {
	Event    any    `yaml:"event"`
	Branch   any    `yaml:"branch"`
	Path     any    `yaml:"path"`
	Evaluate string `yaml:"evaluate"`
}

func anyList(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, e.(string))
		}
		return out
	case map[string]any:
		return anyList(x["include"])
	}
	return nil
}

// wpEvent names the Woodpecker event of a scenario.
func wpEvent(s scenario) string {
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

// wpRuns evaluates the emitted when list for an event: a path filter applies to push and pull request only and any
// other event passes it, as the Woodpecker workflow syntax documents.
func wpRuns(w wpWhen, s scenario, changed bool) bool {
	for _, e := range w {
		if e.Evaluate == "false" || e.Evaluate != "" && !wpEvaluate(e.Evaluate, s) {
			continue
		}
		if ev := anyList(e.Event); len(ev) > 0 && !slices.Contains(ev, wpEvent(s)) {
			continue
		}
		if br := anyList(e.Branch); len(br) > 0 && !slices.Contains(br, s.gh["ref_name"].(string)) {
			continue
		}
		if p := anyList(e.Path); len(p) > 0 && (s.push && !s.tag || s.pr) && !changed {
			continue
		}
		return true
	}
	return false
}

// wpEvaluate evaluates the evaluate text of an entry against the scenario: Woodpecker holds the branch of the run,
// and the target branch (main) on a pull request.
func wpEvaluate(text string, s scenario) bool {
	x, err := wpexpr.Parse(text)
	if err != nil {
		panic(err)
	}
	env := glRun{"event": wpEvent(s), "branch": "main"}
	if name, ok := s.gh["ref_name"].(string); ok && s.push && !s.tag {
		env["branch"] = name
	}
	if s.tag {
		// The model follows the facts of the existing emitter: a tag run has no branch.
		delete(env, "branch")
		env["tag"] = s.gh["ref_name"].(string)
	}
	v, err := expr.Eval(x, wpexpr.UnsetEnv{Env: env}, wpexpr.Semantics)
	if err != nil {
		panic(err)
	}
	b, _ := v.(bool)
	return b
}

// wpListsEvent is true when some entry names the scenario's event; an event no entry names is a trigger difference,
// not a path filter result.
func wpListsEvent(w wpWhen, s scenario) bool {
	return slices.ContainsFunc(w, func(e struct {
		Event    any    `yaml:"event"`
		Branch   any    `yaml:"branch"`
		Path     any    `yaml:"path"`
		Evaluate string `yaml:"evaluate"`
	}) bool {
		return slices.Contains(anyList(e.Event), wpEvent(s))
	})
}

// TestGitlabChangesRunOnWoodpeckerNeverWhereGitLabSkips: a path predicate becomes a native path filter; a condition
// the filter cannot state keeps the workflow disabled. Woodpecker may skip a run GitLab makes (a new branch) but
// never makes a run GitLab skips.
func TestGitlabChangesRunOnWoodpeckerNeverWhereGitLabSkips(t *testing.T) {
	cases := []struct {
		name     string
		workflow []arm
		rules    []arm
		native   bool
	}{
		{name: "changes only", rules: []arm{{Changes: goGlobs}}, native: true},
		{name: "changes under workflow triggers", workflow: mrOrMain, rules: []arm{{Changes: goGlobs}}, native: true},
		{name: "changes and if", rules: []arm{{If: onMain, Changes: goGlobs}}, native: true},
		{name: "two changes arms", rules: []arm{{Changes: []string{"**/*.go"}}, {Changes: []string{"Makefile"}}}, native: true},
		{name: "glob with a class", rules: []arm{{Changes: []string{"src/[ab].go"}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sb strings.Builder
			if len(c.workflow) > 0 {
				sb.WriteString("workflow:\n  rules:\n" + armsYAML(c.workflow))
			}
			sb.WriteString("j:\n  script: [echo hi]\n  rules:\n" + armsYAML(c.rules))
			out, _ := convert(t, provider.GitLab, provider.Woodpecker, gitlabCI, sb.String())
			_, wf, _ := strings.Cut(out, "### .woodpecker/")
			_, wf, _ = strings.Cut(wf, "\n")
			var doc struct {
				When wpWhen `yaml:"when"`
			}
			if err := yaml.Unmarshal([]byte(wf), &doc); err != nil {
				t.Fatalf("emitted workflow: %v\n%s", err, wf)
			}
			if c.native != strings.Contains(wf, "path:") {
				t.Fatalf("native path filter = %v, want %v:\n%s", strings.Contains(wf, "path:"), c.native, wf)
			}
			runs := 0
			for _, changed := range []bool{true, false} {
				for _, s := range scenarios {
					if s.deleted {
						continue
					}
					want := glDecision(t, s, c.workflow, c.rules, changed)
					got := wpRuns(doc.When, s, changed)
					if got && !want {
						t.Errorf("%s changed=%v: Woodpecker runs the job and GitLab does not\n%s", s.name, changed, wf)
					}
					if got {
						runs++
					}
					if c.native && want && !got && wpListsEvent(doc.When, s) && (!s.created || !s.push) && (!s.pr || len(c.workflow) != 0) {
						t.Errorf("%s changed=%v: GitLab runs the job and the native filter does not\n%s", s.name, changed, wf)
					}
				}
			}
			if c.native != (runs > 0) {
				t.Errorf("runs = %d, native = %v:\n%s", runs, c.native, wf)
			}
		})
	}
}

// TestChangesAnchorsAtTheNameEnds: a newline inside a name must not start a new match, and an empty commit changes
// nothing.
func TestChangesAnchorsAtTheNameEnds(t *testing.T) {
	_, doc := ghaFor(t, "'src/**/*.go'")
	verdict := func(files map[string]string) string {
		dir, before, after := history(t, files)
		got := runChanges(t, doc, pushMain, dir, before, after)
		for _, v := range got {
			return v
		}
		t.Fatalf("no output: %v", got)
		return ""
	}
	if v := verdict(map[string]string{"zz\nsrc/q.go": "package a"}); v != "false" {
		t.Errorf("a name with an embedded newline matched: %s", v)
	}
	if v := verdict(map[string]string{"src/q.go": "package a"}); v != "true" {
		t.Errorf("src/q.go did not match: %s", v)
	}
	dir, _, _ := history(t, map[string]string{"README.md": "b"})
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "empty")
	before, after := git(t, dir, "rev-parse", "HEAD~1"), git(t, dir, "rev-parse", "HEAD")
	for _, v := range runChanges(t, doc, pushMain, dir, before, after) {
		if v != "false" {
			t.Errorf("an empty commit changed paths: %s", v)
		}
	}
}

// TestWoodpeckerPathFilterSkipsAnEmptyPush: GitLab finds no changed path in a push with no changes, so the filter
// says on_empty: false.
func TestWoodpeckerPathFilterSkipsAnEmptyPush(t *testing.T) {
	out, rep := convert(t, provider.GitLab, provider.Woodpecker, gitlabCI, "j:\n  script: [echo hi]\n  rules:\n    - changes: ['**/*.go']\n")
	if !strings.Contains(out, "on_empty: false") {
		t.Fatalf("no on_empty: false:\n%s", out)
	}
	_, wf, _ := strings.Cut(out, "### .woodpecker/")
	var doc struct {
		When []struct {
			Event any            `yaml:"event"`
			Path  map[string]any `yaml:"path"`
		} `yaml:"when"`
	}
	if err := yaml.Unmarshal([]byte(wf[strings.Index(wf, "\n")+1:]), &doc); err != nil {
		t.Fatal(err)
	}
	for _, e := range doc.When {
		if slices.Contains(anyList(e.Event), "push") && e.Path["on_empty"] != false {
			t.Errorf("push entry path = %v, want on_empty false", e.Path)
		}
	}
	if !slices.ContainsFunc(rep.Entries, func(e report.Entry) bool { return strings.Contains(e.Detail, "ignore_message") }) {
		t.Errorf("the report does not name ignore_message: %+v", rep.Entries)
	}
}
