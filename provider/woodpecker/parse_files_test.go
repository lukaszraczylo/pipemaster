package woodpecker_test

import (
	"errors"
	"io/fs"
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/woodpecker"
)

const minimal = "steps:\n  - name: build\n    image: golang\n    commands:\n      - go build\n"

func TestDirectoryWinsOverFile(t *testing.T) {
	p, rep := parseRaw(t, map[string]string{
		".woodpecker/ci.yaml": minimal,
		".woodpecker.yaml":    minimal,
	})
	if !reflect.DeepEqual(p.Origin.Files, []string{".woodpecker/ci.yaml"}) {
		t.Fatalf("Origin.Files = %v", p.Origin.Files)
	}
	if !hasEntry(rep, ".woodpecker.yaml is ignored") {
		t.Fatalf("no ignored-file entry: %+v", rep.Entries)
	}
}

func TestYamlWinsOverYml(t *testing.T) {
	p, rep := parseRaw(t, map[string]string{".woodpecker.yaml": minimal, ".woodpecker.yml": minimal})
	if !reflect.DeepEqual(p.Origin.Files, []string{".woodpecker.yaml"}) || !hasEntry(rep, ".woodpecker.yml is ignored") {
		t.Fatalf("files %v entries %+v", p.Origin.Files, rep.Entries)
	}
}

func TestDirectoryIgnoresSubfoldersAndOtherFiles(t *testing.T) {
	p, _ := parseRaw(t, map[string]string{
		".woodpecker/a.yml":        minimal,
		".woodpecker/sub/b.yaml":   minimal,
		".woodpecker/notes.txt":    "x",
		".woodpecker/.hidden.yaml": minimal,
	})
	got := []string{}
	for _, j := range p.Pipelines[0].Jobs {
		got = append(got, j.ID)
	}
	if !reflect.DeepEqual(got, []string{"a", "hidden"}) {
		t.Fatalf("jobs = %v", got)
	}
}

func TestWorkflowNames(t *testing.T) {
	p, _ := parseRaw(t, map[string]string{".woodpecker.yaml": minimal})
	if p.Pipelines[0].ID != "woodpecker" || p.Pipelines[0].Jobs[0].ID != "woodpecker" {
		t.Fatalf("ids = %s/%s", p.Pipelines[0].ID, p.Pipelines[0].Jobs[0].ID)
	}
}

func TestDetectAndLayout(t *testing.T) {
	a := woodpecker.New()
	l, ok := a.(provider.Layout)
	if !ok {
		t.Fatal("adapter does not implement provider.Layout")
	}
	cases := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"none", map[string]string{"ci.yaml": "x"}, nil},
		{"dir", map[string]string{".woodpecker/a.yaml": "x", ".woodpecker/b.yml": "x", ".woodpecker/c.txt": "x"}, []string{".woodpecker/a.yaml", ".woodpecker/b.yml"}},
		{"file", map[string]string{".woodpecker.yml": "x"}, []string{".woodpecker.yml"}},
		{"empty dir falls through", map[string]string{".woodpecker/readme.md": "x", ".woodpecker.yaml": "x"}, []string{".woodpecker.yaml"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := l.SourceFiles(repo(c.files))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("SourceFiles = %v, want %v", got, c.want)
			}
			if a.Detect(repo(c.files)) != (len(c.want) > 0) {
				t.Fatalf("Detect = %v, want %v", a.Detect(repo(c.files)), len(c.want) > 0)
			}
		})
	}
	var _ fs.FS = repo(nil)
}

func TestSanitizeID(t *testing.T) {
	r, ok := woodpecker.New().(provider.IDRules)
	if !ok {
		t.Fatal("adapter does not implement provider.IDRules")
	}
	for in, want := range map[string]string{
		"build":      "build",
		"build test": "build-test",
		".lint":      "lint",
		"a/b":        "a-b",
		"":           "workflow",
		"v1.2":       "v1.2",
	} {
		if got := r.SanitizeID(in); got != want {
			t.Errorf("SanitizeID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMissingStepsIsAnError(t *testing.T) {
	if err := parseErr(t, map[string]string{".woodpecker.yaml": "when:\n  event: push\n"}); err == nil {
		t.Fatal("want error")
	}
}

func TestDependsOn(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{
		".woodpecker/.lint.yaml":  minimal,
		".woodpecker/test.yaml":   minimal,
		".woodpecker/deploy.yaml": "depends_on:\n  - lint\n  - name: test\n    optional: true\n  - name: ghost\n    optional: true\nsteps:\n  - name: s\n    image: x\n    commands: [y]\n",
	})
	for _, j := range p.Pipelines[0].Jobs {
		if j.ID != "deploy" {
			continue
		}
		want := map[string]bool{"lint": false, "test": true, "ghost": true}
		for _, n := range j.Needs {
			if o, ok := want[n.JobID]; ok && n.Optional == o {
				delete(want, n.JobID)
			} else {
				t.Fatalf("unexpected need %+v", n)
			}
		}
		if len(want) != 0 {
			t.Fatalf("missing needs %v in %+v", want, j.Needs)
		}
		return
	}
	t.Fatal("deploy not found")
}

func TestRegisteredAndEmitUnavailable(t *testing.T) {
	p, err := provider.Get(provider.Woodpecker)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Emit(nil); !errors.Is(err, woodpecker.ErrNoEmitter) {
		t.Fatalf("Emit error = %v", err)
	}
}
