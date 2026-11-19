package meta

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func project() *ir.Project {
	return &ir.Project{Pipelines: []*ir.Pipeline{{
		ID:       "ci",
		Ext:      ir.Ext{"gha": map[string]any{"run-name": "x"}},
		Triggers: []ir.Trigger{{}, {Ext: ir.Ext{"gha": map[string]any{"types": []any{"opened"}}}}},
		Jobs: []*ir.Job{{ID: "build", Steps: []*ir.Step{
			{ID: "s1", Ext: ir.Ext{"gha": map[string]any{"id": "one"}}},
			{ID: "s2"},
		}}},
		Finally: []*ir.Job{{ID: "notify", Ext: ir.Ext{"gha": map[string]any{"if": "always()"}}}},
	}}}
}

func testMeta() *Meta {
	return &Meta{
		Version: Version, Source: "gha", Dialect: "github",
		Ext: Collect(project()),
		IDs: map[string]string{"build-linux": "build linux"},
	}
}

func asFS(files map[string][]byte) fstest.MapFS {
	fsys := fstest.MapFS{}
	for k, v := range files {
		fsys[k] = &fstest.MapFile{Data: v}
	}
	return fsys
}

func TestCollect(t *testing.T) {
	got := Collect(project())
	want := []string{
		"pipeline/ci", "pipeline/ci/trigger/1", "pipeline/ci/job/build/step/s1", "pipeline/ci/job/notify",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d paths, want %d: %v", len(got), len(want), got)
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("missing %s", w)
		}
	}
	got["pipeline/ci"]["gha"].(map[string]any)["run-name"] = "mutated"
	if project().Pipelines[0].Ext["gha"].(map[string]any)["run-name"] != "x" {
		t.Error("Collect must deep copy")
	}
}

func TestAttachWritesSidecar(t *testing.T) {
	in := map[string][]byte{"a.yml": []byte("a: 1\n"), "b.yml": []byte("b: 2\n")}
	m := testMeta()
	out := attach(in, m)
	if _, ok := in[SidecarPath]; ok {
		t.Fatal("input map mutated")
	}
	if len(out) != 3 {
		t.Fatalf("out has %d files, want 3", len(out))
	}
	if m.Files["a.yml"] != sum(in["a.yml"]) || len(m.Files) != 2 {
		t.Errorf("hashes = %v", m.Files)
	}
	if _, ok := m.Files[SidecarPath]; ok {
		t.Error("sidecar must not hash itself")
	}
	if !strings.Contains(string(out[SidecarPath]), "version: 2") {
		t.Errorf("sidecar = %s", out[SidecarPath])
	}
}

