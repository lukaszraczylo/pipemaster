//go:build behave

package behave

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lukaszraczylo/pipemaster/report"
)

const convertTimeout = 2 * time.Minute

// Converted is the output of one pipemaster convert run.
type Converted struct {
	Report *report.Report
	Dir    string
	Stderr string
	Files  []string
	Exit   int
}

// Convert converts the fixture to target with the repository's own CLI.
func (e *Env) Convert(ctx context.Context, fx Fixture, target string) (*Converted, error) {
	out := filepath.Join(e.Work, "conv", fx.Source, fx.Name, target)
	text, code, err := e.run(ctx, runOpts{Dir: e.Root, Timeout: convertTimeout}, e.PM,
		"convert", fx.Dir, "--from", fx.Source, "--to", target, "-o", out, "--report", "json")
	if err != nil {
		return nil, fmt.Errorf("convert %s/%s to %s: %w", fx.Source, fx.Name, target, err)
	}
	c := &Converted{Dir: out, Exit: code, Stderr: text}
	if code != 0 {
		return c, nil
	}
	if err := copyRepoFiles(fx.Dir, out); err != nil {
		return nil, fmt.Errorf("convert %s/%s to %s: %w", fx.Source, fx.Name, target, err)
	}
	i := strings.Index(text, "\n{")
	if strings.HasPrefix(text, "{") {
		i = -1
	}
	rep := &report.Report{}
	dec := json.NewDecoder(bytes.NewReader([]byte(text[i+1:])))
	if err := dec.Decode(rep); err != nil {
		return nil, fmt.Errorf("convert %s/%s to %s: parse report: %w", fx.Source, fx.Name, target, err)
	}
	c.Report = rep
	c.Files = listFiles(out)
	return c, nil
}

// listFiles returns the files under dir, relative to it and slash-separated.
func listFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error { // #nosec G703 -- walks the converter output dir of the run
		if err == nil && !d.IsDir() {
			if rel, rerr := filepath.Rel(dir, p); rerr == nil {
				out = append(out, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	return out
}

// targetFile is the path prefix of the files each target engine runs.
func targetFile(target string) string {
	switch target {
	case provGHA:
		return ".github/workflows/"
	case provGitLab:
		return ".gitlab-ci.yml"
	case provWoodpecker:
		return ".woodpecker"
	case provDrone:
		return ".drone.yml"
	case provTekton:
		return ".tekton/"
	case provArgo:
		return ".argo/"
	case provConcourse:
		return ".concourse/"
	}
	return ""
}

// wroteTarget reports whether the converter wrote a file the target engine runs.
func (c *Converted) wroteTarget(target string) bool {
	p := targetFile(target)
	return slices.ContainsFunc(c.Files, func(f string) bool { return strings.HasPrefix(f, p) })
}

// reportText summarises the loss report entries for a cell detail.
func (c *Converted) reportText() string {
	if c.Report == nil || len(c.Report.Entries) == 0 {
		return "the report has no entries"
	}
	var parts []string
	for _, e := range c.Report.Entries {
		parts = append(parts, fmt.Sprintf("%s %s %s", e.Level, e.Feature, e.IRPath))
	}
	return "report: " + strings.Join(parts, ", ")
}
