package tekton_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/golden"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
)

const featuresDir = "../../testdata/tekton/features"

// TestParseFeatures parses each feature fixture, checks that the fixture uses the feature it is named after,
// and compares the IR with its golden file.
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
			feature, _, _ := strings.Cut(d.Name(), "--")
			if !known[feature] {
				t.Fatalf("%s is not a caps feature", d.Name())
			}
			root := filepath.Join(featuresDir, d.Name())
			fsys := os.DirFS(root)
			sources, err := tekton.New().(provider.Layout).SourceFiles(fsys)
			if err != nil || len(sources) == 0 {
				t.Fatalf("no source files: %v", err)
			}
			p, _, err := tekton.New().Parse(fsys)
			if err != nil {
				t.Fatal(err)
			}
			if !usesFeature(p, caps.Feature(feature)) {
				t.Errorf("the fixture does not use %s", feature)
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

// usesFeature reports whether the project uses f. ir.Features does not report script.interpolation, which only
// an emitter detects, so it counts as used when a script holds a reference.
func usesFeature(p *ir.Project, f caps.Feature) bool {
	if f != caps.ScriptInterpolation {
		return slices.ContainsFunc(ir.Features(p), func(u ir.FeatureUse) bool { return u.Feature == f })
	}
	for _, n := range ir.Nodes(p) {
		if n.Step != nil && slices.ContainsFunc(n.Step.Script, func(l string) bool { return strings.Contains(l, "${{") }) {
			return true
		}
	}
	return false
}
