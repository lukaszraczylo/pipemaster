package argo_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/cli"
	_ "github.com/lukaszraczylo/pipemaster/provider/gha"
	_ "github.com/lukaszraczylo/pipemaster/provider/gitlab"
)

// corpusRoot holds the real workflows that every converter must carry through a file and back.
const corpusRoot = "../../testdata/corpus"

// TestCorpusRoundTripThroughArgo checks that a GitHub or GitLab project comes back unchanged after it goes
// through Argo files and metadata. The exit code is the verdict: the diff text holds non-breaking spaces.
func TestCorpusRoundTripThroughArgo(t *testing.T) {
	ents, err := os.ReadDir(corpusRoot)
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, e := range ents {
		if !e.IsDir() || (!strings.Contains(e.Name(), "-gha-") && !strings.Contains(e.Name(), "-gitlab-")) {
			continue
		}
		ran++
		t.Run(e.Name(), func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := cli.Run([]string{"roundtrip", filepath.Join(corpusRoot, e.Name()), "--via", "argo"}, &out, &errb); code != 0 {
				t.Fatalf("roundtrip exit code = %d, want 0\n%s", code, errb.String()+out.String())
			}
		})
	}
	if ran == 0 {
		t.Fatal("no gha or gitlab corpus entry")
	}
}
