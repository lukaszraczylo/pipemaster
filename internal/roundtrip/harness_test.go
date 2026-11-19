package roundtrip_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
	_ "github.com/lukaszraczylo/pipemaster/provider/gitlab"
	"github.com/lukaszraczylo/pipemaster/report"
)

const featuresRoot = "../../testdata"

type fixture struct {
	provider string
	name     string
	dir      string
}

func fixtures(t *testing.T, id string) []fixture {
	t.Helper()
	if provider.Family(id) == provider.GHA && id != provider.GHA {
		return dialectFixtures(t, id)
	}
	return readFixtures(t, id, id)
}

func readFixtures(t *testing.T, id, dir string) []fixture {
	t.Helper()
	root := filepath.Join(featuresRoot, dir, "features")
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []fixture
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, fixture{id, e.Name(), filepath.Join(root, e.Name())})
		}
	}
	return out
}

// dialectFixtures returns the GHA fixtures remapped to the dialect's emit directory, then the
// dialect-only fixtures under testdata/<id>/features when that directory exists.
func dialectFixtures(t *testing.T, id string) []fixture {
	t.Helper()
	d, ok := gha.Dialects[id]
	if !ok {
		t.Fatalf("no dialect %s", id)
	}
	base := readFixtures(t, id, provider.GHA)
	out := make([]fixture, 0, len(base))
	for _, f := range base {
		out = append(out, fixture{id, f.name, remapDir(t, f.dir, d.Dirs[0])})
	}
	if _, err := os.Stat(filepath.Join(featuresRoot, id, "features")); err == nil {
		out = append(out, readFixtures(t, id, id)...)
	}
	return out
}

// remapDir copies a GHA fixture into a temp dir with .github/workflows moved to dir.
func remapDir(t *testing.T, src, dir string) string {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(dst, ".github", "workflows")
	if _, err := os.Stat(from); err != nil {
		return dst
	}
	to := filepath.Join(dst, filepath.FromSlash(dir))
	if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil { // #nosec G301 -- temp dir
		t.Fatal(err)
	}
	if err := os.Rename(from, to); err != nil { // #nosec G703 -- paths are under t.TempDir
		t.Fatal(err)
	}
	return dst
}

func allFixtures(t *testing.T) []fixture {
	return append(fixtures(t, provider.GHA), fixtures(t, provider.GitLab)...)
}

// targets returns every registered provider except id.
func targets(id string) []string {
	var out []string
	for _, p := range provider.IDs() {
		if p != id {
			out = append(out, p)
		}
	}
	return out
}

// other picks the base-adapter target of another family; dialects wait for the per-target tests.
func other(id string) string {
	for _, p := range targets(id) {
		if !provider.SameFamily(p, id) && provider.Family(p) == p {
			return p
		}
	}
	return targets(id)[0]
}

func prov(t *testing.T, id string) provider.Provider {
	t.Helper()
	p, err := provider.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func toFS(files map[string][]byte) fstest.MapFS {
	out := fstest.MapFS{}
	for n, b := range files {
		out[n] = &fstest.MapFile{Data: b}
	}
	return out
}

func names(files map[string][]byte) []string {
	var out []string
	for n := range files {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

type hopResult struct {
	src     *ir.Project
	back    *ir.Project
	toFiles map[string][]byte
	toRep   *report.Report
	toFS    fstest.MapFS
	backRep *report.Report
	srcRep  *report.Report
}

// hops runs A -> B (with metadata, sidecar or embedded) -> A and returns both projects.
func hops(t *testing.T, fsA fs.FS, a, b provider.Provider, embed bool, withMeta bool) hopResult {
	t.Helper()
	var r hopResult
	var err error
	r.src, r.srcRep, err = a.Parse(fsA)
	if err != nil {
		t.Fatalf("parse %s: %v", a.ID(), err)
	}
	var rep *report.Report
	r.toFiles, rep, err = b.Emit(r.src)
	if err != nil {
		t.Fatalf("emit %s: %v", b.ID(), err)
	}
	r.toRep = rep
	m := roundtrip.NewMeta(a.ID(), r.src, b, rep)
	switch {
	case !withMeta:
	case embed:
		r.toFiles = meta.Embed(r.toFiles, m)
	default:
		r.toFiles = meta.Attach(r.toFiles, m)
	}
	r.toFS = toFS(r.toFiles)
	mid, _, err := b.Parse(r.toFS)
	if err != nil {
		t.Fatalf("parse %s: %v", b.ID(), err)
	}
	if withMeta {
		lm, err := meta.Load(r.toFS, mid.Origin.Files)
		if err != nil || lm == nil {
			t.Fatalf("load meta: %v %v", lm, err)
		}
		if stale, err := lm.Stale(r.toFS); err != nil {
			t.Fatalf("stale %v: %v", stale, err)
		}
		roundtrip.Restore(mid, lm)
	}
	files, rep2, err := a.Emit(mid)
	if err != nil {
		t.Fatalf("emit %s back: %v", a.ID(), err)
	}
	r.backRep = rep2
	r.back, _, err = a.Parse(toFS(files))
	if err != nil {
		t.Fatalf("parse %s back: %v", a.ID(), err)
	}
	return r
}
