package roundtrip_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var readmeCapsLine = regexp.MustCompile("^The following table comes from `pipemaster (caps .*)`\\.")

// TestReadmeCapsTablesMatchTheCommand keeps each caps table of the README equal to the command output.
func TestReadmeCapsTablesMatchTheCommand(t *testing.T) {
	raw, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	found := 0
	for i, l := range lines {
		m := readmeCapsLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		found++
		var table []string
		for _, tl := range lines[i+2:] {
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
			t.Errorf("README line %d: the table differs from `pipemaster %s`", i+1, m[1])
		}
	}
	if found == 0 {
		t.Fatal("no caps table found in the README")
	}
}
