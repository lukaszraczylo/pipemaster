package roundtrip_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/golden"
	"github.com/lukaszraczylo/pipemaster/provider"
)

const siteDataDir = "../../docs/data"

// siteNames are the display names the website shows for each provider ID.
var siteNames = map[string]string{
	provider.GHA: "GitHub Actions",
	"gitea":      "Gitea Actions",
	"forgejo":    "Forgejo Actions",
	"gitlab":     "GitLab CI",
	"woodpecker": "Woodpecker CI",
	"drone":      "Drone CI",
	"tekton":     "Tekton",
	"argo":       "Argo Workflows",
	"concourse":  "Concourse",
}

// siteTargets are the targets the website converts every sample to; Gitea and Forgejo are GHA dialects.
var siteTargets = []string{provider.GHA, "gitlab", "woodpecker", "drone", "tekton", "argo", "concourse"}

// siteSample is one source pipeline the website shows. Corpus samples name their upstream repository.
type siteSample struct {
	Key     string `json:"key"`
	From    string `json:"from"`
	Title   string `json:"title"`
	Repo    string `json:"repo,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Licence string `json:"licence,omitempty"`
	dir     string
}

// siteSamples lists one source pipeline per provider family, in the order the page shows them.
var siteSamples = []siteSample{
	{Key: "gha", From: provider.GHA, Title: "Go test matrix and a deploy job", dir: "../../testdata/site"},
	{Key: "gitlab", From: "gitlab", Title: "gitlab-org/gitlab-shell", Repo: "https://gitlab.com/gitlab-org/gitlab-shell",
		Commit: "a50f5a6158092f5eab82ef203979dc0e16558bd3", Licence: "MIT", dir: "../../testdata/corpus/21-gitlab-gitlab-shell"},
	{Key: "woodpecker", From: "woodpecker", Title: "jordanopensource/contributions-web", Repo: "https://github.com/jordanopensource/contributions-web",
		Commit: "5c4d44955d335d309777234d10097f31db583f5c", Licence: "Apache-2.0", dir: "../../testdata/corpus/49-woodpecker-contributions-web"},
	{Key: "drone", From: "drone", Title: "axa-group/Parsr", Repo: "https://github.com/axa-group/Parsr",
		Commit: "4462467727409c4f2f4d186edc8ae4844c354ee8", Licence: "Apache-2.0", dir: "../../testdata/corpus/51-drone-parsr"},
	{Key: "tekton", From: "tekton", Title: "openshift/pipelines-tutorial", Repo: "https://github.com/openshift/pipelines-tutorial",
		Commit: "4ebfb3d6f4e195de106f0f59a2506f4edffdfcf7", Licence: "Apache-2.0", dir: "../../testdata/corpus/69-tekton-pipelines-tutorial"},
	{Key: "argo", From: "argo", Title: "linclin/go-gin-rest-api", Repo: "https://github.com/linclin/go-gin-rest-api",
		Commit: "8f326d8d6789fa8d5b486e7586ba8911b9da63a6", Licence: "Apache-2.0", dir: "../../testdata/corpus/73-argo-go-gin-rest-api"},
	{Key: "concourse", From: "concourse", Title: "concourse/examples multi-branch", Repo: "https://github.com/concourse/examples",
		Commit: "a3d23a6a73977baa2d4a8dbd24a3048f4f98b131", Licence: "Apache-2.0", dir: "../../testdata/corpus/87-concourse-examples-multi-branch-tasks"},
}

type siteFile struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

type siteEntry struct {
	Path     string `json:"path"`
	Feature  string `json:"feature"`
	Level    string `json:"level"`
	Strategy string `json:"strategy,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type siteConversion struct {
	ID        string      `json:"id"`
	Roundtrip string      `json:"roundtrip"`
	Files     []siteFile  `json:"files"`
	Report    []siteEntry `json:"report"`
}

type siteCap struct {
	Level string `json:"level"`
	Note  string `json:"note,omitempty"`
}

