package roundtrip_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// noSourceForm lists supported features that a fixture cannot exercise, with the reason.
var noSourceForm = map[string]string{
	provider.GHA + "/" + string(caps.JobEnvironmentApproval):     "approval rules live in repository settings and no parser sets Environment.Approval",
	provider.Gitea + "/" + string(caps.JobEnvironmentApproval):   "approval rules live in repository settings and no parser sets Environment.Approval",
	provider.Forgejo + "/" + string(caps.JobEnvironmentApproval): "approval rules live in repository settings and no parser sets Environment.Approval",
	provider.GitLab + "/" + string(caps.JobEnvironmentApproval):  "approval rules live in project settings and no parser sets Environment.Approval",
	provider.GitLab + "/" + string(caps.ConfigName):              "GitLab variables parse as plain script text, so no gitlab source yields a secrets or vars reference",
}

// fixtureComplete reports whether a fixture directory holds a source file for the provider and its IR golden, so
// an empty directory named after a feature does not count as coverage.
func fixtureComplete(id, dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "ir.golden.yaml")); err != nil {
		return false
	}
	files, err := fixtureSources(id, dir)
	return err == nil && len(files) > 0
}

// fixtureSources lists the source files of provider id in a fixture directory.
func fixtureSources(id, dir string) ([]string, error) {
	p, err := provider.Get(id)
	if err != nil {
		return nil, err
	}
	l, ok := p.(provider.Layout)
	if !ok {
		return nil, fmt.Errorf("provider %s does not implement provider.Layout", id)
	}
	return l.SourceFiles(os.DirFS(dir))
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
	for _, id := range provider.IDs() {
		m := prov(t, id).Caps()
		dirs := []string{filepath.Join(featuresRoot, id, "features")}
		if provider.Family(id) == provider.GHA && id != provider.GHA {
			// A dialect runs every GHA fixture remapped to its own directory, so those count as coverage.
			dirs = append(dirs, filepath.Join(featuresRoot, provider.GHA, "features"))
		}
		for _, f := range caps.AllFeatures() {
			if m.Lookup(f).Level == caps.Lost {
				continue
			}
			key := id + "/" + string(f)
			t.Run(key, func(t *testing.T) {
				has := false
				for _, dir := range dirs {
					if _, err := os.Stat(dir); err == nil {
						has = has || hasFixtureDir(t, id, dir, f)
					}
				}
				if reason, exempt := noSourceForm[key]; exempt {
					if has {
						t.Errorf("%s has a fixture; drop its noSourceForm entry (%s)", key, reason)
					}
					return
				}
				if !has {
					t.Errorf("no fixture directory %s/%s", dirs[0], f)
				}
			})
		}
	}
}

func TestFixtureSourcesPerProvider(t *testing.T) {
	cases := []struct {
		files map[string]string
		id    string
		want  int
	}{
		{map[string]string{".github/workflows/ci.yml": "x"}, provider.GHA, 1},
		{map[string]string{".gitea/workflows/a.yaml": "x", ".gitea/workflows/b.yml": "x"}, provider.Gitea, 2},
		{map[string]string{".gitlab-ci.yml": "x"}, provider.GitLab, 1},
		{map[string]string{"ci/other.yml": "x"}, provider.GitLab, 0},
		{map[string]string{".gitlab-ci.yml": "x"}, provider.GHA, 0},
		{nil, provider.GHA, 0},
	}
	for _, c := range cases {
		dir := t.TempDir()
		for n, body := range c.files {
			p := filepath.Join(dir, filepath.FromSlash(n))
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil { // #nosec G301 -- temp dir
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil { // #nosec G306 G703 -- temp dir
				t.Fatal(err)
			}
		}
		got, err := fixtureSources(c.id, dir)
		if err != nil || len(got) != c.want {
			t.Errorf("%s %v: got %v, %v; want %d files", c.id, c.files, got, err, c.want)
		}
	}
}

func TestEveryProviderHasLayout(t *testing.T) {
	for _, id := range provider.IDs() {
		if _, ok := prov(t, id).(provider.Layout); !ok {
			t.Errorf("%s does not implement provider.Layout", id)
		}
	}
}
