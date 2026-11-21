package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var o, e bytes.Buffer
	code = Run(args, &o, &e)
	return code, o.String(), e.String()
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // test helper reads t.TempDir paths
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDetectNone(t *testing.T) {
	dir := t.TempDir()
	code, _, errs := run(t, "detect", dir)
	if code != 1 || !strings.Contains(errs, "no supported pipeline config found in "+dir) {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}

func TestDetectOne(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "fake.cfg", "job a\n")
	code, out, _ := run(t, "detect", dir)
	if code != 0 || out != "fake\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestDetectAmbiguous(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "fake.cfg", "job a\n")
	write(t, dir, "fake2.cfg", "job a\n")
	code, out, _ := run(t, "detect", dir)
	if code != 0 || out != "fake\nfake2\n" {
		t.Fatalf("detect lists all: code=%d out=%q", code, out)
	}
	code, _, errs := run(t, "inspect", dir)
	if code != 2 || !strings.Contains(errs, "fake, fake2") || !strings.Contains(errs, "--from") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}

func TestUnknownProviderExit2(t *testing.T) {
	dir := t.TempDir()
	code, _, errs := run(t, "inspect", dir, "--from", "nope")
	if code != 2 || !strings.Contains(errs, "fake") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	cases := [][]string{
		{"convert"},
		{"convert", dir},
		{"convert", dir, dir, "--to", "fake2"},
		{"convert", dir, "--to", "fake2", "--bogus"},
		{"convert", dir, "--to", "fake2", "--report", "xml"},
		{"inspect"},
		{"roundtrip", dir},
		{"caps", "--from", "fake"},
		{"validate", dir},
	}
	for _, args := range cases {
		code, _, errs := run(t, args...)
		if code != 2 || !strings.Contains(errs, "usage: pipemaster "+args[0]) {
			t.Errorf("%v: code=%d stderr=%q", args, code, errs)
		}
	}
}

func TestInspectPrintsIR(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "fake.cfg", "job build\n")
	code, out, errs := run(t, "inspect", dir)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	for _, want := range []string{"pipelines:", "id: main", "id: build"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

func TestConvertWritesFilesAndMeta(t *testing.T) {
	src, out := t.TempDir(), filepath.Join(t.TempDir(), "o")
	write(t, src, "fake.cfg", "job build\next keepme\n")
	// flags after the positional dir, as in the spec
	code, _, errs := run(t, "convert", src, "--to", "fake2", "-o", out)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if got := read(t, out, "fake2.cfg"); got != "job build\n" {
		t.Fatalf("fake2.cfg = %q", got)
	}
	m := read(t, out, ".pipemaster/meta.yaml")
	for _, want := range []string{"source: fake", "dialect: fake-dialect", "keepme"} {
		if !strings.Contains(m, want) {
			t.Errorf("meta lacks %q:\n%s", want, m)
		}
	}
	if !strings.Contains(errs, "Loss report: ") {
		t.Errorf("report not on stderr: %q", errs)
	}
}

func TestConvertEmbedMeta(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, src, "fake.cfg", "job build\n")
	code, _, errs := run(t, "convert", "--embed-meta", "--to", "fake2", "-o", out, src)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if _, err := os.Stat(filepath.Join(out, ".pipemaster/meta.yaml")); err == nil {
		t.Fatal("sidecar written with --embed-meta")
	}
	if !strings.Contains(read(t, out, "fake2.cfg"), "# pipemaster:meta:begin") {
		t.Fatal("no embedded block")
	}
}

func TestConvertStrictExit3(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, src, "fake.cfg", "job emu\n")
	code, _, errs := run(t, "convert", src, "--to", "fake2", "-o", out, "--strict")
	if code != 3 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if !strings.Contains(errs, "[emulated]") {
		t.Errorf("report lacks verdict: %q", errs)
	}
	if got := read(t, out, "fake2.cfg"); got != "job emu\n" {
		t.Fatalf("files must still be written under --strict, got %q", got)
	}
	code, _, _ = run(t, "convert", src, "--to", "fake2", "-o", out)
	if code != 0 {
		t.Fatalf("without --strict code=%d", code)
	}
}

func TestConvertReportFormats(t *testing.T) {
	src := t.TempDir()
	write(t, src, "fake.cfg", "job emu\n")
	for format, want := range map[string]string{"json": `"entries"`, "md": "|", "text": "Loss report"} {
		code, _, errs := run(t, "convert", src, "--to", "fake2", "-o", t.TempDir(), "--report", format)
		if code != 0 || !strings.Contains(errs, want) {
			t.Errorf("%s: code=%d stderr=%q", format, code, errs)
		}
	}
}

