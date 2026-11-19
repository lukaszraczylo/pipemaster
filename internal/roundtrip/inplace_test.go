package roundtrip_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

const inPlaceCorpus = "13-gha-traefik"

func copyCorpus(t *testing.T, name string) string {
	t.Helper()
	return copyDir(t, filepath.Join(corpusRoot, name))
}

func copyDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		b, err := os.ReadFile(p) //nolint:gosec // corpus path
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o600) //nolint:gosec // out is under t.TempDir
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func mustConvert(t *testing.T, args ...string) string {
	t.Helper()
	code, _, msg := runOut(append([]string{"convert"}, args...)...)
	if code != 0 {
		t.Fatalf("convert %v = %d\n%s", args, code, msg)
	}
	return msg
}

// convertInPlace converts dir into dir, as the repro runs do from inside the directory.
func convertInPlace(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return mustConvert(t, append([]string{dir, "-o", dir}, args...)...)
}

func TestHopRejectsStackOfOtherFiles(t *testing.T) {
	gha := &ir.Project{Origin: ir.Origin{Files: []string{".github/workflows/ci.yml"}}, Pipelines: []*ir.Pipeline{{ID: "main"}}}
	mk := func(target string, files ...string) *meta.Stack {
		sum := sha256.Sum256([]byte("body"))
		m := layer("gha", "a", 1)
		m.Target = target
		m.Files = map[string]string{}
		for _, f := range files {
			m.Files[f] = hex.EncodeToString(sum[:])
		}
		return &meta.Stack{Layers: []*meta.Meta{m}}
	}
	cases := []struct {
		in           *meta.Stack
		name, source string
		accepted     bool
	}{
		{mk("gitlab", ".github/workflows/ci.yml"), "top targets the source and lists its files", "gitlab", true},
		{mk("gitlab", ".github/workflows/ci.yml"), "top targets another provider", "gha", false},
		{mk("gitlab", ".gitlab-ci.yml"), "top lists other files", "gitlab", false},
		{mk("", ".github/workflows/ci.yml"), "layer without a target still loads", "gitlab", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := *gha
			p.Pipelines = []*ir.Pipeline{{ID: "main"}}
			rep := &report.Report{}
			fsys := fstest.MapFS{".github/workflows/ci.yml": {Data: []byte("body")}, ".gitlab-ci.yml": {Data: []byte("body")}}
			res, err := roundtrip.Hop(&p, tc.in, fsys, tc.source, "gha", false, rep)
			if err != nil {
				t.Fatal(err)
			}
			applied := p.Pipelines[0].Ext["a"] != nil
			noted := false
			for _, e := range rep.Entries {
				noted = noted || (e.Feature == caps.ExtPassthrough && e.Level == caps.Lost && strings.Contains(e.Detail, "not applied and is replaced"))
			}
			if applied != tc.accepted || noted == tc.accepted {
				t.Fatalf("applied=%v noted=%v res=%+v entries=%+v", applied, noted, res, rep.Entries)
			}
			if !tc.accepted && (res.Restored || len(res.Out.Layers) != 0) {
				t.Fatalf("rejected stack must act as empty: %+v", res)
			}
		})
	}
}

// Repro 1: a second conversion from the same origin must not leave a layer that a later hop applies to files it
// never described.
func TestInPlaceSecondConversionDoesNotMisapplyLayer(t *testing.T) {
	dir := copyCorpus(t, inPlaceCorpus)
	orig := parseDir(t, provider.GHA, dir)
	convertInPlace(t, dir, "--to", "gitlab")
	msg := convertInPlace(t, dir, "--from", "gha", "--to", "gitea")
	if !strings.Contains(msg, "not applied and is replaced") {
		t.Fatalf("second conversion must report the replaced metadata:\n%s", msg)
	}
	out := t.TempDir()
	msg = mustConvert(t, dir, "--from", "gitlab", "--to", "gha", "-o", out)
	if !strings.Contains(msg, "not applied and is replaced") {
		t.Fatalf("return must report the metadata that describes other files:\n%s", msg)
	}
	back := parseDir(t, provider.GHA, out)
	if same, _ := ir.Equal(orig, back); same {
		t.Fatal("the return cannot be exact: the gitlab file lost its metadata when the gitea conversion replaced the sidecar")
	}
	if got := stackSources(t, provider.GHA, out); len(got) != 1 {
		t.Fatalf("return keeps layers %v, want only its own", got)
	}
}

func TestInPlaceReturnIsExact(t *testing.T) {
	dir := copyCorpus(t, inPlaceCorpus)
	orig := parseDir(t, provider.GHA, dir)
	convertInPlace(t, dir, "--to", "gitlab")
	out := t.TempDir()
	mustConvert(t, dir, "--from", "gitlab", "--to", "gha", "-o", out)
	if same, diff := ir.Equal(orig, parseDir(t, provider.GHA, out)); !same {
		t.Fatalf("in-place return differs:\n%s", diff)
	}
}

// Repro 2: metadata of an earlier conversion must not be applied over an edited file.
func TestInPlaceEditedFileKeepsItsEdit(t *testing.T) {
	dir := copyCorpus(t, inPlaceCorpus)
	convertInPlace(t, dir, "--to", "gitlab")
	convertInPlace(t, dir, "--from", "gha", "--to", "gitlab")
	convertInPlace(t, dir, "--from", "gitlab", "--to", "gha")
	ci := filepath.Join(dir, ".gitlab-ci.yml")
	b, err := os.ReadFile(ci) //nolint:gosec // test path
	if err != nil {
		t.Fatal(err)
	}
	const edit = "pipemaster-edit-marker"
	edited := strings.Replace(string(b), "script:\n", "script:\n    - echo "+edit+"\n", 1)
	if edited == string(b) {
		t.Fatal("fixture has no script to edit")
	}
	if err := os.WriteFile(ci, []byte(edited), 0o600); err != nil { //nolint:gosec // test path
		t.Fatal(err)
	}
	bare := copyDir(t, dir)
	if err := os.RemoveAll(filepath.Join(bare, ".pipemaster")); err != nil {
		t.Fatal(err)
	}
	out, outBare := t.TempDir(), t.TempDir()
	code, _, msg := runOut("convert", dir, "--from", "gitlab", "--to", "gha", "-o", out)
	if code == 4 {
		return // the stale check stops the edited file
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, msg)
	}
	mustConvert(t, bare, "--from", "gitlab", "--to", "gha", "-o", outBare)
	got, want := outputFiles(t, out), outputFiles(t, outBare)
	for name, content := range want {
		if strings.HasPrefix(name, ".github/") && string(got[name]) != string(content) {
			t.Errorf("%s: stored metadata changed the edited file's conversion", name)
		}
	}
	if !strings.Contains(string(got[".github/workflows/build.yml"])+string(got[".github/workflows/build.yaml"]), edit) {
		t.Error("the edit did not survive the conversion")
	}
}
