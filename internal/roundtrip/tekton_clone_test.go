package roundtrip_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider"
)

const cloneStepHead = `apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: p
spec:
  pipelineSpec:
    tasks:
      - name: build
        taskSpec:
          steps:
            - name: clone
              ref:
                resolver: git
                params:
                  - {name: url, value: https://github.com/tektoncd-catalog/git-clone.git}
                  - {name: revision, value: v1.7.1}
                  - {name: pathInRepo, value: stepaction/git-clone/git-clone.yaml}
              params:
`

func cloneFile(params ...string) string {
	var b strings.Builder
	b.WriteString(cloneStepHead)
	for _, p := range params {
		b.WriteString("                - " + p + "\n")
	}
	b.WriteString("            - {name: x, image: alpine, script: ls}\n")
	return b.String()
}

func emitClone(t *testing.T, to string, params ...string) (string, string) {
	t.Helper()
	proj, rep, err := prov(t, provider.Tekton).Parse(toFS(map[string][]byte{".tekton/p.yaml": []byte(cloneFile(params...))}))
	if err != nil {
		t.Fatal(err)
	}
	out, emitRep, err := prov(t, to).Emit(proj)
	if err != nil {
		t.Fatal(to, err)
	}
	rep.Merge(emitRep)
	var fb, rb strings.Builder
	for name, data := range out {
		if !strings.Contains(name, "meta") {
			fb.WriteString("# file " + name + "\n" + string(data) + "\n")
		}
	}
	for _, e := range rep.Entries {
		rb.WriteString(string(e.Level) + " " + e.IRPath + " " + string(e.Feature) + " " + e.Detail + "\n")
	}
	return fb.String(), rb.String()
}

// Each StepAction param of the catalog git-clone must reach the checkout, or the script step, on every target.
func TestTektonGitCloneParamsReachTheCheckout(t *testing.T) {
	url := "{name: url, value: https://github.com/o/r.git}"
	cases := []struct {
		want   map[string][]string
		name   string
		params []string
	}{
		{name: "url", params: []string{url}, want: map[string][]string{
			provider.GHA:    {"uses: actions/checkout@", "repository: o/r"},
			provider.GitLab: {`PIPEMASTER_CHECKOUT_REPOSITORY="o/r"`},
			provider.Drone:  {"PIPEMASTER_CHECKOUT_REPOSITORY: o/r"},
		}},
		{name: "revision", params: []string{url, "{name: revision, value: release-1}"}, want: map[string][]string{
			provider.GHA:    {"ref: release-1"},
			provider.GitLab: {`PIPEMASTER_CHECKOUT_REF="release-1"`},
			provider.Drone:  {"PIPEMASTER_CHECKOUT_REF: release-1"},
		}},
		{name: "subdirectory", params: []string{url, "{name: subdirectory, value: sub}"}, want: map[string][]string{
			provider.GHA:    {"path: sub"},
			provider.GitLab: {`PIPEMASTER_CHECKOUT_PATH="sub"`},
			provider.Drone:  {"PIPEMASTER_CHECKOUT_PATH: sub"},
		}},
		{name: "output-path", params: []string{url, "{name: output-path, value: /workspace/out}"}, want: map[string][]string{
			provider.GHA:    {"path: out"},
			provider.GitLab: {`PIPEMASTER_CHECKOUT_PATH="out"`},
			provider.Drone:  {"PIPEMASTER_CHECKOUT_PATH: out"},
		}},
		{name: "sparseCheckoutDirectories", params: []string{url, "{name: sparseCheckoutDirectories, value: docs}"}, want: map[string][]string{
			provider.GHA:    {"sparse-checkout: docs"},
			provider.GitLab: {`PIPEMASTER_CHECKOUT_SPARSE="docs"`},
			provider.Drone:  {"PIPEMASTER_CHECKOUT_SPARSE: docs"},
		}},
		{name: "sparse list", params: []string{url, "{name: sparseCheckoutDirectories, value: 'docs,cmd'}"}, want: map[string][]string{
			provider.GHA: {"sparse-checkout:", "docs", "cmd"},
		}},
		{name: "depth", params: []string{url, "{name: depth, value: '5'}"}, want: map[string][]string{
			provider.GHA:    {`fetch-depth: "5"`},
			provider.GitLab: {`GIT_DEPTH: "5"`},
			provider.Drone:  {"depth: 5"},
		}},
		{name: "submodules", params: []string{url, "{name: submodules, value: 'false'}"}, want: map[string][]string{
			provider.GHA:    {`submodules: "false"`},
			provider.GitLab: {"GIT_SUBMODULE_STRATEGY: none"},
			provider.Drone:  {"pipemaster: lost"},
		}},
		{name: "own repository", params: []string{"{name: url, value: 'https://github.com/$(params.ctx-repo).git'}", "{name: revision, value: $(params.ctx-sha)}"}, want: map[string][]string{
			provider.GHA:    {"uses: actions/checkout@"},
			provider.GitLab: {"pipemaster:step clone"},
		}},
	}
	for _, c := range cases {
		for to, want := range c.want {
			t.Run(c.name+"/"+to, func(t *testing.T) {
				files, rep := emitClone(t, to, c.params...)
				for _, w := range want {
					if !strings.Contains(files, w) {
						t.Errorf("%s output lacks %q\n%s\n%s", to, w, files, rep)
					}
				}
				if strings.Contains(files, "step kind action has no") {
					t.Errorf("%s still writes a failing step\n%s", to, files)
				}
			})
		}
	}
}

// A param with no checkout spelling keeps the loud failing step and the report names the param.
func TestTektonGitCloneParamWithoutSpellingFailsLoudly(t *testing.T) {
	for _, to := range []string{provider.GHA, provider.GitLab, provider.Drone} {
		files, rep := emitClone(t, to, "{name: url, value: https://github.com/o/r.git}", "{name: refspec, value: refs/pull/1/head}")
		if !strings.Contains(rep, "param refspec of the git-clone StepAction") {
			t.Errorf("%s: the report does not name refspec\n%s", to, rep)
		}
		if to != provider.GHA && !strings.Contains(files, "pipemaster: lost") {
			t.Errorf("%s: no failing step\n%s", to, files)
		}
	}
	proj, rep, err := prov(t, provider.Tekton).Parse(toFS(map[string][]byte{".tekton/p.yaml": []byte(cloneFile("{name: url, value: https://gitlab.com/o/r.git}"))}))
	if err != nil {
		t.Fatal(err)
	}
	lost := false
	for _, e := range rep.Entries {
		lost = lost || e.Level == caps.Lost && strings.Contains(e.Detail, "param url of the git-clone StepAction")
	}
	if !lost || proj == nil {
		t.Errorf("a url outside github.com must be reported: %+v", rep.Entries)
	}
}

// Tekton to Tekton keeps the StepAction ref and every param as written.
func TestTektonGitCloneStaysExactOnTekton(t *testing.T) {
	files, _ := emitClone(t, provider.Tekton,
		"{name: url, value: https://github.com/o/r.git}", "{name: refspec, value: refs/pull/1/head}",
		"{name: subdirectory, value: sub}", "{name: sparseCheckoutDirectories, value: docs}", "{name: sslVerify, value: 'false'}")
	for _, w := range []string{"refspec", "refs/pull/1/head", "subdirectory", "sub", "sparseCheckoutDirectories", "sslVerify", "stepaction/git-clone/git-clone.yaml"} {
		if !strings.Contains(files, w) {
			t.Errorf("tekton output lacks %q\n%s", w, files)
		}
	}
}