func TestConvertStaleExit4(t *testing.T) {
	src, mid, back := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, src, "fake.cfg", "job build\next keepme\n")
	if code, _, e := run(t, "convert", src, "--to", "fake2", "-o", mid); code != 0 {
		t.Fatalf("hop1: %d %s", code, e)
	}
	write(t, mid, "fake2.cfg", "job build\njob extra\n")
	code, _, errs := run(t, "convert", mid, "--to", "fake", "-o", back)
	if code != 4 || !strings.Contains(errs, "fake2.cfg") || !strings.Contains(errs, "--force") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if _, err := os.Stat(filepath.Join(back, "fake.cfg")); err == nil {
		t.Fatal("stale convert must write nothing")
	}
}

func TestConvertForce(t *testing.T) {
	src, mid, back := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, src, "fake.cfg", "job build\next keepme\n")
	if code, _, e := run(t, "convert", src, "--to", "fake2", "-o", mid); code != 0 {
		t.Fatalf("hop1: %d %s", code, e)
	}
	write(t, mid, "fake2.cfg", "job build\njob extra\n")
	code, _, errs := run(t, "convert", mid, "--to", "fake", "-o", back, "--force")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if got := read(t, back, "fake.cfg"); got != "job build\njob extra\next keepme\n" {
		t.Fatalf("fake.cfg = %q", got)
	}
}

func TestConvertReattachWhenFresh(t *testing.T) {
	src, mid, back := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, src, "fake.cfg", "job build\next keepme\n")
	run(t, "convert", src, "--to", "fake2", "-o", mid)
	code, _, errs := run(t, "convert", mid, "--to", "fake", "-o", back)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if got := read(t, back, "fake.cfg"); got != "job build\next keepme\n" {
		t.Fatalf("fake.cfg = %q", got)
	}
}

func TestConvertOrphanReported(t *testing.T) {
	src, mid, back := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, src, "fake.cfg", "job build\next keepme\n")
	run(t, "convert", src, "--to", "fake2", "-o", mid)
	// Rewrite meta to add a path no node matches; files hash stays valid.
	m := read(t, mid, ".pipemaster/meta.yaml")
	m = strings.Replace(m, "ext:\n", "ext:\n        gone/path:\n            fake: x\n", 1)
	write(t, mid, ".pipemaster/meta.yaml", m)
	code, _, errs := run(t, "convert", mid, "--to", "fake", "-o", back)
	if code != 0 || !strings.Contains(errs, "[lost]") || !strings.Contains(errs, "gone/path") || !strings.Contains(errs, "ext.passthrough") {
		t.Fatalf("code=%d stderr=%q\nmeta:\n%s", code, errs, m)
	}
}

func TestConvertStaleExit4ForOtherTarget(t *testing.T) {
	src, mid, back := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, src, "fake.cfg", "job build\n")
	run(t, "convert", src, "--to", "fake2", "-o", mid)
	write(t, mid, "fake2.cfg", "job build\njob extra\n")
	// The edited file is stale whatever the target, so a hop that is not a return refuses it too.
	code, _, errs := run(t, "convert", mid, "--to", "fake3", "-o", back)
	if code != 4 || !strings.Contains(errs, "fake2.cfg") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}

func TestRoundtripPrintsEmptyDiff(t *testing.T) {
	src := t.TempDir()
	write(t, src, "fake.cfg", "job build\next keepme\n")
	code, out, errs := run(t, "roundtrip", src, "--via", "fake2")
	if code != 0 || out != "no differences\n" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errs)
	}
	if !strings.Contains(errs, "Loss report") {
		t.Errorf("report missing: %q", errs)
	}
}

func TestRoundtripSelfIsLossless(t *testing.T) {
	src := t.TempDir()
	write(t, src, "fake.cfg", "job a\n")
	if code, out, _ := run(t, "roundtrip", src, "--via", "fake", "--from", "fake"); code != 0 || out != "no differences\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestCapsDiff(t *testing.T) {
	code, out, errs := run(t, "caps", "--from", "fake", "--to", "fake2")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 10 || !strings.HasPrefix(lines[0], "FEATURE") {
		t.Fatalf("unexpected table:\n%s", out)
	}
	var cache string
	for _, l := range lines {
		if strings.HasPrefix(l, "job.cache") {
			cache = l
		}
	}
	for _, want := range []string{"emulated", "cache step"} {
		if !strings.Contains(cache, want) {
			t.Errorf("job.cache row lacks %q: %q", want, cache)
		}
	}
	_, out2, _ := run(t, "caps", "--from", "fake", "--to", "fake2")
	if out != out2 {
		t.Error("caps output not deterministic")
	}
	code, md, _ := run(t, "caps", "--from", "fake", "--to", "fake2", "--format", "md")
	if code != 0 || !strings.HasPrefix(md, "| Feature |") || !strings.Contains(md, `slow \| cold`) {
		t.Fatalf("md: code=%d\n%s", code, md)
	}
	if code, _, _ := run(t, "caps", "--from", "fake", "--to", "fake2", "--format", "xml"); code != 2 {
		t.Fatalf("bad format code=%d", code)
	}
}

