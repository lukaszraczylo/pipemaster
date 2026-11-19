package gitlab_test

import (
	"reflect"
	"strings"
	"testing"
)

func TestLocalInclude(t *testing.T) {
	files := map[string]string{
		rootFile: `
include:
  - local: /ci/a.yml
  - ci/b.yml
  - local: ci/sub/*.yml
variables:
  FROM: main
build:
  script: [make]
`,
		"ci/a.yml":     "variables:\n  FROM: a\n  A: 1\njob-a:\n  script: [a]\n",
		"ci/b.yml":     "variables:\n  B: 2\njob-b:\n  script: [b]\n",
		"ci/sub/c.yml": "job-c:\n  script: [c]\n",
		"ci/sub/d.yml": "job-d:\n  script: [d]\n",
	}
	p, rep := parseFiles(t, files)
	pl := p.Pipelines[0]
	var ids []string
	for _, j := range pl.Jobs {
		ids = append(ids, j.ID)
	}
	if want := []string{"build", "job-a", "job-b", "job-c", "job-d"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("jobs = %v, want %v", ids, want)
	}
	env := map[string]string{}
	for _, v := range pl.Env {
		env[v.Name] = v.Value.Lit
	}
	if env["FROM"] != "main" || env["A"] != "1" || env["B"] != "2" {
		t.Errorf("env = %v; the main file must win over includes and includes must merge", env)
	}
	want := []string{rootFile, "ci/a.yml", "ci/b.yml", "ci/sub/c.yml", "ci/sub/d.yml"}
	if !reflect.DeepEqual(p.Origin.Files, want) {
		t.Errorf("Origin.Files = %v, want %v", p.Origin.Files, want)
	}
	if len(rep.Entries) != 0 {
		t.Errorf("local includes must report nothing, got %+v", rep.Entries)
	}
}

func TestIncludeOrderLastWins(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{
		rootFile: "include: [a.yml, b.yml]\n",
		"a.yml":  "job:\n  image: alpine\n  script: [a]\n  variables:\n    X: a\n    Y: a\n",
		"b.yml":  "job:\n  script: [b]\n  variables:\n    X: b\n",
	})
	j := job(t, p.Pipelines[0], "job")
	if j.Container == nil || j.Container.Ref != "alpine" {
		t.Errorf("hashes merge at any depth: container = %+v", j.Container)
	}
	if got := j.Steps[0].Script; !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("arrays are replaced by the later file: script = %v", got)
	}
	vals := map[string]string{}
	for _, v := range j.Env {
		vals[v.Name] = v.Value.Lit
	}
	if vals["X"] != "b" || vals["Y"] != "a" {
		t.Errorf("env = %v", vals)
	}
}

func TestNestedInclude(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{
		rootFile: "include: a.yml\nmain:\n  script: [m]\n",
		"a.yml":  "include: b.yml\nja:\n  script: [a]\n",
		"b.yml":  "jb:\n  script: [b]\n",
	})
	if n := len(p.Pipelines[0].Jobs); n != 3 {
		t.Fatalf("jobs = %d, want 3", n)
	}
	if want := []string{rootFile, "a.yml", "b.yml"}; !reflect.DeepEqual(p.Origin.Files, want) {
		t.Errorf("Origin.Files = %v", p.Origin.Files)
	}
}

