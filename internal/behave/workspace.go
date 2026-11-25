//go:build behave

package behave

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const fixtureFile = "fixture.json"

// Fixture is one synthetic pipeline of one source provider.
type Fixture struct {
	Name      string              `json:"-"`
	Source    string              `json:"-"`
	Dir       string              `json:"-"`
	Features  []string            `json:"features"`
	Events    []string            `json:"events"`
	Owners    map[string][]string `json:"owners"`
	Expect    []string            `json:"expect"`
	Requires  []string            `json:"requires"`
	Failure   bool                `json:"expect_failure"`
	Unordered bool                `json:"unordered"`
}

// LoadFixtures reads every testdata/behave/<source>/<fixture> directory, sorted.
func LoadFixtures(root string) ([]Fixture, error) {
	base := filepath.Join(root, "testdata", "behave")
	srcs, err := os.ReadDir(base)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", base, err)
	}
	var out []Fixture
	for _, s := range srcs {
		if !s.IsDir() {
			continue
		}
		fxs, err := os.ReadDir(filepath.Join(base, s.Name()))
		if err != nil {
			return nil, fmt.Errorf("read fixtures of %s: %w", s.Name(), err)
		}
		for _, f := range fxs {
			if !f.IsDir() {
				continue
			}
			dir := filepath.Join(base, s.Name(), f.Name())
			raw, err := os.ReadFile(filepath.Join(dir, fixtureFile)) // #nosec G304 -- path built from the testdata tree
			if err != nil {
				return nil, fmt.Errorf("fixture %s/%s: %w", s.Name(), f.Name(), err)
			}
			fx := Fixture{Name: f.Name(), Source: s.Name(), Dir: dir}
			if err := json.Unmarshal(raw, &fx); err != nil {
				return nil, fmt.Errorf("fixture %s/%s: %w", s.Name(), f.Name(), err)
			}
			out = append(out, fx)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Workspace is a throwaway git repository holding one pipeline for one event.
type Workspace struct {
	Dir string
	SHA string
	Ev  Event
}

// NewWorkspace copies src (without fixture.json) into a fresh git repository and positions it for the event: main
// for a main push, feature/x for a branch push and a pull request, and a tag on main for a tag.
func (e *Env) NewWorkspace(ctx context.Context, src, dst string, ev Event) (*Workspace, error) {
	if err := os.RemoveAll(dst); err != nil { // #nosec G703 -- dst is a path under the run work dir
		return nil, fmt.Errorf("clear workspace: %w", err)
	}
	if err := copyTree(src, dst); err != nil {
		return nil, err
	}
	steps := [][]string{
		{"init", "-q", "-b", mainBranch},
		{"add", "-A"},
		{"-c", "user.name=behave", "-c", "user.email=behave@example.invalid", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture"},
	}
	switch ev.Kind {
	case kindTag:
		steps = append(steps, []string{"-c", "tag.gpgsign=false", "tag", ev.Tag})
	case kindPR, kindPush:
		if ev.Branch != mainBranch {
			steps = append(steps, []string{"checkout", "-q", "-b", ev.Branch})
		}
	}
	for _, a := range steps {
		if out, code, err := e.run(ctx, runOpts{Dir: dst, Timeout: time.Minute}, "git", a...); err != nil || code != 0 {
			return nil, fmt.Errorf("git %s: %v %s", strings.Join(a, " "), err, out)
		}
	}
	out, code, err := e.run(ctx, runOpts{Dir: dst, Timeout: time.Minute}, "git", "rev-parse", "HEAD")
	if err != nil || code != 0 {
		return nil, fmt.Errorf("git rev-parse: %v %s", err, out)
	}
	return &Workspace{Dir: dst, SHA: strings.TrimSpace(out), Ev: ev}, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error { // #nosec G703 -- walks the harness own tree
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750) // #nosec G703 -- target is under the run work dir
		}
		if rel == fixtureFile || !d.Type().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(p) // #nosec G304 G122 -- walked inside the harness own testdata or output tree
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o600) // #nosec G703 -- target is under the run work dir
	})
}

// exists reports whether a path exists under dir.
func exists(dir string, parts ...string) bool {
	_, err := os.Stat(filepath.Join(append([]string{dir}, parts...)...))
	return err == nil
}