func TestValidateCommand(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".github/workflows/ok.yml", "name: ci\non: push\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n")
	write(t, dir, ".github/workflows/bad.yaml", "on: push\njobs:\n  b:\n    steps:\n      - run: echo hi\n")
	code, out, errs := run(t, "validate", dir, "--as", "gha")
	if code != 1 || !strings.Contains(out, "ok .github/workflows/ok.yml") || !strings.Contains(errs, "bad.yaml") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errs)
	}
	empty := t.TempDir()
	code, _, errs = run(t, "validate", empty, "--as", "gitlab")
	if code != 1 || !strings.Contains(errs, "no files") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if code, _, _ = run(t, "validate", empty, "--as", "fake"); code != 2 {
		t.Fatalf("no-schema provider code=%d", code)
	}
}

func TestUsageListsCommands(t *testing.T) {
	_, _, errs := run(t)
	for _, c := range []string{"version", "detect", "inspect", "convert", "roundtrip", "caps", "validate"} {
		if !strings.Contains(errs, "  "+c) {
			t.Errorf("usage lacks %q", c)
		}
	}
}

func TestRoundtripInequalityPrintsDiff(t *testing.T) {
	src := t.TempDir()
	write(t, src, "fake.cfg", "job keep\njob lossy\n")
	code, out, _ := run(t, "roundtrip", src, "--via", "fake3", "--from", "fake")
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if out == "no differences\n" || !strings.Contains(out, "lossy") {
		t.Fatalf("diff not printed to stdout: %q", out)
	}
}

func TestConvertValidationFailureWritesNothing(t *testing.T) {
	old := validateEmitted
	t.Cleanup(func() { validateEmitted = old })
	validateEmitted = func(id, name string, _ []byte) error {
		return fmt.Errorf("%s: invalid for %s schema", name, id)
	}
	src, out := t.TempDir(), t.TempDir()
	write(t, src, "fake.cfg", "job build\n")
	code, _, errs := run(t, "convert", src, "--to", "fake2", "-o", out)
	if code != 1 || !strings.Contains(errs, "fake2.cfg: invalid for fake2 schema") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) != 0 {
		t.Fatalf("-o must stay empty, got %v (err %v)", entries, err)
	}
}

func TestConvertNoSchemaWarnsOncePerFile(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, src, "fake.cfg", "job build\n")
	code, _, errs := run(t, "convert", src, "--to", "fake2", "-o", out)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	want := "warning: no schema for fake2; fake2.cfg not validated\n"
	if n := strings.Count(errs, "warning:"); n != 1 || !strings.Contains(errs, want) {
		t.Fatalf("want exactly one warning %q, stderr=%q", want, errs)
	}
	if strings.Contains(errs, "meta.yaml") {
		t.Fatalf("sidecar must not be validated: %q", errs)
	}
}

// The fixtures were written by the Phase 1 binary (commit 38d7f59) from testdata/gha/features/config.name.
func TestConvertBackFromPhase1Metadata(t *testing.T) {
	want, err := os.ReadFile("../../testdata/gha/features/config.name/.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"v1-sidecar", "v1-embedded"} {
		t.Run(name, func(t *testing.T) {
			src, out := t.TempDir(), t.TempDir()
			if err := os.CopyFS(src, os.DirFS("../../testdata/meta/"+name)); err != nil {
				t.Fatal(err)
			}
			code, _, errs := run(t, "convert", "--to", "gha", "-o", out, src)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, errs)
			}
			var got, wantDoc any
			if err := yaml.Unmarshal([]byte(read(t, out, ".github/workflows/ci.yml")), &got); err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal(want, &wantDoc); err != nil {
				t.Fatal(err)
			}
			// The emitter writes the bare trigger `on: push` as a map, so the return is exact as a document, not as bytes.
			if doc, ok := wantDoc.(map[string]any); ok && doc["on"] == "push" {
				doc["on"] = map[string]any{"push": nil}
			}
			if !reflect.DeepEqual(got, wantDoc) {
				t.Errorf("not an exact return:\n got %v\nwant %v", got, wantDoc)
			}
		})
	}
}
