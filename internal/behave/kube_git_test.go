//go:build behave

package behave

import (
	"strings"
	"testing"
)

func cloneDocs() []map[string]any {
	return []map[string]any{{
		"spec": map[string]any{"params": []any{
			map[string]any{"name": "url", "value": "https://github.com/$(params.ctx-repo).git"},
			map[string]any{"name": "catalog", "value": "https://github.com/tektoncd-catalog/git-clone.git"},
		}},
		"repo": "https://github.com/{{workflow.parameters.ctx-repo}}.git",
	}}
}

func TestClonesGitHub(t *testing.T) {
	tests := []struct {
		name string
		docs []map[string]any
		want bool
	}{
		{"clone of the pipeline repository", cloneDocs(), true},
		{"catalog only", []map[string]any{{"url": "https://github.com/tektoncd-catalog/git-clone.git"}}, false},
		{"script only", []map[string]any{{"script": "echo hi"}}, false},
		{"none", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clonesGitHub(tc.docs); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRewriteCloneKeepsTheCatalog(t *testing.T) {
	docs := cloneDocs()
	rewriteClone(docs, gitSrvBase("ns1"))
	params := docs[0]["spec"].(map[string]any)["params"].([]any)
	if got := params[0].(map[string]any)["value"]; got != "git://gitsrv.ns1.svc.cluster.local/$(params.ctx-repo).git" {
		t.Errorf("clone url = %v", got)
	}
	if got := params[1].(map[string]any)["value"]; got != "https://github.com/tektoncd-catalog/git-clone.git" {
		t.Errorf("the catalog url must stay: %v", got)
	}
	if got := docs[0]["repo"].(string); !strings.HasPrefix(got, "git://gitsrv.ns1.svc.cluster.local/") {
		t.Errorf("argo repo = %v", got)
	}
	if clonesGitHub(docs) {
		t.Error("no clone from GitHub may remain")
	}
}

func TestCloneInContainerSet(t *testing.T) {
	build := func(artPath string) []map[string]any {
		return []map[string]any{{"spec": map[string]any{"templates": []any{map[string]any{
			"name": "build",
			"containerSet": map[string]any{
				"volumeMounts": []any{map[string]any{"name": "workspace", "mountPath": "/workspace"}},
				"containers": []any{
					map[string]any{"name": "a"},
					map[string]any{"name": "b", "dependencies": []any{"a"}},
				},
			},
			"inputs": map[string]any{"artifacts": []any{map[string]any{
				"name": "source", "path": artPath, "git": map[string]any{"repo": "git://h/r.git", "revision": "abc"},
			}}},
		}}}}}
	}
	t.Run("artifact on the mount", func(t *testing.T) {
		docs := build("/workspace")
		cloneInContainerSet(docs)
		tpl := docs[0]["spec"].(map[string]any)["templates"].([]any)[0].(map[string]any)
		if _, has := tpl["inputs"].(map[string]any)["artifacts"]; has {
			t.Error("the git artifact must be removed")
		}
		cs := list(tpl["containerSet"].(map[string]any)["containers"])
		if len(cs) != 3 || cs[0]["name"] != cloneContainer {
			t.Fatalf("containers = %v", cs)
		}
		if !strings.Contains(cs[0]["args"].([]any)[0].(string), "git clone -q 'git://h/r.git'") {
			t.Errorf("clone args = %v", cs[0]["args"])
		}
		if dep := cs[1]["dependencies"].([]any); len(dep) != 1 || dep[0] != cloneContainer {
			t.Errorf("a root container must wait for the clone: %v", dep)
		}
		if dep := cs[2]["dependencies"].([]any); len(dep) != 1 || dep[0] != "a" {
			t.Errorf("a dependent container keeps its dependencies: %v", dep)
		}
	})
	t.Run("artifact below the mount stays", func(t *testing.T) {
		docs := build("/workspace/src")
		cloneInContainerSet(docs)
		tpl := docs[0]["spec"].(map[string]any)["templates"].([]any)[0].(map[string]any)
		if _, has := tpl["inputs"].(map[string]any)["artifacts"]; !has {
			t.Error("an artifact off the mount path must stay")
		}
	})
}