// siteIndex is docs/data/index.json: providers, features, caps and the sample list.
type siteIndex struct {
	Caps      map[string]map[string]siteCap `json:"caps"`
	Providers []map[string]string           `json:"providers"`
	Features  []string                      `json:"features"`
	Samples   []siteSample                  `json:"samples"`
}

// siteSampleDoc is docs/data/<key>.json: one source pipeline and its conversions.
type siteSampleDoc struct {
	Source      []siteFile       `json:"source"`
	Conversions []siteConversion `json:"conversions"`
}

// siteTree returns every file under dir except round-trip metadata and Markdown, sorted by path.
func siteTree(t *testing.T, dir string) []siteFile {
	t.Helper()
	var out []siteFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".pipemaster/") || strings.HasSuffix(rel, ".md") {
			return nil
		}
		b, err := os.ReadFile(p) //nolint:gosec // test temp dir or fixed fixture
		if err != nil {
			return err
		}
		out = append(out, siteFile{Path: rel, Text: string(b)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// jsonReport decodes the JSON report that convert writes after any warning lines.
func jsonReport(t *testing.T, stderr string) []siteEntry {
	t.Helper()
	i := strings.Index(stderr, "{\n")
	if i < 0 {
		t.Fatalf("no JSON report in:\n%s", stderr)
	}
	var rep struct {
		Entries []struct {
			Path     string `json:"ir_path"`
			Feature  string `json:"feature"`
			Level    string `json:"level"`
			Strategy string `json:"strategy"`
			Detail   string `json:"detail"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(stderr[i:]), &rep); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	out := make([]siteEntry, 0, len(rep.Entries))
	for _, e := range rep.Entries {
		out = append(out, siteEntry(e))
	}
	return out
}

func siteJSON(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// siteConvert converts one sample to every site target except its own source.
func siteConvert(t *testing.T, s siteSample) siteSampleDoc {
	t.Helper()
	doc := siteSampleDoc{Source: siteTree(t, s.dir)}
	for _, id := range siteTargets {
		if id == s.From {
			continue
		}
		out := t.TempDir()
		code, _, stderr := runOut("convert", s.dir, "--from", s.From, "--to", id, "-o", out, "--report", "json")
		if code != 0 {
			t.Fatalf("%s: convert --to %s = %d\n%s", s.Key, id, code, stderr)
		}
		code, rt, msg := runOut("roundtrip", s.dir, "--from", s.From, "--via", id)
		if code != 0 {
			t.Fatalf("%s: roundtrip --via %s = %d\n%s", s.Key, id, code, msg)
		}
		doc.Conversions = append(doc.Conversions, siteConversion{
			ID: id, Files: siteTree(t, out), Report: jsonReport(t, stderr), Roundtrip: strings.TrimSpace(rt),
		})
	}
	return doc
}

// TestSiteData keeps docs/data, which the website renders, equal to what the code produces now.
func TestSiteData(t *testing.T) {
	idx := siteIndex{Caps: map[string]map[string]siteCap{}, Samples: siteSamples}
	for _, f := range caps.AllFeatures() {
		idx.Features = append(idx.Features, string(f))
	}
	for _, id := range provider.IDs() {
		name, ok := siteNames[id]
		if !ok {
			t.Fatalf("provider %s has no website name", id)
		}
		idx.Providers = append(idx.Providers, map[string]string{"id": id, "name": name})
		m := prov(t, id).Caps()
		row := map[string]siteCap{}
		for _, f := range caps.AllFeatures() {
			s := m.Lookup(f)
			note := s.Strategy
			if s.Caveat != "" {
				note = strings.TrimSpace(note + " (" + s.Caveat + ")")
			}
			row[string(f)] = siteCap{Level: s.Level.String(), Note: note}
		}
		idx.Caps[id] = row
	}
	keep := map[string]bool{"index.json": true}
	golden.Assert(t, filepath.Join(siteDataDir, "index.json"), siteJSON(t, idx))
	for _, s := range siteSamples {
		keep[s.Key+".json"] = true
		golden.Assert(t, filepath.Join(siteDataDir, s.Key+".json"), siteJSON(t, siteConvert(t, s)))
	}
	golden.Orphans(t, siteDataDir, keep)
}
