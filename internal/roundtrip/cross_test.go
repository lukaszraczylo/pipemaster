package roundtrip_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/internal/golden"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/validate"
)

const (
	reportGolden = "report.golden.txt"
	exprOpen     = "${{"
	// lintIgnore skips two findings that are the emitter's reported choices: a condition GHA cannot state
	// becomes if: false, and a self-hosted runner label has no entry in actionlint's hosted list.
	lintIgnore = `constant expression "false" in condition|label ".*" is unknown`
)

// providerCheck holds the Go-side checks one provider's output gets beyond schema validation.
type providerCheck struct {
	// requireLinter skips the test or fails it when the provider's linter is missing.
	requireLinter func(t *testing.T)
	// lintFile fails t on a linter complaint; lintFindings returns the complaints without positions.
	lintFile     func(t *testing.T, name string, data []byte)
	lintFindings func(t *testing.T, name string, data []byte) findings
	// lintFrom is the provider whose fixtures TestCrossGoldenActionlint converts into this one.
	lintFrom string
	// errReplace holds old/new pairs that strip schema-id noise from a validation error.
	errReplace []string
	// schemaLag names corpus entries whose own file fails the pinned schema, so a full return only warns.
	schemaLag []string
	// noGHAExpr is true when the output must not hold a GHA expression.
	noGHAExpr bool
}

// providerChecks is the one place that keys Go-side checks by provider.
var providerChecks = map[string]providerCheck{
	provider.GHA: {requireLinter: requireActionlint, lintFile: lintFile, lintFindings: actionlint, lintFrom: provider.GitLab},
	provider.GitLab: {
		errReplace: []string{"gitlab.com/.gitlab-ci.yml#", "gitlab.com/#"},
		schemaLag:  []string{"15-gitlab-charts-gitlab", "16-gitlab-cli", "20-gitlab-gitlab-runner", "24-gitlab-release-cli"},
		noGHAExpr:  true,
	},
}

// lintedProviders lists, sorted, the providers whose output has a linter.
func lintedProviders() []string {
	var out []string
	for id, c := range providerChecks {
		if c.lintFile != nil {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func TestCrossGolden(t *testing.T) {
	for _, f := range allFixtures(t) {
		for _, to := range targets(f.provider) {
			t.Run(f.provider+"/"+f.name+"/to-"+to, func(t *testing.T) {
				a, b := prov(t, f.provider), prov(t, to)
				proj, _, err := a.Parse(os.DirFS(f.dir))
				if err != nil {
					t.Fatal(err)
				}
				files, rep, err := b.Emit(proj)
				if err != nil {
					t.Fatal(err)
				}
				out := filepath.Join(f.golden, "to-"+b.ID())
				for _, name := range names(files) {
					if err := validate.File(b.ID(), name, files[name]); err != nil && !errors.Is(err, validate.ErrNoSchema) {
						t.Errorf("validate %s: %v", name, err)
					}
					if providerChecks[b.ID()].noGHAExpr && strings.Contains(string(files[name]), exprOpen) {
						t.Errorf("%s holds a GHA expression", name)
					}
					golden.Assert(t, filepath.Join(out, name), files[name])
				}
				rep.Source, rep.Target = a.ID(), b.ID()
				var buf bytes.Buffer
				if err := rep.Render(&buf, "text"); err != nil {
					t.Fatal(err)
				}
				golden.Assert(t, filepath.Join(out, reportGolden), buf.Bytes())
			})
		}
	}
}

// requireLintersEnv makes a missing optional linter a failure instead of a skip.
const requireLintersEnv = "PIPEMASTER_REQUIRE_LINTERS"

func requireActionlint(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("actionlint"); err != nil {
		if os.Getenv(requireLintersEnv) == "1" {
			t.Fatalf("actionlint is not installed and %s=1", requireLintersEnv)
		}
		t.Skip("actionlint is not installed; GHA outputs are not linted")
	}
}

func TestCrossGoldenActionlint(t *testing.T) {
	for _, to := range lintedProviders() {
		c := providerChecks[to]
		c.requireLinter(t)
		for _, f := range fixtures(t, c.lintFrom) {
			t.Run(f.name, func(t *testing.T) {
				proj, _, err := prov(t, c.lintFrom).Parse(os.DirFS(f.dir))
				if err != nil {
					t.Fatal(err)
				}
				files, _, err := prov(t, to).Emit(proj)
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range names(files) {
					c.lintFile(t, name, files[name])
				}
			})
		}
	}
}

func lintFile(t *testing.T, name string, data []byte) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("actionlint", "-no-color", "-ignore", lintIgnore, p) // #nosec G204 -- fixed binary, temp path
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("actionlint %s: %v\n%s", name, err, out)
	}
}

// An unedited emitted file never reports "marker ignored": that note means the user edited the file.
func TestUneditedFilesNeverIgnoreMarkers(t *testing.T) {
	for _, f := range fixtures(t, provider.GHA) {
		t.Run(f.name, func(t *testing.T) {
			proj, _, err := prov(t, provider.GHA).Parse(os.DirFS(f.dir))
			if err != nil {
				t.Fatal(err)
			}
			files, _, err := prov(t, provider.GitLab).Emit(proj)
			if err != nil {
				t.Fatal(err)
			}
			fsys := fstest.MapFS{}
			for n, b := range files {
				fsys[n] = &fstest.MapFile{Data: b}
			}
			_, rep, err := prov(t, provider.GitLab).Parse(fsys)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range rep.Entries {
				if strings.Contains(e.Detail, "marker ignored") {
					t.Errorf("%s: %s", e.IRPath, e.Detail)
				}
			}
		})
	}
}
