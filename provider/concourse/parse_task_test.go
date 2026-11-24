package concourse_test

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/concourse"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
)

const taskCorpus = "../../testdata/corpus/87-concourse-examples-multi-branch-tasks"

func parseDir(t *testing.T, dir string) (*ir.Project, fs.FS) {
	t.Helper()
	fsys := os.DirFS(dir)
	p, _, err := concourse.New().Parse(fsys)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p, fsys
}

func TestTaskFilesOfTheRepositoryBecomeSteps(t *testing.T) {
	p, _ := parseDir(t, taskCorpus)
	pl := p.Pipelines[0]
	for _, id := range []string{"test", "build"} {
		j := job(t, pl, id)
		if j.Container == nil || j.Container.Ref != "golang:latest" {
			t.Errorf("%s: container = %+v, want golang:latest", id, j.Container)
		}
	}
	tests := step(t, job(t, pl, "test"), "tests")
	if tests.Kind != ir.StepScript {
		t.Fatalf("tests kind = %s, want script", tests.Kind)
	}
	if got := strings.Join(tests.Script, "\n"); !strings.Contains(got, `cd "repo/${MODULE}"`) || !strings.Contains(got, "go test -v ./...") {
		t.Errorf("tests script = %q", got)
	}
	if len(tests.Env) != 1 || tests.Env[0].Name != "MODULE" || tests.Env[0].Value.Lit != "apps/golang" {
		t.Errorf("tests env = %+v", tests.Env)
	}
	build := step(t, job(t, pl, "build"), "build")
	if len(build.Env) != 2 || build.Env[1].Name != "BINARY_NAME" || build.Env[1].Value.Lit != "ascii-banner" {
		t.Errorf("build env = %+v", build.Env)
	}
	run := step(t, job(t, pl, "build"), "run-binary")
	if !strings.Contains(strings.Join(run.Script, "\n"), "./binary/ascii-banner concourse") {
		t.Errorf("run-binary script = %q", run.Script)
	}
}

func TestRepositoryGetBecomesTheCheckoutStep(t *testing.T) {
	p, _ := parseDir(t, taskCorpus)
	co := step(t, job(t, p.Pipelines[0], "test"), "checkout")
	if co.Kind != ir.StepAction || co.Action == nil || co.Action.Repo != "checkout" {
		t.Errorf("checkout step = %+v", co)
	}
}

func TestMissingTaskFileIsReportedWithItsPath(t *testing.T) {
	in := repoFS(t, taskCorpus, "tasks/go-test.yml")
	p, rep, err := concourse.New().Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	tests := step(t, job(t, p.Pipelines[0], "test"), "tests")
	if tests.Kind != ir.StepTask {
		t.Errorf("kind = %s, want task", tests.Kind)
	}
	if !hasEntry(rep, "repo/tasks/go-test.yml") {
		t.Error("no report entry names repo/tasks/go-test.yml")
	}
}

// repoFS copies a directory into a map file system without the named files.
func repoFS(t *testing.T, dir string, skip ...string) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	root := os.DirFS(dir)
	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		for _, s := range skip {
			if s == path {
				return nil
			}
		}
		b, err := fs.ReadFile(root, path)
		out[path] = &fstest.MapFile{Data: b}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTaskFileOfAnotherResourceIsNeverReadLocally(t *testing.T) {
	in := repoFS(t, taskCorpus)
	in[".concourse/parent.yml"] = &fstest.MapFile{Data: []byte(`resources:
- name: repo
  type: git
  source: {uri: https://example.com/a.git, branch: main}
- name: other
  type: git
  source: {uri: https://example.com/b.git, branch: main}
jobs:
- name: test
  plan:
  - get: repo
    trigger: true
  - get: other
  - task: t
    file: other/tasks/go-test.yml
`)}
	p, rep, err := concourse.New().Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	st := step(t, job(t, p.Pipelines[0], "test"), "t")
	if st.Kind != ir.StepTask || len(st.Script) != 0 {
		t.Errorf("step = kind %s script %v, want an unread task", st.Kind, st.Script)
	}
	if !hasEntry(rep, "other/tasks/go-test.yml") {
		t.Error("no report entry names other/tasks/go-test.yml")
	}
}

func TestTaskFileWithADotDotPathIsNeverRead(t *testing.T) {
	in := repoFS(t, taskCorpus)
	in["secret.yml"] = &fstest.MapFile{Data: []byte("platform: linux\nrun: {path: echo, args: [x]}\n")}
	in[".concourse/parent.yml"] = &fstest.MapFile{Data: []byte(`resources:
- name: repo
  type: git
  source: {uri: https://example.com/a.git, branch: main}
jobs:
- name: test
  plan:
  - get: repo
    trigger: true
  - task: t
    file: repo/../secret.yml
`)}
	p, _, err := concourse.New().Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if st := step(t, job(t, p.Pipelines[0], "test"), "t"); st.Kind != ir.StepTask {
		t.Errorf("kind = %s, want task", st.Kind)
	}
}

