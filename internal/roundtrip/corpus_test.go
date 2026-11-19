package roundtrip_test

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/golden"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/validate"
)

const (
	corpusRoot    = "../../testdata/corpus"
	corpusSummary = "verdicts.golden.txt"
)

// corpusEntries lists the corpus directories with the provider each one is written in.
func corpusEntries(t *testing.T) []fixture {
	t.Helper()
	ents, err := os.ReadDir(corpusRoot)
	if err != nil {
		t.Fatal(err)
	}
	var out []fixture
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(corpusRoot, e.Name())
		var found []string
		for _, id := range []string{provider.GHA, provider.GitLab} {
			if prov(t, id).Detect(os.DirFS(dir)) {
				found = append(found, id)
			}
		}
		if len(found) != 1 {
			t.Fatalf("%s: detected providers %v, want exactly one", e.Name(), found)
		}
		out = append(out, fixture{found[0], e.Name(), dir})
	}
	if len(out) == 0 {
		t.Fatal("no corpus entries")
	}
	return out
}

// verdictCounts counts the feature uses of src per verdict; native is every use the report does not list.
func verdictCounts(src *ir.Project, rep *report.Report) map[caps.Level]int {
	used := map[ir.FeatureUse]bool{}
	for _, u := range ir.Features(src) {
		used[u] = true
	}
	counts := map[caps.Level]int{}
	listed := 0
	for _, e := range rep.Entries {
		counts[e.Level]++
		if used[ir.FeatureUse{IRPath: e.IRPath, Feature: e.Feature}] {
			listed++
		}
	}
	counts[caps.Native] = len(used) - listed
	return counts
}

// findingRE strips the file position from a linter line so findings compare across files and layouts.
var findingRE = regexp.MustCompile(`^\S+?:\d+:\d+: `)

// findings are the schema and linter complaints about a set of files, without positions.
type findings map[string]bool

func (f findings) add(lines string) {
	for _, l := range strings.Split(lines, "\n") {
		if l = strings.TrimSpace(findingRE.ReplaceAllString(l, "")); l != "" && !strings.HasPrefix(l, "|") && !strings.Contains(l, " | ") {
			f[l] = true
		}
	}
}

// braceVarRE matches ${NAME}, which the emitter prints as $NAME; both spell the same variable.
var braceVarRE = regexp.MustCompile(`\$\{(\w+)\}`)

// problems lists what validate.File and, for GHA, actionlint find in one file. Shell and Python checks
// are off: they judge the scripts of the source repository, not the conversion.
func problems(t *testing.T, id, name string, data []byte) findings {
	t.Helper()
	out := findings{}
	if err := validate.File(id, name, data); err != nil && !errors.Is(err, validate.ErrNoSchema) {
		// The schema id carries a fixed file name that must not tell an included file from the merged one.
		out.add(braceVarRE.ReplaceAllString(strings.NewReplacer(name, "", "gitlab.com/.gitlab-ci.yml#", "gitlab.com/#").Replace(err.Error()), "$$$1"))
	}
	if id == provider.GHA {
		dir := t.TempDir()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("actionlint", "-no-color", "-shellcheck=", "-pyflakes=", "-ignore", lintIgnore, p) // #nosec G204 -- fixed binary, temp path
		if b, err := cmd.CombinedOutput(); err != nil {
			out.add(string(b))
		}
	}
	return out
}

// sourceProblems are the complaints the unconverted files already draw; the schema and actionlint lag
// behind some real syntax, and a converter cannot fix that.
func sourceProblems(t *testing.T, f fixture) findings {
	t.Helper()
	out := findings{}
	root := os.DirFS(f.dir)
	var rels []string
	if f.provider == provider.GHA {
		ents, err := fs.Glob(root, ".github/workflows/*.y*ml")
		if err != nil {
			t.Fatal(err)
		}
		rels = ents
	} else {
		err := fs.WalkDir(root, ".", func(p string, d fs.DirEntry, werr error) error {
			if werr == nil && !d.IsDir() && (strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml")) {
				rels = append(rels, p)
			}
			return werr
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range rels {
		data, err := fs.ReadFile(root, rel)
		if err != nil {
			t.Fatal(err)
		}
		for l := range problems(t, f.provider, rel, data) {
			out[l] = true
		}
	}
	return out
}

// checkEmitted fails on a schema or linter complaint that is not already in known.
func checkEmitted(t *testing.T, id string, files map[string][]byte, known findings) {
	t.Helper()
	for _, name := range names(files) {
		for l := range problems(t, id, name, files[name]) {
			if !known[l] {
				t.Errorf("%s: %s", name, l)
			}
		}
		if id == provider.GitLab && strings.Contains(string(files[name]), exprOpen) {
			t.Errorf("%s holds a GHA expression", name)
		}
	}
}

func TestCorpus(t *testing.T) {
	requireActionlint(t)
	var summary bytes.Buffer
	healthy := true
	for _, f := range corpusEntries(t) {
		ok := t.Run(f.name, func(t *testing.T) {
			a, b := prov(t, f.provider), prov(t, other(f.provider))
			fsA := os.DirFS(f.dir)
			src, _, err := a.Parse(fsA)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			self, _, err := a.Emit(src)
			if err != nil {
				t.Fatalf("identity emit: %v", err)
			}
			again, _, err := a.Parse(toFS(self))
			if err != nil {
				t.Fatalf("identity re-parse: %v", err)
			}
			if same, diff := ir.Equal(src, again); !same {
				t.Errorf("A->A differs:\n%s", diff)
			}
			known := sourceProblems(t, f)
			checkEmitted(t, a.ID(), self, known)

			r := hops(t, fsA, a, b, false, true)
			if same, diff := ir.Equal(r.src, r.back); !same {
				t.Errorf("A->B->A differs:\n%s", diff)
			}
			cross := map[string][]byte{}
			for n, data := range r.toFiles {
				if !strings.HasPrefix(n, ".pipemaster/") {
					cross[n] = data
				}
			}
			checkEmitted(t, b.ID(), cross, findings{})

			c := verdictCounts(r.src, r.toRep)
			fmt.Fprintf(&summary, "%s %s->%s native=%d emulated=%d external=%d lost=%d\n", f.name, a.ID(), b.ID(),
				c[caps.Native], c[caps.Emulated], c[caps.External], c[caps.Lost])
		})
		healthy = healthy && ok
	}
	if !healthy {
		return
	}
	golden.Assert(t, filepath.Join(corpusRoot, corpusSummary), summary.Bytes())
}