func TestIncludeCycle(t *testing.T) {
	err := parseErr(t, map[string]string{
		rootFile: "include: a.yml\n",
		"a.yml":  "include: b.yml\n",
		"b.yml":  "x: 1\ninclude:\n  - a.yml\n",
	})
	msg := err.Error()
	for _, want := range []string{"b.yml:3", "include cycle", ".gitlab-ci.yml -> a.yml -> b.yml -> a.yml"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
}

func TestIncludeSelfCycle(t *testing.T) {
	err := parseErr(t, map[string]string{rootFile: "include: .gitlab-ci.yml\n"})
	if !strings.Contains(err.Error(), "include cycle") {
		t.Errorf("error = %v", err)
	}
}

func TestIncludeMissingLocal(t *testing.T) {
	err := parseErr(t, map[string]string{rootFile: "include:\n  - local: nope.yml\n"})
	if !strings.Contains(err.Error(), rootFile+":2") || !strings.Contains(err.Error(), "nope.yml") {
		t.Errorf("error = %v", err)
	}
}

func TestIncludeEscapesRoot(t *testing.T) {
	files := map[string]string{rootFile: "include: ../other.yml\n", "other.yml": "x:\n  script: [a]\n"}
	err := parseErr(t, files)
	if !strings.Contains(err.Error(), "leaves the repository") {
		t.Errorf("error = %v", err)
	}
}

func TestRemoteIncludeReported(t *testing.T) {
	pl, rep := pipeline(t, `
include:
  - remote: https://example.com/ci.yml
  - project: grp/proj
    file: /ci.yml
  - template: Auto-DevOps.gitlab-ci.yml
  - component: gitlab.com/grp/comp/build@1.0
  - https://example.com/raw.yml
build:
  script: [make]
`)
	inc, _ := ext(pl.Ext)["include"].([]any)
	if len(inc) != 5 {
		t.Fatalf("Ext include = %v, want 5 entries", inc)
	}
	if !hasEntry(rep, "remote include not followed") {
		t.Errorf("report lacks the remote include entry: %+v", rep.Entries)
	}
	n := 0
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "remote include not followed") {
			n++
			if e.Level.String() != "lost" {
				t.Errorf("level = %v", e.Level)
			}
		}
	}
	if n != 5 {
		t.Errorf("remote include report entries = %d, want 5", n)
	}
}

func TestIncludeRulesReported(t *testing.T) {
	_, rep := parseFiles(t, map[string]string{
		rootFile: "include:\n  - local: a.yml\n    rules:\n      - if: $X\nbuild:\n  script: [x]\n",
		"a.yml":  "other:\n  script: [y]\n",
	})
	if !hasEntry(rep, "include rules are not evaluated") {
		t.Errorf("report = %+v", rep.Entries)
	}
}

func TestIncludeGlobDoubleStar(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{
		rootFile:      "include: 'cfg/**.yml'\n",
		"cfg/a.yml":   "ja:\n  script: [a]\n",
		"cfg/x/b.yml": "jb:\n  script: [b]\n",
		"cfg/x/c.txt": "ignored",
	})
	if n := len(p.Pipelines[0].Jobs); n != 2 {
		t.Errorf("jobs = %d, want 2", n)
	}
}

func TestMissingRootFile(t *testing.T) {
	_, _, err := newProvider().Parse(repo(map[string]string{"other.yml": "x: 1"}))
	if err == nil || !strings.Contains(err.Error(), rootFile) {
		t.Errorf("error = %v", err)
	}
}

// The report names the remote includes a missing target may come from.
func TestRemoteDependenceNamesTheIncludes(t *testing.T) {
	src := "include:\n  - project: group/templates\n    file: ci.yml\n  - template: Security/SAST.gitlab-ci.yml\nbuild:\n  extends: .from-remote\n  script: [make]\n"
	_, rep := parseFiles(t, map[string]string{".gitlab-ci.yml": src})
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "depends on remote include") {
			if !strings.Contains(e.Detail, "project: group/templates") || !strings.Contains(e.Detail, "template: Security/SAST.gitlab-ci.yml") || !strings.Contains(e.Detail, ".from-remote") {
				t.Errorf("detail = %q", e.Detail)
			}
			return
		}
	}
	t.Error("no remote dependence entry")
}

func TestSpecStructuredDefaultIsReportedLost(t *testing.T) {
	src := "spec:\n  inputs:\n    envs:\n      default: [a, b]\n    plain:\n      default: x\n---\nbuild:\n  script: [make]\n"
	_, rep := parseFiles(t, map[string]string{".gitlab-ci.yml": src})
	var got []string
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "list or mapping default") {
			got = append(got, e.Detail)
		}
	}
	if len(got) != 1 || !strings.Contains(got[0], "envs") {
		t.Errorf("entries = %q", got)
	}
}
