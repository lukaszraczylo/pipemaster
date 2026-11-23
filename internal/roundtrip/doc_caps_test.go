package roundtrip_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/provider"
)

var docCapsLine = regexp.MustCompile("^The following table comes from `pipemaster (caps .*)`\\.")

// checkCapsTables compares every caps table of one Markdown file with the command output and returns the table count.
// When wantID is not empty, each table command must name that provider as its --to target.
func checkCapsTables(t *testing.T, path, wantID string) int {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // path is a fixed README or docs/providers file
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	found := 0
	for i, l := range lines {
		m := docCapsLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		found++
		if wantID != "" && !strings.Contains(m[1], "--to "+wantID+" ") {
			t.Errorf("%s line %d: the command `pipemaster %s` does not name --to %s", path, i+1, m[1], wantID)
		}
		var table []string
		for _, tl := range lines[min(i+2, len(lines)):] {
			if !strings.HasPrefix(tl, "|") {
				break
			}
			table = append(table, tl)
		}
		args := strings.Fields(strings.TrimSuffix(strings.Split(m[1], ". ")[0], "."))
		code, out, msg := runOut(args...)
		if code != 0 {
			t.Fatalf("%v = %d\n%s", args, code, msg)
		}
		if got, want := strings.Join(table, "\n")+"\n", out; got != want {
			t.Errorf("%s line %d: the table differs from `pipemaster %s`", path, i+1, m[1])
		}
	}
	return found
}

// TestDocCapsTablesMatchTheCommand keeps each caps table of the README and of the provider docs equal to the command output.
func TestDocCapsTablesMatchTheCommand(t *testing.T) {
	if checkCapsTables(t, "../../README.md", "") == 0 {
		t.Error("no caps table found in the README")
	}
	for _, id := range provider.IDs() {
		path := filepath.Join("../../docs/providers", id+".md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("provider %s has no doc: %v", id, err)
			continue
		}
		if checkCapsTables(t, path, id) == 0 {
			t.Errorf("%s has no caps table", path)
		}
	}
	docs, err := filepath.Glob("../../docs/providers/*.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		id := strings.TrimSuffix(filepath.Base(d), ".md")
		if _, err := provider.Get(id); err != nil {
			t.Errorf("%s matches no provider: %v", d, err)
		}
	}
}
