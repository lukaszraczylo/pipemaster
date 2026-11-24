package roundtrip_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/report"
)

// emitFiles parses src files with one provider and emits them with another, without metadata.
func emitFiles(t *testing.T, from, to string, src map[string]string) (map[string][]byte, *report.Report) {
	t.Helper()
	in := map[string][]byte{}
	for n, s := range src {
		in[n] = []byte(s)
	}
	proj, _, err := prov(t, from).Parse(toFS(in))
	if err != nil {
		t.Fatalf("parse %s: %v", from, err)
	}
	files, rep, err := prov(t, to).Emit(proj)
	if err != nil {
		t.Fatalf("emit %s: %v", to, err)
	}
	return files, rep
}

// gitlabJobs decodes the jobs of an emitted .gitlab-ci.yml, in no particular order.
func gitlabJobs(t *testing.T, files map[string][]byte) map[string]map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(files[gitlabCI], &doc); err != nil {
		t.Fatalf("decode %s: %v", gitlabCI, err)
	}
	out := map[string]map[string]any{}
	for k, v := range doc {
		if m, ok := v.(map[string]any); ok && m["script"] != nil {
			out[k] = m
		}
	}
	return out
}

func strList(v any) []string {
	var out []string
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		for _, it := range x {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func imageOf(job map[string]any) string {
	switch x := job["image"].(type) {
	case string:
		return x
	case map[string]any:
		s, _ := x["name"].(string)
		return s
	}
	return ""
}

func lostImagePaths(rep *report.Report) []string {
	var out []string
	for _, e := range rep.Entries {
		if e.Feature == caps.StepImage && e.Level == caps.Lost {
			out = append(out, e.IRPath)
		}
	}
	return out
}

func TestDroneCorpusSplitsGitLabJobsByImage(t *testing.T) {
	src := corpusFile(t, "51-drone-parsr", ".drone.yml")
	files, rep := emitFiles(t, "drone", "gitlab", map[string]string{".drone.yml": src})
	jobs := gitlabJobs(t, files)

	for _, p := range lostImagePaths(rep) {
		// DRONE_BRANCH is the target branch on a pull request, so the image field cannot spell it on GitLab.
		if !strings.Contains(p, "Docker") && !strings.Contains(p, "Code analysis") && !strings.Contains(p, "API") {
			t.Errorf("a script step still loses its image: %s", p)
		}
	}
	want := map[string]string{
		"API-1": "alpine:latest", "API-2": "axarev/parsr-base:${DRONE_BRANCH}", "API-3": "busybox",
		"UI-1": "alpine:latest", "UI-2": "registry.access.redhat.com/ubi8/nodejs-10", "UI-3": "busybox",
		"baseimage": "busybox", "Sonarqube": "slallemand/slack-webhook:latest",
	}
	for id, img := range want {
		j, ok := jobs[id]
		if !ok {
			t.Errorf("job %s is missing; jobs: %v", id, keys(jobs))
			continue
		}
		if got := imageOf(j); got != img {
			t.Errorf("job %s image = %q, want %q", id, got, img)
		}
	}
	for _, id := range []string{"API", "UI"} {
		if _, ok := jobs[id]; ok {
			t.Errorf("job %s should have been split", id)
		}
	}
	if got := strList(jobs["API-1"]["needs"]); !slices.Equal(got, []string{"baseimage"}) {
		t.Errorf("API-1 needs = %v, want the source needs", got)
	}
	for _, c := range []struct{ id, prev string }{{"API-2", "API-1"}, {"API-3", "API-2"}, {"UI-2", "UI-1"}, {"UI-3", "UI-2"}} {
		if got := needNames(jobs[c.id]["needs"]); !slices.Equal(got, []string{c.prev}) {
			t.Errorf("%s needs = %v, want [%s]", c.id, got, c.prev)
		}
	}
	for _, id := range []string{"API-1", "API-2", "UI-1", "UI-2"} {
		art, _ := jobs[id]["artifacts"].(map[string]any)
		if !slices.Contains(strList(art["paths"]), ".") || !slices.Contains(strList(art["exclude"]), ".git/**/*") || art["expire_in"] == nil {
			t.Errorf("%s does not pass the workspace on: artifacts = %v", id, jobs[id]["artifacts"])
		}
	}
	if jobs["API-3"]["artifacts"] != nil {
		t.Errorf("the last split job uploads the workspace: %v", jobs["API-3"]["artifacts"])
	}
	scripts := map[string][]string{
		"API-1": {"chown -R 1001:0 /drone/src"},
		"API-2": {"docker/parsr/build.sh", "npm run test"},
		"UI-2":  {"docker/parsr-ui/build.sh"},
	}
	for id, subs := range scripts {
		body := strings.Join(strList(jobs[id]["script"]), "\n")
		last := -1
		for _, s := range subs {
			i := strings.Index(body, s)
			if i < 0 || i < last {
				t.Errorf("%s script lacks %q in order:\n%s", id, s, body)
			}
			last = i
		}
	}
	if body := strings.Join(strList(jobs["API-1"]["script"]), "\n"); strings.Contains(body, "npm run test") {
		t.Errorf("API-1 runs a step of another image:\n%s", body)
	}
	// A job whose steps share one image is one job, with the image on the job.
	for _, id := range []string{"baseimage", "Sonarqube"} {
		if jobs[id]["artifacts"] != nil {
			t.Errorf("%s is not split but writes artifacts", id)
		}
	}
	for _, e := range rep.Entries {
		if e.Feature == caps.StepImage && e.Level == caps.Emulated && strings.Contains(e.IRPath, "job/API/step/Tests") && e.Detail == "" {
			t.Errorf("emulated entry for %s has no detail", e.IRPath)
		}
	}
}

func needNames(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, it := range l {
		switch x := it.(type) {
		case string:
			out = append(out, x)
		case map[string]any:
			s, _ := x["job"].(string)
			out = append(out, s)
		}
	}
	return out
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

const droneMultiImage = `kind: pipeline
name: ci
steps:
  - name: lint
    image: golang:1.22
    commands:
      - gofmt -l .
  - name: test
    image: golang:1.22
    commands:
      - go test ./...
  - name: docs
    image: node:20
    commands:
      - npm run docs
  - name: pack
    image: golang:1.22
    commands:
      - go build ./...
`

const woodpeckerMultiImage = `when:
  - event: push
steps:
  - name: lint
    image: golang:1.22
    commands:
      - gofmt -l .
  - name: test
    image: golang:1.22
    commands:
      - go test ./...
  - name: docs
    image: node:20
    commands:
      - npm run docs
  - name: pack
    image: golang:1.22
    commands:
      - go build ./...
`

func TestMultiImageJobBecomesChainedGitLabJobsAndBack(t *testing.T) {
	for _, c := range []struct{ from, name, doc string }{
		{"drone", ".drone.yml", droneMultiImage},
		{"woodpecker", ".woodpecker/ci.yaml", woodpeckerMultiImage},
	} {
		t.Run(c.from, func(t *testing.T) {
			files, rep := emitFiles(t, c.from, "gitlab", map[string]string{c.name: c.doc})
			if lost := lostImagePaths(rep); len(lost) > 0 {
				t.Errorf("step images lost: %v", lost)
			}
			jobs := gitlabJobs(t, files)
			var ids []string
			for id := range jobs {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			if len(ids) != 3 {
				t.Fatalf("want 3 chained jobs, got %v", ids)
			}
			dir := t.TempDir()
			full := filepath.Join(dir, c.name)
			if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(c.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, msg := runOut("roundtrip", dir, "--via", "gitlab")
			if code != 0 || !strings.Contains(out+msg, "no differences") {
				t.Fatalf("exit %d, the round trip is not exact\nstdout:\n%.3000s\nstderr:\n%.1500s", code, out, msg)
			}
		})
	}
}

const ghaSHA = "${{ github.sha }}"

const droneVarImage = `kind: pipeline
name: ci
steps:
  - name: build
    image: alpine:${DRONE_COMMIT_SHA}
    environment:
      MODE: fast
    commands:
      - echo "it's $MODE"
      - echo $(touch PWNED_SENTINEL)
      - |
        printf '%s\n' "multi
        line" ; touch PWNED_SENTINEL2
`

// ghaStep decodes the first step of the only job of an emitted GHA workflow.
func ghaStep(t *testing.T, files map[string][]byte) map[string]any {
	t.Helper()
	for n, b := range files {
		if !strings.HasPrefix(n, ".github/workflows/") {
			continue
		}
		var doc struct {
			Jobs map[string]struct {
				Steps []map[string]any `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		for _, j := range doc.Jobs {
			return j.Steps[0]
		}
	}
	t.Fatal("no workflow emitted")
	return nil
}

func TestGHAVariableImageRunsThroughDockerRun(t *testing.T) {
	files, rep := emitFiles(t, "drone", "gha", map[string]string{".drone.yml": droneVarImage})
	for _, e := range rep.Entries {
		if e.Feature == caps.StepImage && e.Level == caps.Lost {
			t.Errorf("the image is still lost: %s: %s", e.IRPath, e.Detail)
		}
	}
	st := ghaStep(t, files)
	run, _ := st["run"].(string)
	if !strings.Contains(run, "docker run --rm") || !strings.Contains(run, `-v "$GITHUB_WORKSPACE:$GITHUB_WORKSPACE"`) || !strings.Contains(run, `-w "$GITHUB_WORKSPACE"`) {
		t.Fatalf("the step does not run docker:\n%s", run)
	}
	if strings.Contains(run, "PWNED") || strings.Contains(run, "${{") {
		t.Fatalf("script text or an expression sits in the shell line:\n%s", run)
	}
	env, _ := st["env"].(map[string]any)
	if got, _ := env["PIPEMASTER_IMAGE"].(string); got != "alpine:"+ghaSHA {
		t.Errorf("image env = %q", got)
	}
	if !strings.Contains(run, "-e MODE") {
		t.Errorf("the step env is not passed to the container:\n%s", run)
	}
	var emulated bool
	for _, e := range rep.Entries {
		if e.Feature == caps.StepImage && e.Level == caps.Emulated && strings.Contains(e.Detail, "Docker") && strings.Contains(e.Detail, "user") {
			emulated = true
		}
	}
	if !emulated {
		t.Errorf("no emulated verdict names the Docker runner and the user difference: %v", rep.Entries)
	}
}

func TestGHAVariableImageScriptReachesTheContainerIntact(t *testing.T) {
	files, _ := emitFiles(t, "drone", "gha", map[string]string{".drone.yml": droneVarImage})
	st := ghaStep(t, files)
	run, _ := st["run"].(string)
	env, _ := st["env"].(map[string]any)
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\0' \"$a\"; done > \"$ARGV_OUT\"\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(stub), 0o700); err != nil { // #nosec G306 -- test stub must be executable
		t.Fatal(err)
	}
	argv := filepath.Join(dir, "argv")
	cmd := exec.Command("bash", "-ec", run) // #nosec G204 -- the emitted step under test
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "GITHUB_WORKSPACE=" + dir, "ARGV_OUT=" + argv, "MODE=fast"}
	for k, v := range env {
		s, _ := v.(string)
		cmd.Env = append(cmd.Env, k+"="+strings.ReplaceAll(s, ghaSHA, "main"))
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("step failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(argv) // #nosec G304 -- temp path
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	script := args[len(args)-1]
	for _, want := range []string{`echo "it's $MODE"`, "echo $(touch PWNED_SENTINEL)", "multi\nline\" ; touch PWNED_SENTINEL2"} {
		if !strings.Contains(script, want) {
			t.Errorf("the container script lost %q:\n%s", want, script)
		}
	}
	if !slices.Contains(args, "alpine:main") {
		t.Errorf("image argument missing: %q", args)
	}
	for _, f := range []string{"PWNED_SENTINEL", "PWNED_SENTINEL2"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s ran on the host", f)
		}
	}
}

func TestConcourseVariableImageSaysWhichImageRuns(t *testing.T) {
	_, rep := emitFiles(t, "drone", "concourse", map[string]string{".drone.yml": droneVarImage})
	for _, e := range rep.Entries {
		if e.Feature == caps.StepImage && e.Level == caps.Lost {
			if !strings.Contains(e.Detail, "script still runs") || !strings.Contains(e.Detail, "ubuntu") {
				t.Errorf("the verdict does not say the script runs in the wrong image: %s", e.Detail)
			}
			return
		}
	}
	t.Error("no lost step.image entry for the variable image")
}

func TestGHAVariableImageReturnsExactly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".drone.yml"), []byte(droneVarImage), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, msg := runOut("roundtrip", dir, "--via", "gha")
	if code != 0 || !strings.Contains(out+msg, "no differences") {
		t.Fatalf("exit %d\n%.3000s\n%.1500s", code, out, msg)
	}
}
