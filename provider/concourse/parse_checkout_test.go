package concourse_test

import (
	"slices"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
)

func TestRepositoryGetAsksForACheckout(t *testing.T) {
	tests := []struct {
		wantDepth any
		name      string
		get       string
	}{
		{name: "plain get", get: "{get: repo, trigger: true}"},
		{name: "depth param", get: "{get: repo, trigger: true, params: {depth: 1}}", wantDepth: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			y := gitRepo + "jobs:\n- name: build\n  plan:\n  - " + tc.get + "\n" + verdictTask
			pl, _ := parseYAML(t, y)
			ext, _ := pl.Jobs[0].Ext["concourse"].(map[string]any)
			v, has := ext["_checkout"]
			if !has {
				t.Fatal("_checkout missing")
			}
			m, _ := v.(map[string]any)
			if got := m["depth"]; got != tc.wantDepth {
				t.Errorf("depth = %v, want %v", got, tc.wantDepth)
			}
		})
	}
}

func TestJobWithoutSourceGetHasNoCheckoutKey(t *testing.T) {
	y := "resources:\n- name: t\n  type: time\n  source: {interval: 1h}\njobs:\n- name: build\n  plan:\n  - {get: t, trigger: true}\n" + verdictTask
	pl, _ := parseYAML(t, y)
	ext, _ := pl.Jobs[0].Ext["concourse"].(map[string]any)
	if _, has := ext["_checkout"]; has {
		t.Error("a time resource is no repository")
	}
}

// ownRepo names the repository of the pipeline with a ((var)) uri, as a pipeline with two uris needs.
const ownRepo = "resources:\n- name: repo\n  type: git\n  source: {uri: ((git-uri)), branch: main}\n"

func TestOtherRepositoryInputIsCloned(t *testing.T) {
	task := "platform: linux\nimage_resource: {type: registry-image, source: {repository: alpine}}\ninputs: [{name: lib}]\nrun: {path: sh, args: [-ec, 'ls lib']}\n"
	pipe := func(res, get string) map[string]string {
		return map[string]string{
			"ci.yml": ownRepo + res + "jobs:\n- name: build\n  plan:\n  - {get: repo, trigger: true}\n  - " + get + "\n  - {task: t, file: repo/.concourse/t.yml}\n",
			"t.yml":  task,
		}
	}
	res := "- name: lib\n  type: git\n  source: {uri: 'https://example.com/lib.git', branch: dev}\n"
	t.Run("clones", func(t *testing.T) {
		p, rep := parseFiles(t, pipe(res, "{get: lib, params: {depth: 1}}"))
		steps := p.Pipelines[0].Jobs[0].Steps
		want := "[ -e lib ] || git clone --depth 1 --branch 'dev' -- 'https://example.com/lib.git' lib"
		if len(steps) == 0 || !slices.Contains(steps[0].Script, want) {
			t.Fatalf("script = %q, want the line %q", steps[0].Script, want)
		}
		if got := entriesAt(rep, caps.Lost, "input lib"); len(got) != 0 {
			t.Errorf("a cloned input is no loss: %+v", got)
		}
	})
	t.Run("credentials stay lost", func(t *testing.T) {
		withKey := "- name: lib\n  type: git\n  source: {uri: 'https://example.com/lib.git', private_key: ((k))}\n"
		_, rep := parseFiles(t, pipe(withKey, "{get: lib}"))
		if got := entriesAt(rep, caps.Lost, "input lib"); len(got) != 1 {
			t.Errorf("a source with credentials has no clone form: %+v", rep.Entries)
		}
	})
}
