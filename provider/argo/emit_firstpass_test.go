package argo_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/internal/cli"
)

// TestFirstArgoPassKeepsGHAFiles checks that reading the Argo files of a GitHub project and writing them again
// changes no value. The comments hold the job names, which the reader does not take back.
func TestFirstArgoPassKeepsGHAFiles(t *testing.T) {
	ents, err := os.ReadDir(corpusRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !e.IsDir() || !strings.Contains(e.Name(), "-gha-") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			first, second := filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")
			convert(t, filepath.Join(corpusRoot, e.Name()), "", "argo", first)
			convert(t, first, "argo", "argo", second)
			one, two := yamlTree(t, filepath.Join(first, ".argo")), yamlTree(t, filepath.Join(second, ".argo"))
			for name, want := range one {
				if !reflect.DeepEqual(want, two[name]) {
					t.Errorf("%s changed in the first Argo pass", name)
				}
			}
			if len(one) != len(two) {
				t.Errorf("the first pass wrote %d files, the second %d", len(one), len(two))
			}
		})
	}
}

func convert(t *testing.T, dir, from, to, out string) {
	t.Helper()
	args := []string{"convert", dir, "--to", to, "-o", out}
	if from != "" {
		args = append(args, "--from", from)
	}
	var stdout, stderr strings.Builder
	if code := cli.Run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("convert %v exit code = %d\n%s", args, code, stderr.String())
	}
}

// yamlTree decodes every YAML file under dir, so a comparison ignores comments and key order.
func yamlTree(t *testing.T, dir string) map[string][]any {
	t.Helper()
	out := map[string][]any{}
	fsys := os.DirFS(dir)
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		for {
			var v any
			if dec.Decode(&v) != nil {
				return nil
			}
			out[path] = append(out[path], v)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
