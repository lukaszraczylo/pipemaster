package gha_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
)

const dialectCI = "on: push\njobs:\n  x:\n    runs-on: r\n    steps:\n      - run: echo hi\n"

func dialectFS(dirs ...string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for _, d := range dirs {
		fsys[d+"/ci.yml"] = &fstest.MapFile{Data: []byte(dialectCI)}
	}
	return fsys
}

func TestDialectDirs(t *testing.T) {
	for id, want := range map[string]string{
		provider.GHA:     ".github/workflows/ci.yml",
		provider.Gitea:   ".gitea/workflows/ci.yml",
		provider.Forgejo: ".forgejo/workflows/ci.yml",
	} {
		src, _, err := gha.New(gha.GHA).Parse(dialectFS(".github/workflows"))
		if err != nil {
			t.Fatal(err)
		}
		p, err := provider.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		files, _, err := p.Emit(src)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := files[want]; !ok || len(files) != 1 {
			t.Errorf("%s: want only %s, got %v", id, want, reflect.ValueOf(files).MapKeys())
		}
	}
}

func TestDetectOwnDirOnly(t *testing.T) {
	cases := []struct {
		dirs []string
		want []string
	}{
		{[]string{".github/workflows"}, []string{"gha"}},
		{[]string{".gitea/workflows"}, []string{"gitea"}},
		{[]string{".forgejo/workflows"}, []string{"forgejo"}},
		{[]string{".github/workflows", ".gitea/workflows"}, []string{"gha", "gitea"}},
	}
	for _, c := range cases {
		var got []string
		for _, id := range []string{provider.GHA, provider.Gitea, provider.Forgejo} {
			p, err := provider.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if p.Detect(dialectFS(c.dirs...)) {
				got = append(got, id)
			}
		}
		sort.Strings(got)
		sort.Strings(c.want)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: detected %v, want %v", c.dirs, got, c.want)
		}
	}
}

func TestParseFirstExistingDir(t *testing.T) {
	fsys := fstest.MapFS{
		".gitea/workflows/a.yml":  &fstest.MapFile{Data: []byte(dialectCI)},
		".github/workflows/b.yml": &fstest.MapFile{Data: []byte(dialectCI)},
	}
	p, err := provider.Get(provider.Forgejo)
	if err != nil {
		t.Fatal(err)
	}
	proj, _, err := p.Parse(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(proj.Pipelines) != 1 || proj.Pipelines[0].ID != "a" {
		t.Fatalf("pipelines %+v", proj.Pipelines)
	}
}

func TestParseNoFilesNamesEveryDir(t *testing.T) {
	p, _ := provider.Get(provider.Forgejo)
	_, _, err := p.Parse(fstest.MapFS{})
	if err == nil {
		t.Fatal("want error")
	}
	for _, d := range []string{".forgejo/workflows", ".gitea/workflows", ".github/workflows"} {
		if !strings.Contains(err.Error(), d) {
			t.Errorf("error lacks %s: %v", d, err)
		}
	}
}

func TestExtFamilyKey(t *testing.T) {
	gitea, _ := provider.Get(provider.Gitea)
	proj, _, err := gitea.Parse(fstest.MapFS{".gitea/workflows/ci.yml": &fstest.MapFile{
		Data: []byte("on: push\njobs:\n  x:\n    runs-on: r\n    foo: bar\n    steps:\n      - run: echo\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := proj.Pipelines[0].Jobs[0].Ext["gha"].(map[string]any); got["foo"] != "bar" {
		t.Fatalf("gitea job ext %#v", proj.Pipelines[0].Jobs[0].Ext)
	}
	forgejo, _ := provider.Get(provider.Forgejo)
	proj, _, err = forgejo.Parse(fstest.MapFS{".forgejo/workflows/ci.yml": &fstest.MapFile{
		Data: []byte("on: push\nenable-email-notifications: true\nunknown: 1\njobs:\n  x:\n    runs-on: r\n    steps:\n      - run: echo\n")}})
	if err != nil {
		t.Fatal(err)
	}
	ext := proj.Pipelines[0].Ext
	if f, _ := ext["forgejo"].(map[string]any); f["enable-email-notifications"] != true || len(f) != 1 {
		t.Fatalf("forgejo ext %#v", ext)
	}
	if g, _ := ext["gha"].(map[string]any); g["unknown"] == nil {
		t.Fatalf("family ext %#v", ext)
	}
}

func TestOtherDialectExtDroppedAndReported(t *testing.T) {
	forgejo, _ := provider.Get(provider.Forgejo)
	proj, _, err := forgejo.Parse(fstest.MapFS{".forgejo/workflows/ci.yml": &fstest.MapFile{
		Data: []byte("on: push\nenable-email-notifications: true\njobs:\n  x:\n    runs-on: r\n    steps:\n      - run: echo\n")}})
	if err != nil {
		t.Fatal(err)
	}
	gitea, _ := provider.Get(provider.Gitea)
	files, rep, err := gitea.Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(files[".gitea/workflows/ci.yml"]), "enable-email-notifications") {
		t.Fatal("forgejo-only key written to gitea file")
	}
	found := false
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "enable-email-notifications") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no report entry names the dropped key: %+v", rep.Entries)
	}
	back, _ := provider.Get(provider.Forgejo)
	files, _, err = back.Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files[".forgejo/workflows/ci.yml"]), "enable-email-notifications: true") {
		t.Fatalf("forgejo emit lost its own key:\n%s", files[".forgejo/workflows/ci.yml"])
	}
}
