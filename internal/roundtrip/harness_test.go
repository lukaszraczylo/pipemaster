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
	_ "github.com/lukaszraczylo/pipemaster/provider/gha"
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
	root := filepath.Join(featuresRoot, id, "features")
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

func allFixtures(t *testing.T) []fixture {
	return append(fixtures(t, provider.GHA), fixtures(t, provider.GitLab)...)
}

func other(id string) string {
	if id == provider.GHA {
		return provider.GitLab
	}
	return provider.GHA
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
		lm, err := meta.Load(r.toFS)
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
