package roundtrip_test

import (
	"io/fs"
	"os"
	"strings"
	"testing"
)

const paramsPipeline = `resources:
  - name: repo
    type: git
    source: {uri: https://example.com/r.git, branch: main}
jobs:
  - name: build
    plan:
      - get: repo
        trigger: true
      - task: unit
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: golang}}
          inputs: [{name: repo}]
          params:
            REGION: ((region))
            GCP_SA: ((gcp_service_account_json))
          run: {path: sh, args: [-ec, "echo $REGION"]}
`

// treeText joins every file under root.
func treeText(t *testing.T, root string) string {
	t.Helper()
	var sb strings.Builder
	fsys := os.DirFS(root)
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := fs.ReadFile(fsys, p)
		sb.WriteString(string(b))
		return rerr
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

// Concourse reads every ((name)) from the credential manager, so no target may write one as a plain value.
func TestConcourseVarsAreSecretsInEveryTarget(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, ".concourse/ci.yml", paramsPipeline)
	for _, c := range []struct {
		target string
		want   []string
		deny   []string
	}{
		{"gha", []string{"secrets.region", "secrets.gcp_service_account_json"}, []string{"vars.region", "expression has no translation"}},
		{"tekton", []string{"secretKeyRef"}, []string{"configMapKeyRef"}},
		{"argo", []string{"secretKeyRef"}, []string{"configMapKeyRef"}},
	} {
		out := t.TempDir()
		if code, msg := runCLI("convert", src, "--from", "concourse", "--to", c.target, "-o", out); code != 0 {
			t.Fatalf("convert to %s = %d\n%s", c.target, code, msg)
		}
		got := treeText(t, out)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s output lacks %q:\n%s", c.target, w, got)
			}
		}
		for _, d := range c.deny {
			if strings.Contains(got, d) {
				t.Errorf("%s output holds %q:\n%s", c.target, d, got)
			}
		}
	}
}
