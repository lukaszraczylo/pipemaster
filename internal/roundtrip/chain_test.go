package roundtrip_test

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/cli"
	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/validate"
)

func layer(source, key string, val any) *meta.Meta {
	return &meta.Meta{Source: source, Ext: map[string]ir.Ext{"pipeline/main": {key: val}}}
}

func chainProject() *ir.Project {
	return &ir.Project{Pipelines: []*ir.Pipeline{{ID: "main"}}}
}

func hasEntry(rep *report.Report, level caps.Level, detail string) bool {
	for _, e := range rep.Entries {
		if e.Feature == caps.ExtPassthrough && e.Level == level && e.Detail == detail {
			return true
		}
	}
	return false
}

func TestHopRules(t *testing.T) {
	staleFS := fstest.MapFS{"b.yml": {Data: []byte("edited")}}
	stale := func(m *meta.Meta) *meta.Meta {
		m.Files = map[string]string{"b.yml": "0000"}
		return m
	}
	t.Run("1 empty stack restores nothing and leaves the layers to the caller", func(t *testing.T) {
		rep := &report.Report{}
		res, err := roundtrip.Hop(chainProject(), &meta.Stack{}, fstest.MapFS{}, "gha", "gitlab", false, rep)
		if err != nil || len(res.Out.Layers) != 0 || res.Restored || len(rep.Entries) != 0 {
			t.Fatalf("res=%+v err=%v entries=%v", res, err, rep.Entries)
		}
	})
	t.Run("2 stale without force is ErrStale", func(t *testing.T) {
		in := &meta.Stack{Layers: []*meta.Meta{layer("gha", "a", 1), stale(layer("gitlab", "b", 2))}}
		_, err := roundtrip.Hop(chainProject(), in, staleFS, "gitea", "gha", false, &report.Report{})
		if !errors.Is(err, meta.ErrStale) || !strings.Contains(err.Error(), "b.yml") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("2 stale with force and target is top source keeps edits and drops older layers", func(t *testing.T) {
		in := &meta.Stack{Layers: []*meta.Meta{layer("gha", "a", 1), stale(layer("gitlab", "b", 2))}}
		p, rep := chainProject(), &report.Report{}
		res, err := roundtrip.Hop(p, in, staleFS, "gitea", "gitlab", true, rep)
		if err != nil || res.Restored || len(res.Out.Layers) != 0 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		ext := p.Pipelines[0].Ext
		if ext["b"] != 2 || ext["a"] != nil {
			t.Fatalf("pipeline ext = %v, want only the top layer applied", ext)
		}
		if !hasEntry(rep, caps.Lost, "metadata from gha dropped: b.yml was edited (--force)") || len(rep.Entries) != 1 {
			t.Fatalf("entries = %+v", rep.Entries)
		}
	})
	t.Run("2 stale with force and other target drops every layer", func(t *testing.T) {
		in := &meta.Stack{Layers: []*meta.Meta{layer("gha", "a", 1), stale(layer("gitlab", "b", 2))}}
		p, rep := chainProject(), &report.Report{}
		res, err := roundtrip.Hop(p, in, staleFS, "gitea", "forgejo", true, rep)
		if err != nil || res.Restored || len(res.Out.Layers) != 0 || p.Pipelines[0].Ext != nil {
			t.Fatalf("res=%+v err=%v ext=%v", res, err, p.Pipelines[0].Ext)
		}
		for _, src := range []string{"gha", "gitlab"} {
			if !hasEntry(rep, caps.Lost, "metadata from "+src+" dropped: b.yml was edited (--force)") {
				t.Errorf("no dropped entry for %s: %+v", src, rep.Entries)
			}
		}
	})
	t.Run("3 unwinds every layer down to the match", func(t *testing.T) {
		in := &meta.Stack{Layers: []*meta.Meta{layer("gha", "a", 1), layer("gitlab", "b", 2)}}
		p := chainProject()
		res, err := roundtrip.Hop(p, in, fstest.MapFS{}, "gitea", "gha", false, &report.Report{})
		if err != nil || !res.Restored || len(res.Out.Layers) != 0 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if ext := p.Pipelines[0].Ext; ext["a"] != 1 || ext["b"] != 2 || p.Origin.Provider != "gha" {
			t.Fatalf("ext=%v origin=%v", ext, p.Origin)
		}
	})
	t.Run("3 stops at a middle match and keeps the older layer", func(t *testing.T) {
		in := &meta.Stack{Layers: []*meta.Meta{layer("gha", "a", 1), layer("gitlab", "b", 2)}}
		p := chainProject()
		res, err := roundtrip.Hop(p, in, fstest.MapFS{}, "gitea", "gitlab", false, &report.Report{})
		if err != nil || !res.Restored || len(res.Out.Layers) != 1 || res.Out.Layers[0].Source != "gha" {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if ext := p.Pipelines[0].Ext; ext["a"] != nil || ext["b"] != 2 || p.Origin.Provider != "gitlab" {
			t.Fatalf("ext=%v origin=%v", ext, p.Origin)
		}
	})
	t.Run("4 no match keeps every layer and reports each", func(t *testing.T) {
		g := layer("gha", "a", 1)
		g.Lost = map[string]meta.Lost{"x": {}}
		in := &meta.Stack{Layers: []*meta.Meta{g, layer("gitlab", "b", 2)}}
		p, rep := chainProject(), &report.Report{}
		res, err := roundtrip.Hop(p, in, fstest.MapFS{}, "gitea", "forgejo", false, rep)
		if err != nil || res.Restored || len(res.Out.Layers) != 2 || p.Pipelines[0].Ext != nil {
			t.Fatalf("res=%+v err=%v ext=%v", res, err, p.Pipelines[0].Ext)
		}
		if !hasEntry(rep, caps.Native, "kept 2 metadata entries from gha; they restore on conversion to gha") ||
			!hasEntry(rep, caps.Native, "kept 1 metadata entry from gitlab; they restore on conversion to gitlab") {
			t.Fatalf("entries = %+v", rep.Entries)
		}
	})
}

func runOut(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = cli.Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func parseDir(t *testing.T, id, dir string) *ir.Project {
	t.Helper()
	p, _, err := prov(t, id).Parse(os.DirFS(dir))
	if err != nil {
		t.Fatalf("parse %s %s: %v", id, dir, err)
	}
	return p
}

func TestChainGhaGitlabGiteaGha(t *testing.T) {
	for _, f := range fixtures(t, provider.GHA) {
		t.Run(f.name, func(t *testing.T) {
			d1, d2, d3 := t.TempDir(), t.TempDir(), t.TempDir()
			for _, step := range [][]string{
				{f.dir, "--to", "gitlab", "-o", d1},
				{d1, "--to", "gitea", "-o", d2},
				{d2, "--to", "gha", "-o", d3},
			} {
				if code, _, msg := runOut(append([]string{"convert"}, step...)...); code != 0 {
					t.Fatalf("convert %v = %d\n%s", step, code, msg)
				}
			}
			if same, diff := ir.Equal(parseDir(t, provider.GHA, f.dir), parseDir(t, provider.GHA, d3)); !same {
				t.Fatalf("chain changed the pipeline:\n%s", diff)
			}
			if _, err := os.Stat(filepath.Join(d3, meta.SidecarPath)); err == nil {
				t.Fatal("a full return must write no sidecar")
			}
		})
	}
}

func TestChainEditedMiddle(t *testing.T) {
	src := filepath.Join(featuresRoot, "gha", "features", "job.env")
	b, c := t.TempDir(), t.TempDir()
	if code, _, msg := runOut("convert", src, "--to", "gitlab", "-o", b); code != 0 {
		t.Fatalf("A to B = %d\n%s", code, msg)
	}
	file := filepath.Join(b, ".gitlab-ci.yml")
	data, err := os.ReadFile(file) // #nosec G304 -- temp dir path
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(data, "\n# edited by hand\n"...), 0o600); err != nil { // #nosec G703 -- path under t.TempDir
		t.Fatal(err)
	}
	if code, _, msg := runOut("convert", b, "--to", "gitea", "-o", c); code != 4 || !strings.Contains(msg, ".gitlab-ci.yml") {
		t.Fatalf("B to C without --force = %d, want 4 naming the file\n%s", code, msg)
	}
	code, _, msg := runOut("convert", b, "--to", "gitea", "-o", c, "--force")
	if code != 0 || !strings.Contains(msg, "metadata from gha dropped: .gitlab-ci.yml was edited (--force)") {
		t.Fatalf("B to C with --force = %d, want 0 and a dropped entry\n%s", code, msg)
	}
	s, err := meta.LoadStack(os.DirFS(c), nil)
	if err != nil || len(s.Layers) != 1 || s.Top().Source != "gitlab" {
		t.Fatalf("stack = %+v err=%v, want one layer from gitlab", s, err)
	}
}

// The fixture holds the Phase 1 output for testdata/gha/features/config.name.
func TestDirectReturnV1Sidecar(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	if err := os.CopyFS(src, os.DirFS("../../testdata/meta/v1-sidecar")); err != nil {
		t.Fatal(err)
	}
	if code, _, msg := runOut("convert", src, "--to", "gha", "-o", out); code != 0 {
		t.Fatalf("convert = %d\n%s", code, msg)
	}
	want := parseDir(t, provider.GHA, filepath.Join(featuresRoot, "gha", "features", "config.name"))
	if same, diff := ir.Equal(want, parseDir(t, provider.GHA, out)); !same {
		t.Fatalf("not an exact return:\n%s", diff)
	}
	if _, err := os.Stat(filepath.Join(out, meta.SidecarPath)); err == nil {
		t.Fatal("a return to the origin must write no sidecar")
	}
}

func TestRoundtripViaList(t *testing.T) {
	for _, f := range fixtures(t, provider.GHA) {
		t.Run(f.name, func(t *testing.T) {
			code, out, msg := runOut("roundtrip", f.dir, "--via", "gitlab,gitea")
			if code != 0 || out != "no differences\n" {
				t.Fatalf("code=%d out=%q\n%s", code, out, msg)
			}
		})
	}
}

func appendTo(t *testing.T, file, text string) {
	t.Helper()
	data, err := os.ReadFile(file) // #nosec G304 -- temp dir path
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(data, text...), 0o600); err != nil { // #nosec G703 -- path under t.TempDir
		t.Fatal(err)
	}
}

func TestReturnRemovesStaleSidecar(t *testing.T) {
	x, z := t.TempDir(), t.TempDir()
	src := filepath.Join(featuresRoot, "gha", "features", "job.env")
	if code, _, msg := runOut("convert", src, "--to", "gitlab", "-o", x); code != 0 {
		t.Fatalf("A to B = %d\n%s", code, msg)
	}
	code, _, msg := runOut("convert", x, "--to", "gha", "-o", x)
	if code != 0 || !strings.Contains(msg, "removed the stale round-trip sidecar") {
		t.Fatalf("in-place return = %d, want 0 and a removal entry\n%s", code, msg)
	}
	if _, err := os.Stat(filepath.Join(x, ".pipemaster")); err == nil {
		t.Fatal("the empty .pipemaster directory must go with the sidecar")
	}
	appendTo(t, filepath.Join(x, ".gitlab-ci.yml"), "\n# edited by hand\n")
	if code, _, msg := runOut("convert", x, "--from", "gha", "--to", "forgejo", "-o", z); code != 0 {
		t.Fatalf("later gha conversion = %d, want 0 over an unrelated edit\n%s", code, msg)
	}
}

func TestReturnKeepsOtherSidecarDirContent(t *testing.T) {
	x := t.TempDir()
	src := filepath.Join(featuresRoot, "gha", "features", "job.env")
	if code, _, msg := runOut("convert", src, "--to", "gitlab", "-o", x); code != 0 {
		t.Fatalf("A to B = %d\n%s", code, msg)
	}
	keep := filepath.Join(x, ".pipemaster", "notes.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil { // #nosec G703 -- path under t.TempDir
		t.Fatal(err)
	}
	if code, _, msg := runOut("convert", x, "--to", "gha", "-o", x); code != 0 {
		t.Fatalf("return = %d\n%s", code, msg)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("only the sidecar may go: %v", err)
	}
}

func hasBlock(t *testing.T, dir string) bool {
	t.Helper()
	found := false
	fsys := os.DirFS(dir)
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := fs.ReadFile(fsys, p)
		if rerr != nil {
			return rerr
		}
		found = found || strings.Contains(string(b), "pipemaster:meta:begin")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestEmbeddedReturnLeavesNoBlock(t *testing.T) {
	b, a := t.TempDir(), t.TempDir()
	src := filepath.Join(featuresRoot, "gha", "features", "job.env")
	if code, _, msg := runOut("convert", src, "--to", "gitlab", "--embed-meta", "-o", b); code != 0 {
		t.Fatalf("A to B = %d\n%s", code, msg)
	}
	if !hasBlock(t, b) {
		t.Fatal("the outbound hop must embed a block")
	}
	if code, _, msg := runOut("convert", b, "--to", "gha", "--embed-meta", "-o", a); code != 0 {
		t.Fatalf("return = %d\n%s", code, msg)
	}
	if hasBlock(t, a) {
		t.Fatal("a full return must leave no embedded block")
	}
}

type chainStep struct {
	to     string
	layers []string // sources of the stack the step's output holds, oldest first
}

func stackSources(t *testing.T, id, dir string) []string {
	t.Helper()
	s, err := meta.LoadStack(os.DirFS(dir), parseDir(t, id, dir).Origin.Files)
	if err != nil {
		t.Fatalf("load stack %s: %v", dir, err)
	}
	out := []string{}
	for _, m := range s.Layers {
		out = append(out, m.Source)
	}
	return out
}

// Every hop must exit 0, so a partial unwind leaves a top layer whose hashes match the new output.
func TestChainShapes(t *testing.T) {
	chains := map[string][]chainStep{
		"A-B-C-B-A":   {{"gitlab", []string{"gha"}}, {"gitea", []string{"gha", "gitlab"}}, {"gitlab", []string{"gha"}}, {"gha", nil}},
		"A-B-A-B-A":   {{"gitlab", []string{"gha"}}, {"gha", nil}, {"gitlab", []string{"gha"}}, {"gha", nil}},
		"A-gitea-F-A": {{"gitea", []string{"gha"}}, {"forgejo", []string{"gha", "gitea"}}, {"gha", nil}},
	}
	for name, steps := range chains {
		for _, embed := range []bool{false, true} {
			for _, f := range fixtures(t, provider.GHA) {
				t.Run(fmt.Sprintf("%s/embed=%t/%s", name, embed, f.name), func(t *testing.T) {
					prev, from := f.dir, provider.GHA
					for i, st := range steps {
						out := t.TempDir()
						args := []string{"convert", prev, "--from", from, "--to", st.to, "-o", out}
						if embed {
							args = append(args, "--embed-meta")
						}
						if code, _, msg := runOut(args...); code != 0 {
							t.Fatalf("step %d to %s = %d\n%s", i+1, st.to, code, msg)
						}
						if got := stackSources(t, st.to, out); !slices.Equal(got, st.layers) {
							t.Fatalf("step %d stack = %v, want %v", i+1, got, st.layers)
						}
						prev, from = out, st.to
					}
					if same, diff := ir.Equal(parseDir(t, provider.GHA, f.dir), parseDir(t, provider.GHA, prev)); !same {
						t.Fatalf("chain changed the pipeline:\n%s", diff)
					}
					if hasBlock(t, prev) {
						t.Fatal("a full return must leave no embedded block")
					}
				})
			}
		}
	}
}

func TestChainForceToTopSource(t *testing.T) {
	src := filepath.Join(featuresRoot, "gha", "features", "job.env")
	b, c, d := t.TempDir(), t.TempDir(), t.TempDir()
	for _, step := range [][]string{{src, "--to", "gitlab", "-o", b}, {b, "--to", "gitea", "-o", c}} {
		if code, _, msg := runOut(append([]string{"convert"}, step...)...); code != 0 {
			t.Fatalf("convert %v = %d\n%s", step, code, msg)
		}
	}
	edited, err := filepath.Glob(filepath.Join(c, ".gitea", "workflows", "*.yml"))
	if err != nil || len(edited) != 1 {
		t.Fatalf("gitea workflow files = %v, %v", edited, err)
	}
	appendTo(t, edited[0], "\n# edited by hand\n")
	code, _, msg := runOut("convert", c, "--to", "gitlab", "-o", d, "--force")
	if code != 0 || !strings.Contains(msg, "metadata from gha dropped") || strings.Contains(msg, "metadata from gitlab dropped") {
		t.Fatalf("force to the top layer's source = %d, want 0, the gha layer dropped and the gitlab layer applied\n%s", code, msg)
	}
	if got := stackSources(t, "gitlab", d); !slices.Equal(got, []string{"gitea"}) {
		t.Fatalf("stack = %v, want one layer from gitea", got)
	}
}

func TestEmbedConversionRemovesOldSidecar(t *testing.T) {
	src := filepath.Join(featuresRoot, "gha", "features", "job.env")
	b, x := t.TempDir(), t.TempDir()
	if code, _, msg := runOut("convert", src, "--to", "gitlab", "-o", b); code != 0 {
		t.Fatalf("A to B = %d\n%s", code, msg)
	}
	if code, _, msg := runOut("convert", b, "--to", "gitea", "-o", x); code != 0 {
		t.Fatalf("B to C = %d\n%s", code, msg)
	}
	code, _, msg := runOut("convert", src, "--to", "gitlab", "--embed-meta", "-o", x)
	if code != 0 || !strings.Contains(msg, "this conversion embeds its metadata") {
		t.Fatalf("embed into a dir with a sidecar = %d, want 0 and a removal entry\n%s", code, msg)
	}
	if _, err := os.Stat(filepath.Join(x, meta.SidecarPath)); err == nil {
		t.Fatal("the old sidecar must go")
	}
	if got := stackSources(t, "gitlab", x); !slices.Equal(got, []string{"gha"}) {
		t.Fatalf("next load = %v, want the embedded stack [gha]", got)
	}
}

// chainsFrom lists every A to B to C to A chain that starts at a: B differs from A, C from B and A.
func chainsFrom(a string) [][2]string {
	var out [][2]string
	for _, b := range targets(a) {
		for _, c := range targets(b) {
			if c != a {
				out = append(out, [2]string{b, c})
			}
		}
	}
	return out
}

// sampleChains keeps one chain per middle hop under -short: for the source at position src in the registry,
// the middle hop's group yields its chain number (group+src) mod group size. Every provider is then a middle
// hop of the sources that differ from it; TestChainSampleCoversEveryProvider checks all three roles.
func sampleChains(src int, chains [][2]string) [][2]string {
	var out [][2]string
	for lo := 0; lo < len(chains); {
		hi := lo
		for hi < len(chains) && chains[hi][0] == chains[lo][0] {
			hi++
		}
		out = append(out, chains[lo+(len(out)+src)%(hi-lo)])
		lo = hi
	}
	return out
}

// chainInputs is every feature fixture and corpus entry written in provider a.
func chainInputs(t *testing.T, a string) []fixture {
	t.Helper()
	out := fixtures(t, a)
	for _, e := range corpusEntries(t) {
		if e.provider == a {
			out = append(out, e)
		}
	}
	return out
}

// outputFiles reads every converted file, leaving out the sidecar directory.
func outputFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	root := os.DirFS(dir)
	err := fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasPrefix(p, ".pipemaster/") {
			return nil
		}
		b, rerr := fs.ReadFile(root, p)
		files[p] = b
		return rerr
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// checkReturn asserts that dir, the output of a full return into a, holds the pipeline parsed from src. A
// dialect that cannot write every key keeps the rest in metadata of its own, which the check applies;
// GHA writes every key, so its return carries no metadata at all.
func checkReturn(t *testing.T, a, src, dir string) {
	t.Helper()
	final := parseDir(t, a, dir)
	stack, err := meta.LoadStack(os.DirFS(dir), final.Origin.Files)
	if err != nil {
		t.Fatalf("load stack: %v", err)
	}
	if len(stack.Layers) > 0 {
		if a == provider.GHA {
			t.Fatal("a full return into GHA must carry no metadata")
		}
		if got := stackSources(t, a, dir); !slices.Equal(got, slices.Repeat([]string{a}, 1)) {
			t.Fatalf("a full return keeps one layer of its own provider, got %v", got)
		}
		res, err := roundtrip.Hop(final, stack, os.DirFS(dir), a, a, false, &report.Report{})
		if err != nil {
			t.Fatalf("restore own layer: %v", err)
		}
		final = res.Proj
	}
	if same, diff := ir.Equal(parseDir(t, a, src), final); !same {
		t.Fatalf("chain changed the pipeline:\n%s", diff)
	}
}

// checkValid fails when a hop's output does not pass the pinned schema of its provider.
func checkValid(t *testing.T, id string, files map[string][]byte) {
	t.Helper()
	for name, data := range files {
		if err := validate.File(id, name, data); err != nil && !errors.Is(err, validate.ErrNoSchema) {
			t.Errorf("validate %s %s: %v", id, name, err)
		}
	}
}

const schemaWarning = "warning: restored origin does not validate against the pinned schema"

// checkFinalSchema asserts that the final output fails the pinned schema, and warns, only for a known entry.
func checkFinalSchema(t *testing.T, a, name, dir, stderr string) {
	t.Helper()
	invalid := false
	for file, data := range outputFiles(t, dir) {
		if err := validate.File(a, file, data); err != nil && !errors.Is(err, validate.ErrNoSchema) {
			invalid = true
		}
	}
	lag := providerChecks[a].schemaLag
	known := slices.Contains(lag, name)
	switch warned := strings.Contains(stderr, schemaWarning); {
	case invalid && !known:
		t.Fatalf("final output of %s fails the schema, and only %v may", name, lag)
	case invalid && !warned:
		t.Fatalf("final output fails the schema without a warning\n%s", stderr)
	case !invalid && warned:
		t.Fatalf("a warning for an output that validates\n%s", stderr)
	}
}

func TestChainMatrix(t *testing.T) {
	total := 0
	for i, a := range provider.IDs() {
		chains := chainsFrom(a)
		if testing.Short() || raceEnabled {
			chains = sampleChains(i, chains)
		}
		inputs := chainInputs(t, a)
		for _, f := range inputs {
			for _, ch := range chains {
				route := []string{a, ch[0], ch[1], a}
				for _, embed := range []bool{false, true} {
					total++
					name := fmt.Sprintf("%s/embed=%t/%s", strings.Join(route, "-"), embed, f.name)
					t.Run(name, func(t *testing.T) {
						t.Parallel()
						prev, lastMsg := f.dir, ""
						for i := 1; i < len(route); i++ {
							out := t.TempDir()
							args := []string{"convert", prev, "--from", route[i-1], "--to", route[i], "-o", out}
							if embed {
								args = append(args, "--embed-meta")
							}
							code, _, msg := runOut(args...)
							if code != 0 {
								t.Fatalf("hop %d to %s = %d\n%s", i, route[i], code, msg)
							}
							lastMsg = msg
							if i < len(route)-1 {
								checkValid(t, route[i], outputFiles(t, out))
							}
							prev = out
						}
						checkFinalSchema(t, a, f.name, prev, lastMsg)
						checkReturn(t, a, f.dir, prev)
					})
				}
			}
		}
	}
	if total == 0 {
		t.Fatal("no chains ran")
	}
}

func TestChainSampleCoversEveryProvider(t *testing.T) {
	roles := map[string][3]bool{}
	for i, a := range provider.IDs() {
		all := chainsFrom(a)
		sample := sampleChains(i, all)
		if len(sample) == 0 || len(sample) >= len(all) {
			t.Fatalf("sample of %s has %d of %d chains, want a strict, non-empty subset", a, len(sample), len(all))
		}
		if !slices.Equal(sample, sampleChains(i, all)) {
			t.Fatalf("sample of %s changes between calls", a)
		}
		r := roles[a]
		r[0] = true
		roles[a] = r
		for _, ch := range sample {
			if !slices.Contains(all, ch) {
				t.Fatalf("sampled chain %v is not a chain from %s", ch, a)
			}
			rb, rc := roles[ch[0]], roles[ch[1]]
			rb[1], rc[2] = true, true
			roles[ch[0]], roles[ch[1]] = rb, rc
		}
	}
	for _, id := range provider.IDs() {
		if r := roles[id]; !r[0] || !r[1] || !r[2] {
			t.Errorf("%s as source, middle, target = %v, want all three", id, r)
		}
	}
}

func TestFullReturnIntoDialectKeepsWhatItCannotWrite(t *testing.T) {
	src := t.TempDir()
	wf := filepath.Join(src, ".gitea", "workflows")
	if err := os.MkdirAll(wf, 0o750); err != nil {
		t.Fatal(err)
	}
	const body = "on: push\npermissions:\n  contents: read\n  id-token: write\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo ok\n"
	if err := os.WriteFile(filepath.Join(wf, "ci.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	b, a := t.TempDir(), t.TempDir()
	for _, step := range [][]string{
		{src, "--from", "gitea", "--to", "gitlab", "-o", b},
		{b, "--from", "gitlab", "--to", "gitea", "-o", a},
	} {
		if code, _, msg := runOut(append([]string{"convert"}, step...)...); code != 0 {
			t.Fatalf("convert %v = %d\n%s", step, code, msg)
		}
	}
	if got := stackSources(t, provider.Gitea, a); !slices.Equal(got, []string{provider.Gitea}) {
		t.Fatalf("Gitea cannot write id-token, so the return keeps a layer of its own: got %v", got)
	}
	checkReturn(t, provider.Gitea, src, a)
}

func TestFullReturnWarnsWhenOriginFailsSchema(t *testing.T) {
	src := filepath.Join(corpusRoot, "24-gitlab-release-cli")
	b, a := t.TempDir(), t.TempDir()
	if code, _, msg := runOut("convert", src, "--from", "gitlab", "--to", "gha", "-o", b); code != 0 {
		t.Fatalf("out = %d\n%s", code, msg)
	}
	code, _, msg := runOut("convert", b, "--from", "gha", "--to", "gitlab", "-o", a)
	if code != 0 || !strings.Contains(msg, "warning: restored origin does not validate against the pinned schema") {
		t.Fatalf("return = %d, want 0 with a schema warning\n%s", code, msg)
	}
}

// returnVia converts the corpus entry to gha and back with extra flags on the return. It edits the sidecar of
// the middle output with edit when edit is not nil.
func returnVia(t *testing.T, edit func(string) string, flags ...string) (code int, stdout, stderr string) {
	t.Helper()
	b, a := t.TempDir(), t.TempDir()
	if c, _, msg := runOut("convert", filepath.Join(corpusRoot, "24-gitlab-release-cli"), "--from", "gitlab", "--to", "gha", "-o", b); c != 0 {
		t.Fatalf("out = %d\n%s", c, msg)
	}
	if edit != nil {
		p := filepath.Join(b, meta.SidecarPath)
		raw, err := os.ReadFile(p) // #nosec G304 -- temp dir
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(edit(string(raw))), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return runOut(append([]string{"convert", b, "--from", "gha", "--to", "gitlab", "-o", a}, flags...)...)
}

func TestFullReturnSchemaWarningIsInTheReport(t *testing.T) {
	code, _, msg := returnVia(t, nil, "--report", "json")
	if code != 0 || !strings.Contains(msg, `"detail": "restored origin does not validate`) && !strings.Contains(msg, `"detail":"restored origin does not validate`) {
		t.Fatalf("return = %d, want 0 and a report entry\n%s", code, msg)
	}
	if code, _, msg := returnVia(t, nil, "--strict"); code != 3 {
		t.Fatalf("--strict = %d, want 3: the downgraded warning is not native\n%s", code, msg)
	}
}

func TestFullReturnSchemaFailureNeedsTheOriginToHaveFailed(t *testing.T) {
	dropRecord := func(s string) string {
		return regexp.MustCompile(`(?m)^      source_invalid:\n(?:        - .*\n)+`).ReplaceAllString(s, "")
	}
	if code, _, msg := returnVia(t, dropRecord); code != 1 || !strings.Contains(msg, "invalid output") {
		t.Fatalf("no recorded origin failure: code = %d, want 1\n%s", code, msg)
	}
	bogus := func(s string) string {
		return strings.Replace(dropRecord(s), "      source: gitlab\n", "      source_invalid:\n        - '- at ''/nowhere'': unrelated'\n      source: gitlab\n", 1)
	}
	if code, _, msg := returnVia(t, bogus); code != 1 || !strings.Contains(msg, "invalid output") {
		t.Fatalf("a different recorded failure: code = %d, want 1\n%s", code, msg)
	}
}

// writeGitlab writes a one-file GitLab project and returns its directory.
func writeGitlab(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitlab-ci.yml"), []byte(src), 0o600); err != nil { // #nosec G703 -- path under t.TempDir
		t.Fatal(err)
	}
	return dir
}

const gitlabRetryOne = "build:\n  script: [echo hi]\n  retry:\n    max: 0\n"

// A GitLab `retry: {max: 0}` is one attempt. Argo writes it as `limit: "0"`, so the return must keep it.
func TestRetryOneThroughArgo(t *testing.T) {
	t.Run("with metadata", func(t *testing.T) {
		code, out, msg := runOut("roundtrip", writeGitlab(t, gitlabRetryOne), "--via", "argo")
		if code != 0 || out != "no differences\n" {
			t.Fatalf("code=%d out=%q\n%s", code, out, msg)
		}
	})
	t.Run("without metadata", func(t *testing.T) {
		src, mid, back := writeGitlab(t, gitlabRetryOne), t.TempDir(), t.TempDir()
		if c, _, msg := runOut("convert", src, "--from", "gitlab", "--to", "argo", "-o", mid); c != 0 {
			t.Fatalf("to argo = %d\n%s", c, msg)
		}
		if err := os.RemoveAll(filepath.Join(mid, ".pipemaster")); err != nil {
			t.Fatal(err)
		}
		if c, _, msg := runOut("convert", mid, "--from", "argo", "--to", "gitlab", "-o", back); c != 0 {
			t.Fatalf("to gitlab = %d\n%s", c, msg)
		}
		got, err := os.ReadFile(filepath.Join(back, ".gitlab-ci.yml")) // #nosec G304 -- temp dir
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got), "max: 0") {
			t.Fatalf("the one attempt is gone:\n%s", got)
		}
	})
}
