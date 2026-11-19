package meta

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func layer(source string) *Meta {
	return &Meta{
		Version: Version, Source: source, Dialect: source,
		IDs: map[string]string{"id-" + source: "src " + source},
	}
}

func threeLayers() *Stack {
	return &Stack{Layers: []*Meta{layer("gha"), layer("gitlab"), layer("gitea")}}
}

func sameLayers(t *testing.T, got, want *Stack) {
	t.Helper()
	if len(got.Layers) != len(want.Layers) {
		t.Fatalf("got %d layers, want %d", len(got.Layers), len(want.Layers))
	}
	for i := range want.Layers {
		if !reflect.DeepEqual(got.Layers[i], want.Layers[i]) {
			t.Errorf("layer %d:\n got %+v\nwant %+v", i, got.Layers[i], want.Layers[i])
		}
	}
}

func TestStackRoundTrip(t *testing.T) {
	files := map[string][]byte{"a.yml": []byte("a: 1\n"), "b.yml": []byte("b: 2\n")}
	for name, write := range map[string]func(map[string][]byte, *Stack) map[string][]byte{
		"sidecar": AttachStack,
		"embed":   EmbedStack,
	} {
		t.Run(name, func(t *testing.T) {
			s := threeLayers()
			out := write(files, s)
			got, err := LoadStack(asFS(out), []string{"a.yml", "b.yml"})
			if err != nil {
				t.Fatal(err)
			}
			sameLayers(t, got, s)
			if len(got.Top().Files) == 0 {
				t.Error("top layer lost its file hashes")
			}
			if _, err := got.Top().Stale(asFS(out)); err != nil {
				t.Errorf("fresh stack reads stale: %v", err)
			}
		})
	}
}

func TestStackSidecarFormat(t *testing.T) {
	out := AttachStack(map[string][]byte{"a.yml": []byte("a: 1\n")}, &Stack{Layers: []*Meta{layer("gha"), layer("gitlab")}})
	doc := string(out[SidecarPath])
	if !strings.HasPrefix(doc, "layers:") || !strings.Contains(doc, "version: 2\n") || strings.Count(doc, "version:") != 1 {
		t.Fatalf("unexpected document:\n%s", doc)
	}
}

func TestLoadV1AsOneLayer(t *testing.T) {
	cases := []struct {
		name  string
		dir   string
		parse []string
	}{
		{"sidecar", "../testdata/meta/v1-sidecar", []string{".gitlab-ci.yml"}},
		{"embedded", "../testdata/meta/v1-embedded", []string{".gitlab-ci.yml"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := LoadStack(os.DirFS(c.dir), c.parse)
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Layers) != 1 {
				t.Fatalf("got %d layers, want 1", len(s.Layers))
			}
			m := s.Top()
			if m.Source != "gha" || m.Dialect != "gha" || len(m.Lost) == 0 || len(m.Emitted) == 0 {
				t.Errorf("v1 fields lost: %+v", m)
			}
			if _, err := m.Stale(os.DirFS(c.dir)); err != nil {
				t.Errorf("recorded v1 metadata reads stale: %v", err)
			}
		})
	}
}

func TestLoadStackTopReadsV1(t *testing.T) {
	m, err := load(os.DirFS("../testdata/meta/v1-sidecar"), nil)
	if err != nil || m == nil || m.Source != "gha" {
		t.Fatalf("got %+v, %v", m, err)
	}
}

func TestStackFindNearest(t *testing.T) {
	s := &Stack{Layers: []*Meta{layer("gha"), layer("gitlab"), layer("gha")}}
	for source, want := range map[string]int{"gha": 2, "gitlab": 1, "forgejo": -1} {
		if got := s.Find(source); got != want {
			t.Errorf("Find(%q) = %d, want %d", source, got, want)
		}
	}
	if (&Stack{}).Top() != nil || (&Stack{}).Find("gha") != -1 {
		t.Error("empty stack must have no top and find nothing")
	}
}

func TestStackUnknownVersion(t *testing.T) {
	for _, v := range []string{"0", "3", "99"} {
		fsys := fstest.MapFS{SidecarPath: {Data: []byte("version: " + v + "\nlayers: []\n")}}
		_, err := LoadStack(fsys, nil)
		if err == nil || !strings.Contains(err.Error(), "version "+v) {
			t.Errorf("version %s: err = %v, want one naming the version", v, err)
		}
	}
}

func TestEmptyStackWritesNothing(t *testing.T) {
	files := map[string][]byte{"a.yml": []byte("a: 1")}
	for name, out := range map[string]map[string][]byte{
		"sidecar": AttachStack(files, &Stack{}),
		"embed":   EmbedStack(files, &Stack{}),
	} {
		if _, ok := out[SidecarPath]; ok {
			t.Errorf("%s: sidecar written", name)
		}
		if string(out["a.yml"]) != "a: 1" {
			t.Errorf("%s: file changed to %q", name, out["a.yml"])
		}
	}
}

func TestLoadStackAbsentIsEmptyNotNil(t *testing.T) {
	s, err := LoadStack(asFS(map[string][]byte{"a.yml": []byte("a: 1\n")}), []string{"a.yml"})
	if err != nil || s == nil || len(s.Layers) != 0 {
		t.Fatalf("got %+v, %v", s, err)
	}
}

func TestV1FixtureFilesExist(t *testing.T) {
	for _, p := range []string{"v1-sidecar/.pipemaster/meta.yaml", "v1-sidecar/.gitlab-ci.yml", "v1-embedded/.gitlab-ci.yml"} {
		if _, err := os.Stat(filepath.Join("..", "testdata", "meta", p)); err != nil {
			t.Error(err)
		}
	}
}