func TestLoadSidecar(t *testing.T) {
	m := testMeta()
	out := attach(map[string][]byte{"a.yml": []byte("a: 1\n")}, m)
	got, err := load(asFS(out), []string{"a.yml", "b.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Errorf("got %+v\nwant %+v", got, m)
	}
}

func TestLoadAbsent(t *testing.T) {
	got, err := load(asFS(map[string][]byte{"a.yml": []byte("a: 1\n")}), []string{"a.yml"})
	if got != nil || err != nil {
		t.Fatalf("got %v, %v; want nil, nil", got, err)
	}
}

func TestLoadRejectsBadVersion(t *testing.T) {
	fsys := asFS(map[string][]byte{SidecarPath: []byte("version: 3\n")})
	if _, err := load(fsys, nil); err == nil {
		t.Fatal("want version error")
	}
}

func TestLoadMalformedEmbedded(t *testing.T) {
	for name, body := range map[string]string{
		"bad base64": "a: 1\n# pipemaster:meta:begin\n# !!!\n# pipemaster:meta:end\n",
		"no end":     "a: 1\n# pipemaster:meta:begin\n# AAAA\n",
		"bad yaml":   "a: 1\n# pipemaster:meta:begin\n# " + "Zm9vOiBbMQ==" + "\n# pipemaster:meta:end\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := load(asFS(map[string][]byte{"a.yml": []byte(body)}), []string{"a.yml"}); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestEmbedExtractRoundTrip(t *testing.T) {
	in := map[string][]byte{"z.yml": []byte("z: 1\n"), "a.yml": []byte("a: 1")}
	m := testMeta()
	out := embed(in, m)
	if len(out) != 2 {
		t.Fatalf("Embed must not add files, got %d", len(out))
	}
	if string(out["z.yml"]) != "z: 1\n" {
		t.Error("only the lexically first file may change")
	}
	a := string(out["a.yml"])
	if !strings.HasPrefix(a, "a: 1\n# pipemaster:meta:begin\n") || !strings.HasSuffix(a, "# pipemaster:meta:end\n") {
		t.Fatalf("block shape wrong: %q", a)
	}
	for _, l := range strings.Split(strings.TrimSuffix(a, "\n"), "\n")[1:] {
		if !strings.HasPrefix(l, "# ") || len(l) > 78 {
			t.Errorf("bad line %q (%d)", l, len(l))
		}
	}
	got, err := load(asFS(out), []string{"a.yml", "b.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Errorf("got %+v\nwant %+v", got, m)
	}
	if string(StripEmbedded(out["a.yml"])) != "a: 1\n" {
		t.Errorf("strip = %q", StripEmbedded(out["a.yml"]))
	}
}

func TestStripEmbeddedNoBlock(t *testing.T) {
	b := []byte("x: 1\n")
	if string(StripEmbedded(b)) != "x: 1\n" {
		t.Error("no-op expected")
	}
}

func TestStaleDetectsEdit(t *testing.T) {
	m := testMeta()
	out := attach(map[string][]byte{"a.yml": []byte("a: 1\n"), "b.yml": []byte("b: 2\n"), "c.yml": []byte("c: 3\n")}, m)
	if stale, err := m.Stale(asFS(out)); err != nil || len(stale) != 0 {
		t.Fatalf("clean tree: %v, %v", stale, err)
	}
	out["b.yml"] = []byte("b: edited\n")
	delete(out, "a.yml")
	stale, err := m.Stale(asFS(out))
	if !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(stale, []string{"a.yml", "b.yml"}) {
		t.Errorf("stale = %v", stale)
	}
}

func TestStaleIgnoresEmbeddedBlock(t *testing.T) {
	m := testMeta()
	out := embed(map[string][]byte{"a.yml": []byte("a: 1\n"), "b.yml": []byte("b: 2\n")}, m)
	loaded, err := load(asFS(out), []string{"a.yml", "b.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if stale, err := loaded.Stale(asFS(out)); err != nil || len(stale) != 0 {
		t.Fatalf("stale = %v, err = %v", stale, err)
	}
	out["a.yml"] = append([]byte("# edit\n"), out["a.yml"]...)
	if _, err := loaded.Stale(asFS(out)); !errors.Is(err, ErrStale) {
		t.Fatalf("edit must be stale, err = %v", err)
	}
}

func TestReattachOrphans(t *testing.T) {
	m := testMeta()
	m.Ext["pipeline/ci/job/gone"] = ir.Ext{"gha": 1}
	m.Ext["pipeline/ci/job/also-gone/step/x"] = ir.Ext{"gha": 1}

	p := project()
	p.Pipelines[0].Jobs[0].Steps[0].Ext = nil
	p.Pipelines[0].Ext = ir.Ext{"gha": "old", "other": "keep"}
	orphans := Reattach(p, m)

	want := []string{"pipeline/ci/job/also-gone/step/x", "pipeline/ci/job/gone"}
	if !reflect.DeepEqual(orphans, want) {
		t.Errorf("orphans = %v, want %v", orphans, want)
	}
	if got := p.Pipelines[0].Jobs[0].Steps[0].Ext["gha"].(map[string]any)["id"]; got != "one" {
		t.Errorf("step ext not reattached: %v", got)
	}
	pe := p.Pipelines[0].Ext
	if pe["other"] != "keep" {
		t.Error("keys only on the node must survive")
	}
	if _, ok := pe["gha"].(map[string]any); !ok {
		t.Errorf("stored value must win, got %v", pe["gha"])
	}
}

func TestReattachKeepingEditsNeverOverwrites(t *testing.T) {
	m := testMeta()
	m.Ext["pipeline/ci/job/gone"] = ir.Ext{"gha": 1}

	p := project()
	p.Pipelines[0].Jobs[0].Steps[0].Ext = nil
	p.Pipelines[0].Ext = ir.Ext{"gha": "edited", "other": "keep"}
	orphans, kept := ReattachKeepingEdits(p, m)

	if !reflect.DeepEqual(orphans, []string{"pipeline/ci/job/gone"}) {
		t.Errorf("orphans = %v", orphans)
	}
	if !reflect.DeepEqual(kept, []string{"pipeline/ci"}) {
		t.Errorf("kept = %v, want the pipeline whose Ext was edited", kept)
	}
	if got := p.Pipelines[0].Ext["gha"]; got != "edited" {
		t.Errorf("edited Ext was overwritten: %v", got)
	}
	if got := p.Pipelines[0].Jobs[0].Steps[0].Ext["gha"].(map[string]any)["id"]; got != "one" {
		t.Errorf("an absent key must still be filled: %v", got)
	}
}

func TestLoadScansOnlyParsedFiles(t *testing.T) {
	foreign := embed(map[string][]byte{"docs/example-converted.yml": []byte("a: 1\n")}, testMeta())
	files := map[string][]byte{".github/workflows/ci.yml": []byte("on: push\n")}
	for k, v := range foreign {
		files[k] = v
	}
	fsys := asFS(files)

	got, err := load(fsys, []string{".github/workflows/ci.yml"})
	if err != nil || got != nil {
		t.Fatalf("a block in a file the provider did not parse must be ignored: %v, %v", got, err)
	}
	got, err = load(fsys, []string{"docs/example-converted.yml"})
	if err != nil || got == nil {
		t.Fatalf("a block in a parsed file must load: %v, %v", got, err)
	}
}

func TestLoadIgnoresBlockThatDoesNotListItsFile(t *testing.T) {
	m := testMeta()
	files := embed(map[string][]byte{"a.yml": []byte("a: 1\n")}, m)
	// The same block pasted into another parsed file is not metadata for that file.
	files["b.yml"] = append([]byte("b: 1\n"), files["a.yml"][len("a: 1\n"):]...)
	got, err := load(asFS(files), []string{"b.yml"})
	if err != nil || got != nil {
		t.Fatalf("a block whose files omit the holder must be ignored: %v, %v", got, err)
	}
}
