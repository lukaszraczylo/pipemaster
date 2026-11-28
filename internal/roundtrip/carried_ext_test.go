package roundtrip_test

import (
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

var lostExtRE = regexp.MustCompile(`^Ext of provider (\w+) \(([^)]*)\) is not written`)

// lostExtKeys parses fsys with the source provider, emits it for the target, and returns the Ext keys that the
// report names as lost, with the Ext keys that the parsed IR holds. mutate can edit the IR before the emit.
func lostExtKeys(t *testing.T, source string, fsys fs.FS, target string, mutate func(*ir.Project)) (lost []string, held map[string]bool) {
	t.Helper()
	proj, _, err := prov(t, source).Parse(fsys)
	if err != nil {
		t.Fatal(err)
	}
	held = map[string]bool{}
	for _, pl := range proj.Pipelines {
		collectExtKeys(source, pl.Ext, held)
		for _, j := range pl.Jobs {
			collectExtKeys(source, j.Ext, held)
			for _, st := range j.Steps {
				collectExtKeys(source, st.Ext, held)
			}
		}
	}
	if mutate != nil {
		mutate(proj)
	}
	_, rep, err := prov(t, target).Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Entries {
		if e.Feature != caps.ExtPassthrough || e.Level != caps.Lost {
			continue
		}
		if m := lostExtRE.FindStringSubmatch(e.Detail); m != nil && m[1] == source {
			lost = append(lost, strings.Split(m[2], ", ")...)
		}
	}
	return lost, held
}

func collectExtKeys(source string, x ir.Ext, into map[string]bool) {
	m, _ := x[source].(map[string]any)
	for k := range m {
		into[k] = true
	}
}

// markUncarried adds a key that has no IR field to every job, so the report must still name it.
func markUncarried(source string) func(*ir.Project) {
	return func(p *ir.Project) {
		for _, pl := range p.Pipelines {
			for _, j := range pl.Jobs {
				if j.Ext == nil {
					j.Ext = ir.Ext{}
				}
				m, _ := j.Ext[source].(map[string]any)
				if m == nil {
					m = map[string]any{}
					j.Ext[source] = m
				}
				m["no_ir_field"] = true
			}
		}
	}
}

func requireHeld(t *testing.T, held map[string]bool, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if !held[k] {
			t.Fatalf("fixture lacks Ext key %q (held: %v)", k, held)
		}
	}
}

func requireNotLost(t *testing.T, lost []string, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if slices.Contains(lost, k) {
			t.Errorf("Ext key %q is reported lost, but the IR carries it: %v", k, lost)
		}
	}
}

func requireLost(t *testing.T, lost []string, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if !slices.Contains(lost, k) {
			t.Errorf("Ext key %q has no IR field and must stay lost: %v", k, lost)
		}
	}
}

const gitlabCarriedSrc = `.base:
  script: [echo base]
build:
  extends: .base
  rules:
    - changes: [a.go]
  cache:
    key: {files: [go.sum], prefix: p}
    paths: [x/]
  coverage: '/\d+%/'
`

func TestCarriedExtGitLab(t *testing.T) {
	fsys := fstest.MapFS{".gitlab-ci.yml": {Data: []byte(gitlabCarriedSrc)}}
	for _, target := range []string{provider.GHA, provider.Tekton} {
		t.Run(target, func(t *testing.T) {
			lost, held := lostExtKeys(t, provider.GitLab, fsys, target, nil)
			requireHeld(t, held, "extends", "rules", "cache", "hidden", "coverage")
			requireNotLost(t, lost, "extends", "rules", "cache", "hidden")
			requireLost(t, lost, "coverage")
		})
	}
}

func TestCarriedExtGitLabKeepsPartialCacheLost(t *testing.T) {
	src := "build:\n  script: [echo]\n  cache:\n    key: {files: [go.sum]}\n    paths: [x/]\n    untracked: true\n"
	lost, held := lostExtKeys(t, provider.GitLab, fstest.MapFS{".gitlab-ci.yml": {Data: []byte(src)}}, provider.GHA, nil)
	requireHeld(t, held, "cache")
	requireLost(t, lost, "cache")
}

func TestCarriedExtTekton(t *testing.T) {
	fsys := os.DirFS("../../testdata/behave/tekton/workspace")
	for _, target := range []string{provider.Concourse, provider.GHA} {
		t.Run(target, func(t *testing.T) {
			lost, held := lostExtKeys(t, provider.Tekton, fsys, target, markUncarried(provider.Tekton))
			requireHeld(t, held, "workspaces")
			requireNotLost(t, lost, "workspaces", "task_workspaces", "workspace_artifacts", "workspace_dirs", "source_checkout")
			requireLost(t, lost, "no_ir_field")
		})
	}
}

func TestCarriedExtConcourse(t *testing.T) {
	fsys := os.DirFS("../../testdata/behave/concourse/outputs_loadvar")
	lost, held := lostExtKeys(t, provider.Concourse, fsys, provider.GHA, markUncarried(provider.Concourse))
	requireHeld(t, held, "kind", "step")
	requireLost(t, lost, "no_ir_field")
}

const argoCarriedSrc = `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata: {name: ci}
spec:
  entrypoint: main
  templates:
  - name: main
    volumes:
    - {name: data, emptyDir: {}}
    container:
      image: alpine
      command: [sh, -c]
      args: ["echo hi > /a/x"]
      volumeMounts:
      - {name: data, mountPath: /a}
`

func TestCarriedExtArgo(t *testing.T) {
	fsys := fstest.MapFS{".argo/ci.yaml": {Data: []byte(argoCarriedSrc)}}
	lost, held := lostExtKeys(t, provider.Argo, fsys, provider.GHA, markUncarried(provider.Argo))
	requireHeld(t, held, "mount_paths", "workspace_dirs")
	requireNotLost(t, lost, "mount_paths", "workspace_dirs")
	requireLost(t, lost, "no_ir_field")
}
