package roundtrip_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

const taskFileRoot = "../../testdata/concourse/taskfiles"

// TestTaskFileFixturesTripThroughEveryTarget converts each fixture to every other provider and back with the
// metadata, and requires the same IR; the task files are not in the converted output.
func TestTaskFileFixturesTripThroughEveryTarget(t *testing.T) {
	ents, err := os.ReadDir(taskFileRoot)
	if err != nil {
		t.Fatal(err)
	}
	a := prov(t, provider.Concourse)
	for _, e := range ents {
		fsA := os.DirFS(filepath.Join(taskFileRoot, e.Name()))
		t.Run(e.Name()+"/identity", func(t *testing.T) {
			src, _, err := a.Parse(fsA)
			if err != nil {
				t.Fatal(err)
			}
			again, _ := returnInto(t, a, src)
			if same, diff := ir.Equal(src, again); !same {
				t.Errorf("A->A differs:\n%s", diff)
			}
		})
		for _, to := range targets(provider.Concourse) {
			t.Run(e.Name()+"/to-"+to, func(t *testing.T) {
				r := hops(t, fsA, a, prov(t, to), false, true)
				if same, diff := ir.Equal(r.src, r.back); !same {
					t.Errorf("A->B->A differs:\n%s", diff)
				}
			})
		}
	}
}

// TestRunDirInsideAnInputWaitsForTheLayout checks that the link of the input comes before the cd into it, and
// that no target applies the directory before the first script line.
func TestRunDirInsideAnInputWaitsForTheLayout(t *testing.T) {
	src, _, err := prov(t, provider.Concourse).Parse(os.DirFS(filepath.Join(taskFileRoot, "defaults")))
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"gitlab", "gha", "tekton"} {
		t.Run(to, func(t *testing.T) {
			files, _, err := prov(t, to).Emit(src)
			if err != nil {
				t.Fatal(err)
			}
			var text string
			for n, b := range files {
				if !strings.HasPrefix(n, ".pipemaster/") {
					text += string(b)
				}
			}
			link, cd := strings.Index(text, `ln -s "$PWD" source`), strings.Index(text, "cd 'source/sub'")
			if link < 0 || cd < 0 || link > cd {
				t.Errorf("link at %d, cd at %d; the link must come first:\n%s", link, cd, text)
			}
			for _, field := range []string{"working-directory: source/sub", "workingDir: source/sub"} {
				if strings.Contains(text, field) {
					t.Errorf("the output sets %q before the layout", field)
				}
			}
		})
	}
}
