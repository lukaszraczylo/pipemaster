package actions

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

func repo(owner, name, path string) ir.ActionRef {
	return ir.ActionRef{Kind: ir.ActionRepo, Owner: owner, Repo: name, Path: path, Ref: "v1"}
}

func TestEmbeddedFileParses(t *testing.T) {
	if err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLookupCheckout(t *testing.T) {
	got, ok := Lookup(repo("actions", "checkout", ""), provider.GitLab,
		map[string]string{"fetch-depth": "0", "submodules": "recursive", "token": "x"})
	if !ok {
		t.Fatal("checkout not found")
	}
	if got.Kind != "implicit" || got.Support.Level != caps.Native {
		t.Fatalf("kind/support = %q/%v", got.Kind, got.Support.Level)
	}
	want := map[string]any{"GIT_DEPTH": "0", "GIT_SUBMODULE_STRATEGY": "recursive"}
	if !reflect.DeepEqual(got.With, want) {
		t.Fatalf("With = %v, want %v", got.With, want)
	}
	if !reflect.DeepEqual(got.Unmapped, []string{"token"}) {
		t.Fatalf("Unmapped = %v", got.Unmapped)
	}
}

func TestLookupCaseInsensitive(t *testing.T) {
	if _, ok := Lookup(repo("Actions", "Checkout", ""), provider.GitLab, nil); !ok {
		t.Fatal("case-insensitive match failed")
	}
}

func TestLookupUnknown(t *testing.T) {
	cases := map[string]ir.ActionRef{
		"unknown repo":  repo("acme", "thing", ""),
		"subpath":       repo("actions", "cache", "save"),
		"docker":        {Kind: ir.ActionDocker, Image: "alpine:3"},
		"local":         {Kind: ir.ActionLocal, Path: "./x"},
		"docker-shaped": {Kind: ir.ActionDocker, Owner: "actions", Repo: "checkout"},
	}
	for name, ref := range cases {
		got, ok := Lookup(ref, provider.GitLab, nil)
		if ok || !reflect.DeepEqual(got, Target{}) {
			t.Errorf("%s: got %+v, %v", name, got, ok)
		}
	}
	if _, ok := Lookup(repo("actions", "checkout", ""), provider.Argo, nil); ok {
		t.Error("unregistered target matched")
	}
}

func TestSetupGoImage(t *testing.T) {
	got, ok := Lookup(repo("actions", "setup-go", ""), provider.GitLab, map[string]string{"go-version": "1.27"})
	if !ok {
		t.Fatal("setup-go not found")
	}
	if got.Kind != "image" || got.Image != "golang:1.27" {
		t.Fatalf("kind/image = %q/%q", got.Kind, got.Image)
	}
	if got.Support.Level != caps.Emulated || got.Support.Caveat != "job image replaced" {
		t.Fatalf("support = %+v", got.Support)
	}
	if len(got.Unmapped) != 0 {
		t.Fatalf("Unmapped = %v", got.Unmapped)
	}
}

func TestSetupImages(t *testing.T) {
	cases := []struct{ name, key, want string }{
		{"setup-node", "node-version", "node:20"},
		{"setup-python", "python-version", "python:20"},
		{"setup-java", "java-version", "eclipse-temurin:20"},
	}
	for _, c := range cases {
		got, ok := Lookup(repo("actions", c.name, ""), provider.GitLab, map[string]string{c.key: "20"})
		if !ok || got.Image != c.want {
			t.Errorf("%s: %q, %v", c.name, got.Image, ok)
		}
	}
}

func TestSetupMissingVersionIsLost(t *testing.T) {
	got, ok := Lookup(repo("actions", "setup-go", ""), provider.GitLab, nil)
	if !ok {
		t.Fatal("not found")
	}
	if got.Image != "" || got.Support.Level != caps.Lost || !strings.Contains(got.Support.Caveat, "go-version") {
		t.Fatalf("got %+v", got)
	}
}

func TestSetupVersionFileIsLost(t *testing.T) {
	got, _ := Lookup(repo("actions", "setup-node", ""), provider.GitLab, map[string]string{"node-version-file": ".nvmrc"})
	if got.Image != "" || got.Support.Level != caps.Lost || got.Support.Caveat != "version read from file at runtime" {
		t.Fatalf("got %+v", got)
	}
	if len(got.Unmapped) != 0 {
		t.Fatalf("Unmapped = %v", got.Unmapped)
	}
	// A literal version wins over the file input.
	got, _ = Lookup(repo("actions", "setup-node", ""), provider.GitLab,
		map[string]string{"node-version-file": ".nvmrc", "node-version": "22"})
	if got.Image != "node:22" || got.Support.Level != caps.Emulated {
		t.Fatalf("literal should win: %+v", got)
	}
}

func TestCacheAndArtifacts(t *testing.T) {
	c, _ := Lookup(repo("actions", "cache", ""), provider.GitLab, map[string]string{"key": "k", "path": "~/.cache", "restore-keys": "r"})
	if c.Kind != "cache" || c.With["key"] != "k" || !reflect.DeepEqual(c.With["paths"], []string{"~/.cache"}) || !reflect.DeepEqual(c.Unmapped, []string{"restore-keys"}) {
		t.Fatalf("cache: %+v", c)
	}
	u, _ := Lookup(repo("actions", "upload-artifact", ""), provider.GitLab, map[string]string{"name": "n", "path": "dist", "retention-days": "3"})
	if u.Kind != "artifact_upload" || u.With["expire_in"] != "3 days" || u.With["name"] != "n" || !reflect.DeepEqual(u.With["path"], []string{"dist"}) {
		t.Fatalf("upload: %+v", u)
	}
	d, _ := Lookup(repo("actions", "download-artifact", ""), provider.GitLab, nil)
	if d.Kind != "artifact_download" {
		t.Fatalf("download: %+v", d)
	}
}

func TestGHAPassthrough(t *testing.T) {
	in := map[string]string{"a": "1", "b": "2"}
	got, ok := Lookup(repo("actions", "setup-go", ""), provider.GHA, in)
	if !ok || got.Kind != "passthrough" || got.Support.Level != caps.Native {
		t.Fatalf("got %+v, %v", got, ok)
	}
	if !reflect.DeepEqual(got.With, map[string]any{"a": "1", "b": "2"}) || len(got.Unmapped) != 0 {
		t.Fatalf("with/unmapped = %v / %v", got.With, got.Unmapped)
	}
}

func TestUnmappedSorted(t *testing.T) {
	got, _ := Lookup(repo("actions", "checkout", ""), provider.GitLab, map[string]string{"z": "1", "a": "2", "m": "3"})
	if !reflect.DeepEqual(got.Unmapped, []string{"a", "m", "z"}) {
		t.Fatalf("Unmapped = %v", got.Unmapped)
	}
}

func TestEverySetupAndCoreEntryHasGHA(t *testing.T) {
	for _, n := range []string{"checkout", "cache", "upload-artifact", "download-artifact", "setup-go", "setup-node", "setup-python", "setup-java"} {
		if _, ok := Lookup(repo("actions", n, ""), provider.GHA, nil); !ok {
			t.Errorf("%s missing gha target", n)
		}
	}
}

func TestParsePathAndDefaults(t *testing.T) {
	r, err := parse([]byte(`
actions:
  - match: acme/mono
    path: sub
    targets:
      gitlab:
        kind: image
        image: "tool:{{ver}}"
        defaults: {ver: "9"}
        support: {level: emulated, strategy: s}
reverse: {checkout: "a/b@v1", cache: "a/c@v1", upload: "a/d@v1", download: "a/e@v1"}
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.lookup(repo("acme", "mono", ""), provider.GitLab, nil); ok {
		t.Error("entry with path must not match pathless ref")
	}
	if _, ok := r.lookup(repo("acme", "mono", "other"), provider.GitLab, nil); ok {
		t.Error("wrong path matched")
	}
	got, ok := r.lookup(repo("acme", "mono", "sub"), provider.GitLab, nil)
	if !ok || got.Image != "tool:9" || got.Support.Level != caps.Emulated {
		t.Fatalf("got %+v, %v", got, ok)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	bad := map[string]string{
		"bad yaml":      "actions: [",
		"unknown field": "actions: []\nreverse: {checkout: a/b@v1, cache: a/b@v1, upload: a/b@v1, download: a/b@v1}\nbogus: 1",
		"bad level":     "actions:\n  - match: a/b\n    targets: {gitlab: {kind: implicit, support: {level: bogus}}}\nreverse: {checkout: a/b@v1, cache: a/b@v1, upload: a/b@v1, download: a/b@v1}",
		"bad match":     "actions:\n  - match: nope\n    targets: {gitlab: {kind: implicit}}\nreverse: {checkout: a/b@v1, cache: a/b@v1, upload: a/b@v1, download: a/b@v1}",
		"missing rev":   "actions: []\nreverse: {checkout: a/b@v1}",
		"unpinned rev":  "actions: []\nreverse: {checkout: a/b, cache: a/b@v1, upload: a/b@v1, download: a/b@v1}",
	}
	for name, src := range bad {
		if _, err := parse([]byte(src)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestReversePins(t *testing.T) {
	want := map[string]string{
		"checkout": "checkout", "cache": "cache",
		"upload": "upload-artifact", "download": "download-artifact",
	}
	for kind, name := range want {
		ref, ok := Reverse(kind)
		if !ok {
			t.Fatalf("%s missing", kind)
		}
		if ref.Kind != ir.ActionRepo || ref.Owner != "actions" || ref.Repo != name || !strings.HasPrefix(ref.Ref, "v") || strings.Contains(ref.Ref, ".") {
			t.Errorf("%s: %+v", kind, ref)
		}
	}
	if _, ok := Reverse("nope"); ok {
		t.Error("unknown reverse kind matched")
	}
}

func lookupWith(t *testing.T, name string, with map[string]string) Target {
	t.Helper()
	got, ok := Lookup(repo("actions", name, ""), provider.GitLab, with)
	if !ok {
		t.Fatalf("%s not found", name)
	}
	return got
}

func TestImageValueValidation(t *testing.T) {
	cases := []struct{ name, val, caveat string }{
		{"matrix expr", "${{ matrix.node }}", "version is an expression"},
		{"lts glob", "lts/*", "not a plain tag"},
		{"caret", "^1.22", "not a plain tag"},
		{"space", "20 x", "not a plain tag"},
		{"newline", "20\n21", "not a plain tag"},
		{"leading dot", ".5", "not a plain tag"},
		{"too long", strings.Repeat("a", 129), "not a plain tag"},
	}
	for _, c := range cases {
		got := lookupWith(t, "setup-node", map[string]string{"node-version": c.val})
		if got.Image != "" || got.Support.Level != caps.Lost ||
			!strings.Contains(got.Support.Caveat, "node-version") || !strings.Contains(got.Support.Caveat, c.caveat) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	for _, ok := range []string{"20", "1.27.3", "20-alpine", "v_1", strings.Repeat("a", 128)} {
		if got := lookupWith(t, "setup-node", map[string]string{"node-version": ok}); got.Support.Level != caps.Emulated {
			t.Errorf("%q rejected: %+v", ok, got)
		}
	}
}

func TestRetentionDays(t *testing.T) {
	for in, want := range map[string]string{"3": "3 days", "1": "1 day", "90": "90 days"} {
		got := lookupWith(t, "upload-artifact", map[string]string{"retention-days": in})
		if got.With["expire_in"] != want || got.Support.Level != caps.Native {
			t.Errorf("%s: %+v", in, got)
		}
	}
	for _, bad := range []string{"0", "-1", "x", "1.5", "${{ inputs.d }}"} {
		got := lookupWith(t, "upload-artifact", map[string]string{"retention-days": bad})
		if got.Support.Level != caps.Lost || !strings.Contains(got.Support.Caveat, "retention-days") {
			t.Errorf("%q: %+v", bad, got)
		}
		if _, set := got.With["expire_in"]; set {
			t.Errorf("%q passed through: %+v", bad, got.With)
		}
	}
}

func TestSubmodules(t *testing.T) {
	for in, want := range map[string]string{"true": "normal", "false": "none", "recursive": "recursive", "TRUE": "normal"} {
		got := lookupWith(t, "checkout", map[string]string{"submodules": in})
		if got.With["GIT_SUBMODULE_STRATEGY"] != want || got.Support.Level != caps.Native {
			t.Errorf("%s: %+v", in, got)
		}
	}
	got := lookupWith(t, "checkout", map[string]string{"submodules": "yes"})
	if got.Support.Level != caps.Lost || !strings.Contains(got.Support.Caveat, "submodules") {
		t.Errorf("yes: %+v", got)
	}
}

func TestFetchDepth(t *testing.T) {
	// GHA 0 and GitLab GIT_DEPTH 0 both mean full history.
	for _, in := range []string{"0", "1", "50"} {
		got := lookupWith(t, "checkout", map[string]string{"fetch-depth": in})
		if got.With["GIT_DEPTH"] != in {
			t.Errorf("%s: %+v", in, got)
		}
	}
	for _, bad := range []string{"-1", "abc", ""} {
		got := lookupWith(t, "checkout", map[string]string{"fetch-depth": bad})
		if got.Support.Level != caps.Lost || !strings.Contains(got.Support.Caveat, "fetch-depth") {
			t.Errorf("%q: %+v", bad, got)
		}
	}
}

func TestPathLines(t *testing.T) {
	c := lookupWith(t, "cache", map[string]string{"path": "~/.cache\n  node_modules \n\n./dist/\n"})
	if want := []string{"~/.cache", "node_modules", "./dist/"}; !reflect.DeepEqual(c.With["paths"], want) {
		t.Errorf("cache paths = %v", c.With["paths"])
	}
	u := lookupWith(t, "upload-artifact", map[string]string{"path": "a\nb"})
	if want := []string{"a", "b"}; !reflect.DeepEqual(u.With["path"], want) {
		t.Errorf("upload path = %v", u.With["path"])
	}
	empty := lookupWith(t, "cache", map[string]string{"path": " \n "})
	if empty.Support.Level != caps.Lost {
		t.Errorf("empty path list: %+v", empty)
	}
}

func TestUnknownTransformFailsParse(t *testing.T) {
	_, err := parse([]byte(`
actions:
  - match: a/b
    targets:
      gitlab:
        kind: cache
        with: {x: {to: y, transform: bogus}}
        support: {level: native}
reverse: {checkout: a/b@v1, cache: a/b@v1, upload: a/b@v1, download: a/b@v1}
`))
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("err = %v", err)
	}
}
