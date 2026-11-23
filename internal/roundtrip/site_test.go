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

const (
	siteSample = "../../testdata/site"
	siteData   = "../../docs/data.json"
)

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

type siteDoc struct {
	Providers   []map[string]string           `json:"providers"`
	Features    []string                      `json:"features"`
	Caps        map[string]map[string]siteCap `json:"caps"`
	Source      []siteFile                    `json:"source"`
	Conversions []siteConversion              `json:"conversions"`
}

// siteTree returns every file under dir except round-trip metadata, sorted by path.
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
		if strings.HasPrefix(rel, ".pipemaster/") {
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

// TestSiteData keeps docs/data.json, which the website renders, equal to what the code produces now.
func TestSiteData(t *testing.T) {
	doc := siteDoc{Caps: map[string]map[string]siteCap{}, Source: siteTree(t, siteSample)}
	for _, f := range caps.AllFeatures() {
		doc.Features = append(doc.Features, string(f))
	}
	for _, id := range provider.IDs() {
		name, ok := siteNames[id]
		if !ok {
			t.Fatalf("provider %s has no website name", id)
		}
		doc.Providers = append(doc.Providers, map[string]string{"id": id, "name": name})
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
		doc.Caps[id] = row
		if id == provider.GHA {
			continue
		}
		out := t.TempDir()
		code, _, stderr := runOut("convert", siteSample, "--to", id, "-o", out, "--report", "json")
		if code != 0 {
			t.Fatalf("convert --to %s = %d\n%s", id, code, stderr)
		}
		code, rt, msg := runOut("roundtrip", siteSample, "--via", id)
		if code != 0 {
			t.Fatalf("roundtrip --via %s = %d\n%s", id, code, msg)
		}
		doc.Conversions = append(doc.Conversions, siteConversion{
			ID: id, Files: siteTree(t, out), Report: jsonReport(t, stderr), Roundtrip: strings.TrimSpace(rt),
		})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(doc); err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, siteData, buf.Bytes())
}
