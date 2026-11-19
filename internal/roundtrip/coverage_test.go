package roundtrip_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// noSourceForm lists supported features that a fixture cannot exercise, with the reason.
var noSourceForm = map[string]string{
	provider.GHA + "/" + string(caps.JobEnvironmentApproval):    "approval rules live in repository settings and no parser sets Environment.Approval",
	provider.GitLab + "/" + string(caps.JobEnvironmentApproval): "approval rules live in project settings and no parser sets Environment.Approval",
}

// fixtureComplete reports whether a fixture directory holds a source file for the provider and its IR golden, so
// an empty directory named after a feature does not count as coverage.
func fixtureComplete(id, dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "ir.golden.yaml")); err != nil {
		return false
	}
	if provider.Family(id) != provider.GHA {
		_, err := os.Stat(filepath.Join(dir, ".gitlab-ci.yml"))
		return err == nil
	}
	files, _ := filepath.Glob(filepath.Join(dir, ".github", "workflows", "*.y*ml"))
	return len(files) > 0
}

// hasFixtureDir reports whether dir holds a complete fixture named after the feature, optionally followed by "--<variant>".
func hasFixtureDir(t *testing.T, id, dir string, f caps.Feature) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() && (n == string(f) || strings.HasPrefix(n, string(f)+"--")) && fixtureComplete(id, filepath.Join(dir, n)) {
			return true
		}
	}
	return false
}

func TestCoverageRejectsEmptyFixtureDir(t *testing.T) {
	for _, id := range []string{provider.GHA, provider.GitLab} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, string(caps.JobEnv)), 0o750); err != nil { // #nosec G301 -- temp dir
			t.Fatal(err)
		}
		if hasFixtureDir(t, id, dir, caps.JobEnv) {
			t.Errorf("%s: an empty fixture directory counted as coverage", id)
		}
		src := filepath.Join(dir, string(caps.JobEnv), ".gitlab-ci.yml")
		if id == provider.GHA {
			src = filepath.Join(dir, string(caps.JobEnv), ".github", "workflows", "ci.yml")
			if err := os.MkdirAll(filepath.Dir(src), 0o750); err != nil { // #nosec G301 -- temp dir
				t.Fatal(err)
			}
		}
		for _, name := range []string{src, filepath.Join(dir, string(caps.JobEnv), "ir.golden.yaml")} {
			if err := os.WriteFile(name, []byte("x: 1\n"), 0o600); err != nil { // #nosec G306 G703 -- temp dir
				t.Fatal(err)
			}
		}
		if !hasFixtureDir(t, id, dir, caps.JobEnv) {
			t.Errorf("%s: a complete fixture directory did not count", id)
		}
	}
}

// TestFeatureFixtureCoverage requires a fixture directory for every feature a provider can write.
// A lost feature has no syntax in the provider, so it has no fixture.
func TestFeatureFixtureCoverage(t *testing.T) {
	for _, id := range []string{provider.GHA, provider.GitLab} {
		m := prov(t, id).Caps()
		dir := filepath.Join(featuresRoot, id, "features")
		for _, f := range caps.AllFeatures() {
			if m.Lookup(f).Level == caps.Lost {
				continue
			}
			key := id + "/" + string(f)
			t.Run(key, func(t *testing.T) {
				has := hasFixtureDir(t, id, dir, f)
				if reason, exempt := noSourceForm[key]; exempt {
					if has {
						t.Errorf("%s has a fixture; drop its noSourceForm entry (%s)", key, reason)
					}
					return
				}
				if !has {
					t.Errorf("no fixture directory %s/%s", dir, f)
				}
			})
		}
	}
}
