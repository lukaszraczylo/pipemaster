package concourse_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/cli"
	"github.com/lukaszraczylo/pipemaster/provider/concourse"
	_ "github.com/lukaszraczylo/pipemaster/provider/gha"
	_ "github.com/lukaszraczylo/pipemaster/provider/gitlab"
)

const corpusRoot = "../../testdata/corpus"

// shortCorpus is the part of the corpus that the short run covers.
var shortCorpus = map[string]bool{"04": true, "09": true, "12": true, "18": true, "21": true, "26": true}

// TestRoundTripViaConcourseKeepsTheIR converts every gha and gitlab corpus entry to Concourse and back with the
// metadata, and requires the same IR. The exit code is the oracle: the diff text holds non-breaking spaces.
func TestRoundTripViaConcourseKeepsTheIR(t *testing.T) {
	ents, err := os.ReadDir(corpusRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		parts := strings.SplitN(e.Name(), "-", 3)
		if !e.IsDir() || len(parts) != 3 || (parts[1] != "gha" && parts[1] != "gitlab") {
			continue
		}
		if testing.Short() && !shortCorpus[parts[0]] {
			continue
		}
		dir, from := filepath.Join(corpusRoot, e.Name()), parts[1]
		t.Run(e.Name(), func(t *testing.T) {
			t.Parallel()
			var out, msg bytes.Buffer
			if code := cli.Run([]string{"roundtrip", dir, "--via", "concourse", "--from", from}, &out, &msg); code != 0 {
				t.Fatalf("roundtrip via concourse = %d\n%.3000s", code, out.String())
			}
		})
	}
}

// TestUneditedCorpusOutputReportsNoIgnoredMarker reads every emitted corpus file back with no metadata and
// requires that no marker is reported as edited.
func TestUneditedCorpusOutputReportsNoIgnoredMarker(t *testing.T) {
	ents, err := os.ReadDir(corpusRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		parts := strings.SplitN(e.Name(), "-", 3)
		if !e.IsDir() || len(parts) != 3 || (parts[1] != "gha" && parts[1] != "gitlab") {
			continue
		}
		if testing.Short() && !shortCorpus[parts[0]] {
			continue
		}
		dir, from := filepath.Join(corpusRoot, e.Name()), parts[1]
		t.Run(e.Name(), func(t *testing.T) {
			t.Parallel()
			out := t.TempDir()
			var so, se bytes.Buffer
			if code := cli.Run([]string{"convert", dir, "--from", from, "--to", "concourse", "-o", out}, &so, &se); code != 0 {
				t.Fatalf("convert = %d\n%s", code, se.String())
			}
			_, rep, err := concourse.New().Parse(os.DirFS(out))
			if err != nil {
				t.Fatal(err)
			}
			for _, en := range rep.Entries {
				if strings.Contains(en.Detail, "marker ignored") {
					t.Errorf("%s %s: %s", en.IRPath, en.Feature, en.Detail)
				}
			}
		})
	}
}
