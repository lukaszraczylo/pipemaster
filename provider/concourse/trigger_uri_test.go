package concourse_test

import (
	"os"
	"slices"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/concourse"
)

func twoRepos(uriA, uriB string) string {
	return "resources:\n- {name: app, type: git, source: {uri: " + uriA + ", branch: main}}\n" +
		"- {name: lib, type: git, source: {uri: " + uriB + ", branch: dev}}\n" +
		"jobs:\n- name: j\n  plan:\n  - {get: app, trigger: true}\n  - {get: lib, trigger: true}\n" + echoTask
}

func triggerKinds(pl *ir.Pipeline) []ir.TriggerKind {
	var out []ir.TriggerKind
	for _, t := range pl.Triggers {
		out = append(out, t.Kind)
	}
	return out
}

func TestOtherRepositoryTriggerNeverFires(t *testing.T) {
	pl, rep := parseYAML(t, twoRepos("https://example.com/app.git", "https://example.com/lib.git"))
	for _, tr := range pl.Triggers {
		if tr.Kind == ir.TriggerPush {
			t.Fatalf("a push trigger of another repository was kept: %+v", pl.Triggers)
		}
	}
	for _, uri := range []string{"https://example.com/app.git", "https://example.com/lib.git"} {
		if !hasEntry(rep, uri) {
			t.Errorf("no entry names %s: %v", uri, rep.Entries)
		}
	}
}

func TestVarUriIsTheConvertedRepository(t *testing.T) {
	pl, rep := parseYAML(t, twoRepos("((git-uri))", "https://example.com/lib.git"))
	kinds := triggerKinds(pl)
	if len(kinds) != 2 || !slices.Contains(kinds, ir.TriggerPush) || !slices.Contains(kinds, ir.TriggerAPI) {
		t.Fatalf("trigger kinds = %v", kinds)
	}
	if !hasFeatureEntry(rep, "trigger.push", "((git-uri))") || !hasEntry(rep, "https://example.com/lib.git") {
		t.Fatalf("the entries do not name the watched uris: %v", rep.Entries)
	}
}

func TestOnlyGitUriIsTheConvertedRepository(t *testing.T) {
	pl, rep := parseYAML(t, twoRepos("https://example.com/app.git", "https://example.com/app.git"))
	if len(pl.Triggers) != 1 || pl.Triggers[0].Kind != ir.TriggerPush {
		t.Fatalf("triggers = %+v", pl.Triggers)
	}
	if !hasFeatureEntry(rep, "trigger.push", "https://example.com/app.git") {
		t.Fatalf("the trigger entry does not name the uri: %v", rep.Entries)
	}
}

func TestCorpusCapiTriggersNameTheirRepository(t *testing.T) {
	const dir = "../../testdata/corpus/86-concourse-capi-ci"
	p, rep, err := concourse.New().Parse(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, pl := range p.Pipelines {
		for _, tr := range pl.Triggers {
			if tr.Kind == ir.TriggerPush {
				t.Fatalf("pipeline %s keeps a push trigger of another repository: %+v", pl.ID, tr)
			}
		}
	}
	if !hasEntry(rep, "https://github.com/cloudfoundry/capi-release.git") {
		t.Fatalf("no entry names the capi-release uri")
	}
}
