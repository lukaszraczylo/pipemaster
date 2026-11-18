package gitlab_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/golden"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
	"github.com/lukaszraczylo/pipemaster/validate"
)

const featuresDir = "../../testdata/gitlab/features"

func TestParseFeatures(t *testing.T) {
	dirs, err := os.ReadDir(featuresDir)
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, f := range caps.AllFeatures() {
		known[string(f)] = true
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		t.Run(d.Name(), func(t *testing.T) {
			if !known[d.Name()] {
				t.Fatalf("%s is not a caps feature", d.Name())
			}
			root := filepath.Join(featuresDir, d.Name())
			src, err := os.ReadFile(filepath.Join(root, rootFile)) // #nosec G304 -- fixture path
			if err != nil {
				t.Fatal(err)
			}
			if err := validate.File("gitlab", rootFile, src); err != nil {
				t.Fatalf("fixture is not valid GitLab CI: %v", err)
			}
			p, _, err := gitlab.New().Parse(os.DirFS(root))
			if err != nil {
				t.Fatal(err)
			}
			got, err := ir.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, filepath.Join(root, "ir.golden.yaml"), got)
			back, err := ir.Unmarshal(got)
			if err != nil {
				t.Fatalf("golden does not decode: %v", err)
			}
			if ok, diff := ir.Equal(p, back); !ok {
				t.Fatalf("Marshal/Unmarshal round trip differs: %s", diff)
			}
		})
	}
}