func TestImageGetKeepsTagAndDigestExactly(t *testing.T) {
	for _, tc := range []struct{ name, res, get, want string }{
		{"tag", "repository: golang, tag: 1.22-alpine", "", "golang:1.22-alpine"},
		{"digest", "repository: golang", "version: {digest: 'sha256:abc'}", "golang@sha256:abc"},
		{"tag and digest", "repository: reg.example.com/go, tag: '1.22'", "version: {digest: 'sha256:abc'}", "reg.example.com/go:1.22@sha256:abc"},
		{"no tag", "repository: golang", "", "golang"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pl, _ := parseYAML(t, "resources:\n- name: repo\n  type: git\n  source: {uri: https://example.com/r.git, branch: main}\n- name: img\n  type: registry-image\n  source: {"+tc.res+"}\njobs:\n- name: b\n  plan:\n  - get: repo\n    trigger: true\n  - get: img\n    "+tc.get+"\n  - task: t\n    image: img\n    config:\n      platform: linux\n      run: {path: echo, args: [hi]}\n")
			j := job(t, pl, "b")
			if j.Container == nil || j.Container.Ref != tc.want {
				t.Errorf("container = %+v, want %s", j.Container, tc.want)
			}
		})
	}
}

func TestTaskFilesSurviveTheConcourseIdentityTrip(t *testing.T) {
	src, _ := parseDir(t, taskCorpus)
	files, _, err := concourse.New().Emit(src)
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for n, b := range files {
		out[n] = &fstest.MapFile{Data: b}
	}
	text := string(files[".concourse/parent.yml"])
	for _, want := range []string{"file: repo/tasks/go-test.yml", "file: repo/tasks/go-build.yml", "file: repo/apps/golang/binary-test.yml"} {
		if !strings.Contains(text, want) {
			t.Errorf("emitted file lost %q:\n%s", want, text)
		}
	}
	back, _, err := concourse.New().Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := ir.Equal(src, back); !same {
		t.Errorf("identity trip differs:\n%s", diff)
	}
}

func TestEditedTaskStepDropsTheFileReference(t *testing.T) {
	src, _ := parseDir(t, taskCorpus)
	st := step(t, job(t, src.Pipelines[0], "test"), "tests")
	st.Script = append(st.Script, "echo extra")
	files, _, err := concourse.New().Emit(src)
	if err != nil {
		t.Fatal(err)
	}
	text := string(files[".concourse/parent.yml"])
	if strings.Contains(text, "repo/tasks/go-test.yml") || !strings.Contains(text, "echo extra") {
		t.Errorf("edited step still points at the file:\n%s", text)
	}
}

func TestTaskFilesReachTheGitLabFile(t *testing.T) {
	src, _ := parseDir(t, taskCorpus)
	files, _, err := gitlab.New().Emit(src)
	if err != nil {
		t.Fatal(err)
	}
	text := string(files[".gitlab-ci.yml"])
	for _, want := range []string{"image: golang:latest", "MODULE", "go test -v ./...", "go build -o", "./binary/ascii-banner concourse"} {
		if !strings.Contains(text, want) {
			t.Errorf("GitLab file lacks %q:\n%s", want, text)
		}
	}
}

func TestTaskFileDirectoriesAreLaidOutByScriptLines(t *testing.T) {
	p, rep, err := concourse.New().Parse(os.DirFS(taskCorpus))
	if err != nil {
		t.Fatal(err)
	}
	j := job(t, p.Pipelines[0], "build")
	got := strings.Join(step(t, j, "build").Script, "\n")
	for _, want := range []string{`[ -e repo ] || ln -s "$PWD" repo`, "mkdir -p binary"} {
		if !strings.Contains(got, want) {
			t.Errorf("build script lacks %q:\n%s", want, got)
		}
	}
	if sh := step(t, j, "build").Shell; sh != "" {
		t.Errorf("shell = %q, want none for sh -cex", sh)
	}
	if !hasEntry(rep, "link the checkout or make the directory") {
		t.Error("no emulated entry for the layout lines")
	}
}

func TestAcrossOfLiteralValuesIsTheJobMatrix(t *testing.T) {
	pl, _ := parseYAML(t, oneJob(`  - across:
    - var: go
      values: ["1.21", "1.22"]
    do:
    - task: t
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: golang}}
        run: {path: echo, args: [hi]}
      params: {GO_VERSION: ((.:go))}
`))
	j := job(t, pl, "build")
	if j.Matrix == nil || len(j.Matrix.Axes) != 1 || j.Matrix.Axes[0].Name != "go" || len(j.Matrix.Axes[0].Values) != 2 {
		t.Fatalf("matrix = %+v", j.Matrix)
	}
	st := step(t, j, "t")
	if len(st.Env) != 1 || st.Env[0].Value.Kind != ir.ValueExpr {
		t.Errorf("env = %+v, want the matrix reference", st.Env)
	}
}

func TestAcrossOfADynamicValueStaysLost(t *testing.T) {
	pl, rep := parseYAML(t, oneJob(`  - across:
    - var: b
      values: ((.:branches))
    do:
    - task: t
      config:
        platform: linux
        image_resource: {type: registry-image, source: {repository: golang}}
        run: {path: echo, args: [hi]}
`))
	if job(t, pl, "build").Matrix != nil {
		t.Error("a dynamic across became a matrix")
	}
	if !hasEntry(rep, "across") {
		t.Error("no across entry")
	}
}

func TestTaskFileThatIsALinkIsNeverRead(t *testing.T) {
	in := repoFS(t, taskCorpus)
	in["tasks/go-test.yml"] = &fstest.MapFile{Data: []byte("../../outside.yml"), Mode: fs.ModeSymlink}
	in["../outside.yml"] = &fstest.MapFile{Data: []byte("platform: linux\nrun: {path: echo, args: [x]}\n")}
	p, rep, err := concourse.New().Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if st := step(t, job(t, p.Pipelines[0], "test"), "tests"); st.Kind != ir.StepTask {
		t.Errorf("kind = %s, want task", st.Kind)
	}
	if !hasEntry(rep, "repo/tasks/go-test.yml") {
		t.Error("no report entry names the path")
	}
}
