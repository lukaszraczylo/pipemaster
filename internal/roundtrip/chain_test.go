package roundtrip_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
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
	t.Run("1 empty stack makes one new layer", func(t *testing.T) {
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
			!hasEntry(rep, caps.Native, "kept 1 metadata entries from gitlab; they restore on conversion to gitlab") {
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
