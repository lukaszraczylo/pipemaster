package roundtrip_test

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
