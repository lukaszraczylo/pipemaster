package concourse_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
)

func TestLiteralRepositoryUriNamesTheAssumption(t *testing.T) {
	const want = "https://example.com/r.git is the repository that holds the converted pipeline"
	y := oneJob(inlineHead + "      run: {path: ls}\n")
	_, rep := parseYAML(t, y)
	if got := entriesAt(rep, caps.Emulated, want); len(got) != 1 {
		t.Errorf("a literal uri needs one entry that names the assumption: %+v", rep.Entries)
	}
	_, rep = parseYAML(t, strings.Replace(y, "https://example.com/r.git", "((git-uri))", 1))
	if got := entriesAt(rep, caps.Emulated, "is the repository that holds the converted pipeline"); len(got) != 0 {
		t.Errorf("a ((var)) uri names the repository, so it holds no assumption: %+v", got)
	}
}
