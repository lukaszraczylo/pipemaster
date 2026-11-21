package drone_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/drone"
)

func TestDetect(t *testing.T) {
	d := drone.New()
	for name, tc := range map[string]struct {
		files map[string]string
		want  bool
	}{
		"yml":            {map[string]string{".drone.yml": minimal}, true},
		"jsonnet alone":  {map[string]string{".drone.jsonnet": "{}"}, false},
		"star alone":     {map[string]string{".drone.star": "def main(ctx): pass"}, false},
		"yml and star":   {map[string]string{".drone.yml": minimal, ".drone.star": "x"}, true},
		"empty repo":     {map[string]string{}, false},
		"other provider": {map[string]string{".woodpecker.yaml": minimal}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := d.Detect(repo(tc.files)); got != tc.want {
				t.Fatalf("Detect = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestJsonnetAloneIsUnsupported(t *testing.T) {
	_, rep, err := drone.New().Parse(repo(map[string]string{".drone.jsonnet": "{}"}))
	if err == nil || !strings.Contains(err.Error(), ".drone.jsonnet") {
		t.Fatalf("err = %v, want one that names .drone.jsonnet", err)
	}
	if rep == nil || !hasEntry(rep, ".drone.jsonnet is a generator file") {
		t.Fatalf("report = %+v", rep)
	}
}

func TestGeneratorFileNextToYMLIsReported(t *testing.T) {
	_, rep, err := drone.New().Parse(repo(map[string]string{".drone.yml": minimal, ".drone.star": "x", ".drone.jsonnet": "{}"}))
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntry(rep, ".drone.star is a generator file") || !hasEntry(rep, ".drone.jsonnet is a generator file") {
		t.Fatalf("entries = %+v", rep.Entries)
	}
}

func TestSourceFiles(t *testing.T) {
	l, ok := drone.New().(provider.Layout)
	if !ok {
		t.Fatal("the provider must implement Layout")
	}
	got, err := l.SourceFiles(repo(map[string]string{".drone.yml": minimal}))
	if err != nil || !reflect.DeepEqual(got, []string{".drone.yml"}) {
		t.Fatalf("SourceFiles = %v, %v", got, err)
	}
	if got, _ := l.SourceFiles(repo(nil)); len(got) != 0 {
		t.Fatalf("SourceFiles of an empty repo = %v", got)
	}
}

func TestSecretAndTemplateKeepExtWithEntry(t *testing.T) {
	yml := minimal + "---\nkind: secret\nname: tok\nget:\n  path: secret/data/x\n  name: token\n---\nkind: template\nload: t.yaml\n"
	pl, rep := parseYML(t, yml)
	res, _ := ext(pl.Ext)["resources"].([]any)
	if len(res) != 2 {
		t.Fatalf("resources = %v", pl.Ext)
	}
	first, _ := res[0].(map[string]any)
	if first["kind"] != "secret" || first["name"] != "tok" {
		t.Fatalf("first resource = %v", first)
	}
	if !hasEntry(rep, "kind: secret is not a pipeline") || !hasEntry(rep, "kind: template is not a pipeline") {
		t.Fatalf("entries = %+v", rep.Entries)
	}
	if len(pl.Jobs) != 1 {
		t.Fatalf("jobs = %d, want only the pipeline", len(pl.Jobs))
	}
}

func TestSignatureIsDroppedWithEntry(t *testing.T) {
	pl, rep := parseYML(t, minimal+"---\nkind: signature\nhmac: deadbeef\n")
	if pl.Ext != nil || len(pl.Jobs) != 1 {
		t.Fatalf("pipeline = %+v", pl)
	}
	if !hasEntry(rep, "kind: signature") {
		t.Fatal("a dropped signature needs an entry")
	}
}

func TestEntryLinesNameFileLines(t *testing.T) {
	_, rep := parseYML(t, minimal+"---\nkind: pipeline\nname: b\nclone:\n  depth: 1\nsteps:\n- {name: s, image: x}\n")
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "key clone") {
			if e.SourceLoc != ".drone.yml:11" {
				t.Fatalf("SourceLoc = %q, want .drone.yml:11", e.SourceLoc)
			}
			return
		}
	}
	t.Fatal("no entry for clone")
}

func TestRepeatedKeyIsReportedNotFatal(t *testing.T) {
	j, rep := parseJob(t, "kind: pipeline\nname: p\nsteps:\n- {name: a, image: x}\nenvironment: {A: 1}\nenvironment: {A: 2}\n")
	if j.Env[0].Value.Lit != "2" || !hasEntry(rep, "key environment is defined twice") {
		t.Fatalf("env = %+v", j.Env)
	}
}

func TestIDRules(t *testing.T) {
	r, ok := drone.New().(provider.IDRules)
	if !ok {
		t.Fatal("the provider must implement IDRules")
	}
	if got := r.SanitizeID("build & test/linux"); got != "build---test-linux" {
		t.Fatalf("SanitizeID = %q", got)
	}
	if got := r.SanitizeID(strings.Repeat("a", 150)); len(got) != 100 {
		t.Fatalf("len = %d, want 100", len(got))
	}
	if got := r.SanitizeID(""); got != "default" {
		t.Fatalf("SanitizeID(empty) = %q", got)
	}
}
