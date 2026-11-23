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
	"slices"
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
		id := corpusProvider(t, e.Name())
		if !prov(t, id).Detect(os.DirFS(dir)) {
			t.Fatalf("%s: provider %s does not detect the entry", e.Name(), id)
		}
		out = append(out, fixture{id, e.Name(), dir, dir})
	}
	if len(out) == 0 {
		t.Fatal("no corpus entries")
	}
	return out
}

// corpusProvider reads the provider from the NN-<provider>-<slug> directory name; the dialect
// directories overlap, so detection cannot tell them apart.
func corpusProvider(t *testing.T, name string) string {
	t.Helper()
	parts := strings.SplitN(name, "-", 3)
	if len(parts) != 3 || !slices.Contains(provider.IDs(), parts[1]) {
		t.Fatalf("%s: want NN-<provider>-<slug> with provider in %v", name, provider.IDs())
	}
	return parts[1]
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
		l = sortProps(strings.TrimSpace(findingRE.ReplaceAllString(l, "")))
		if l != "" && !strings.HasPrefix(l, "|") && !strings.Contains(l, " | ") {
			f[l] = true
		}
	}
}

// propsRE matches the property list of a schema "additional properties" complaint, whose order the validator
// takes from map iteration.
var propsRE = regexp.MustCompile(`additional properties ('[^']*'(?:, '[^']*')*) not allowed`)

// sortProps puts the property names of a complaint in order, so the same complaint compares equal.
func sortProps(l string) string {
	return propsRE.ReplaceAllStringFunc(l, func(m string) string {
		list := propsRE.FindStringSubmatch(m)[1]
		names := strings.Split(list, ", ")
		slices.Sort(names)
		return "additional properties " + strings.Join(names, ", ") + " not allowed"
	})
}

// dialectCtxRE matches the context aliases of Gitea and Forgejo.
var dialectCtxRE = regexp.MustCompile(`\b(?:gitea|forgejo|forge)\.`)

// braceVarRE matches ${NAME}, which the emitter prints as $NAME; both spell the same variable.
var braceVarRE = regexp.MustCompile(`\$\{(\w+)\}`)

// actionlint reports what the linter finds in one GHA-family file.
func actionlint(t *testing.T, name string, data []byte) findings {
	t.Helper()
	out := findings{}
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
	return out
}

func (f findings) merge(o findings) {
	for l := range o {
		f[l] = true
	}
}

// problems lists what validate.File and, for GHA, actionlint find in one file. Shell and Python checks
// are off: they judge the scripts of the source repository, not the conversion.
func problems(t *testing.T, id, name string, data []byte) findings {
	t.Helper()
	out := findings{}
	if err := validate.File(id, name, data); err != nil && !errors.Is(err, validate.ErrNoSchema) {
		// The schema id carries a fixed file name that must not tell an included file from the merged one.
		out.add(braceVarRE.ReplaceAllString(strings.NewReplacer(append([]string{name, ""}, providerChecks[id].errReplace...)...).Replace(err.Error()), "$$$1"))
	}
	if lint := providerChecks[id].lintFindings; lint != nil {
		out.merge(lint(t, name, data))
	}
	return out
}

// sourceFiles lists the files of fsys that provider id reads.
func sourceFiles(t *testing.T, id string, fsys fs.FS) ([]string, error) {
	t.Helper()
	l, ok := prov(t, id).(provider.Layout)
	if !ok {
		t.Fatalf("provider %s does not implement provider.Layout", id)
	}
	return l.SourceFiles(fsys)
}

