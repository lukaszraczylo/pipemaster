package concourse_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
)

const verdictTask = "  - task: t\n    config: {platform: linux, image_resource: {type: registry-image, source: {repository: alpine}}, run: {path: sh, args: [-ec, ls]}}\n"

func TestSourceGetAndResourceCarryNoLoss(t *testing.T) {
	_, rep := parseYAML(t, oneJob(verdictTask))
	for _, e := range rep.Entries {
		if e.Level == caps.Lost {
			t.Errorf("a plain source get and resource lose nothing: %+v", e)
		}
	}
}

func TestSourceGetExtraKeysStayLost(t *testing.T) {
	y := gitRepo + "jobs:\n- name: build\n  plan:\n  - {get: repo, trigger: true, params: {depth: 1}}\n" + verdictTask
	_, rep := parseYAML(t, y)
	if got := entriesAt(rep, caps.Lost, "get repo: keys params"); len(got) != 1 {
		t.Errorf("want one lost entry for the get params: %+v", rep.Entries)
	}
}

func TestResourceCheckEveryIsExternal(t *testing.T) {
	y := "resources:\n- name: repo\n  type: git\n  check_every: 5m\n  source: {uri: https://example.com/r.git, branch: main}\n" +
		"jobs:\n- name: build\n  plan:\n  - {get: repo, trigger: true}\n" + verdictTask
	_, rep := parseYAML(t, y)
	if got := entriesAt(rep, caps.External, "check_every 5m"); len(got) != 1 {
		t.Errorf("want one external entry for check_every: %+v", rep.Entries)
	}
	if got := entriesAt(rep, caps.Lost, "resource repo"); len(got) != 0 {
		t.Errorf("check_every is no loss: %+v", got)
	}
}

func TestResourceUnknownSourceKeyStaysLost(t *testing.T) {
	y := "resources:\n- name: repo\n  type: git\n  source: {uri: https://example.com/r.git, branch: main, private_key: ((k))}\n" +
		"jobs:\n- name: build\n  plan:\n  - {get: repo, trigger: true}\n" + verdictTask
	_, rep := parseYAML(t, y)
	if got := entriesAt(rep, caps.Lost, "resource repo (type git): keys source"); len(got) != 1 {
		t.Errorf("a source key that no target runs stays lost: %+v", rep.Entries)
	}
}

func TestJobPublicIsExternal(t *testing.T) {
	y := gitRepo + "jobs:\n- name: build\n  public: true\n  plan:\n  - {get: repo, trigger: true}\n" + verdictTask
	pl, rep := parseYAML(t, y)
	if got := entriesAt(rep, caps.External, "public makes the builds visible"); len(got) != 1 {
		t.Errorf("want one external entry for public: %+v", rep.Entries)
	}
	if got := entriesAt(rep, caps.Lost, "public"); len(got) != 0 {
		t.Errorf("public is no loss: %+v", got)
	}
	ext, _ := pl.Jobs[0].Ext["concourse"].(map[string]any)
	if _, kept := ext["public"]; !kept {
		t.Error("public must stay in Ext for the round trip")
	}
}

func TestTaskFilePlatformAndIO(t *testing.T) {
	file := func(platform string) map[string]string {
		return map[string]string{
			"ci.yml": gitRepo + "jobs:\n- name: build\n  plan:\n  - {get: repo, trigger: true}\n  - {task: t, file: repo/.concourse/t.yml}\n",
			"t.yml":  "platform: " + platform + "\nimage_resource: {type: registry-image, source: {repository: alpine}}\ninputs: [{name: repo}]\noutputs: [{name: out}]\nrun: {path: sh, args: [-ec, 'echo 1 > out/x']}\n",
		}
	}
	t.Run("linux", func(t *testing.T) {
		_, rep := parseFiles(t, file("linux"))
		for _, e := range rep.Entries {
			if e.Level == caps.Lost || e.Level == caps.External {
				t.Errorf("a linux task file with inputs and outputs loses nothing: %+v", e)
			}
		}
	})
	t.Run("darwin", func(t *testing.T) {
		_, rep := parseFiles(t, file("darwin"))
		if got := entriesAt(rep, caps.External, "platform darwin"); len(got) != 1 {
			t.Errorf("want one external entry for platform darwin: %+v", rep.Entries)
		}
		if got := entriesAt(rep, caps.Lost, "platform"); len(got) != 0 {
			t.Errorf("platform is no loss: %+v", got)
		}
	})
}

func TestInlinePlatformDarwinIsExternal(t *testing.T) {
	_, rep := parseYAML(t, oneJob("  - task: t\n    config: {platform: darwin, image_resource: {type: registry-image, source: {repository: alpine}}, run: {path: sh, args: [-ec, ls]}}\n"))
	if got := entriesAt(rep, caps.External, "platform darwin"); len(got) != 1 {
		t.Errorf("want one external entry: %+v", rep.Entries)
	}
}