// sourceProblems are the complaints the unconverted files already draw; the schema and actionlint lag
// behind some real syntax, and a converter cannot fix that.
func sourceProblems(t *testing.T, f fixture) findings {
	t.Helper()
	out := findings{}
	root := os.DirFS(f.dir)
	rels, err := sourceFiles(t, f.provider, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range rels {
		data, err := fs.ReadFile(root, rel)
		if err != nil {
			t.Fatal(err)
		}
		out.merge(problems(t, f.provider, rel, data))
		if f.provider != provider.GHA && provider.Family(f.provider) == provider.GHA {
			// actionlint rejects the dialect context aliases and so hides findings that the converted github.* form draws.
			out.merge(actionlint(t, rel, dialectCtxRE.ReplaceAll(data, []byte("github."))))
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
		if providerChecks[id].noGHAExpr && strings.Contains(string(files[name]), exprOpen) {
			t.Errorf("%s holds a GHA expression", name)
		}
	}
}

func TestCorpus(t *testing.T) {
	for _, id := range lintedProviders() {
		providerChecks[id].requireLinter(t)
	}
	var summary bytes.Buffer
	healthy := true
	for _, f := range corpusEntries(t) {
		a := prov(t, f.provider)
		fsA := os.DirFS(f.dir)
		ok := t.Run(f.name+"/identity", func(t *testing.T) {
			src, _, err := a.Parse(fsA)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			again, _ := returnInto(t, a, src)
			if same, diff := ir.Equal(src, again); !same {
				t.Errorf("A->A differs:\n%s", diff)
			}
			self, _, err := a.Emit(src)
			if err != nil {
				t.Fatalf("identity emit: %v", err)
			}
			checkEmitted(t, a.ID(), self, sourceProblems(t, f))
		})
		healthy = healthy && ok
		// A GHA-family target keeps the source's own linter findings, so only new ones fail.
		var srcKnown findings
		for _, to := range targets(f.provider) {
			crossKnown := findings{}
			if provider.Family(f.provider) == provider.GHA && provider.Family(to) == provider.GHA {
				if srcKnown == nil {
					srcKnown = sourceProblems(t, f)
				}
				crossKnown = srcKnown
			}
			ok := t.Run(f.name+"/to-"+to, func(t *testing.T) {
				b := prov(t, to)
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
				checkEmitted(t, b.ID(), cross, crossKnown)
				if f.provider == provider.Tekton && provider.Family(to) != provider.Tekton {
					checkShellSyntax(t, b.ID(), cross)
				}
				if to == provider.Concourse {
					checkConcourseShells(t, cross, r.toRep)
					checkPowershellReported(t, cross, r.toRep)
				}

				c := verdictCounts(r.src, r.toRep)
				fmt.Fprintf(&summary, "%s %s->%s native=%4d emulated=%4d external=%4d lost=%4d\n", f.name, a.ID(), b.ID(),
					c[caps.Native], c[caps.Emulated], c[caps.External], c[caps.Lost])
			})
			healthy = healthy && ok
		}
	}
	if !healthy {
		return
	}
	golden.Assert(t, filepath.Join(corpusRoot, corpusSummary), summary.Bytes())
}

// checkPowershellReported fails when a Concourse task runs under PowerShell with no emulated step.shell entry, because sh -n
// skips those tasks and the report must say why.
func checkPowershellReported(t *testing.T, files map[string][]byte, rep *report.Report) {
	t.Helper()
	tasks := 0
	for _, name := range names(files) {
		n, err := powershellTasks(files[name])
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		tasks += n
	}
	entries := 0
	for _, e := range rep.Entries {
		if e.Feature == caps.StepShell && e.Level == caps.Emulated && strings.Contains(e.Detail, "-Command") {
			entries++
		}
	}
	if entries < tasks {
		t.Errorf("%d PowerShell tasks but %d step.shell entries", tasks, entries)
	}
}

// checkConcourseShells runs the syntax check on every Concourse shell task. A task that fails is allowed only
// when the report has a lost step.shell entry for that exact task that says the script may be PowerShell.
func checkConcourseShells(t *testing.T, files map[string][]byte, rep *report.Report) {
	t.Helper()
	for _, name := range names(files) {
		tasks, err := concourseTasks(files[name])
		if err != nil {
			t.Errorf("%s: cannot read the emitted file: %v", name, err)
			continue
		}
		for _, tk := range tasks {
			msg := syntaxError(tk.shell, tk.script)
			if msg == "" || reportedPowershell(rep, tk) {
				continue
			}
			t.Errorf("%s: job %s task %s: %s -n rejects the script and the report does not say it may be PowerShell: %s\n%s", name, tk.job, tk.name, tk.shell, msg, tk.script)
		}
	}
}

// reportedPowershell is true when the report has a lost step.shell entry for the task that names PowerShell.
// The emitter renames IDs that Concourse does not accept, so both sides are compared in their written form.
func reportedPowershell(rep *report.Report, tk concourseTask) bool {
	for _, e := range rep.Entries {
		if e.Feature != caps.StepShell || e.Level != caps.Lost || !strings.Contains(e.Detail, "may be PowerShell") {
			continue
		}
		job, step, ok := strings.Cut(strings.TrimPrefix(e.IRPath[strings.Index(e.IRPath, "/job/")+1:], "job/"), "/step/")
		if ok && writtenName(job) == tk.job && writtenName(step) == tk.name {
			return true
		}
	}
	return false
}

var unwrittenRE = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// writtenName is an ID as the Concourse emitter writes it.
func writtenName(id string) string {
	return strings.ToLower(strings.Trim(unwrittenRE.ReplaceAllString(id, "-"), "-"))
}
