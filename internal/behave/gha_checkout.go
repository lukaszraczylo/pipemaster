//go:build behave

package behave

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// act copies the working tree into the job container and skips actions/checkout, so a job with no checkout step
// still sees the repository there. A real runner starts empty. The check below stands in for that: a job with no
// checkout step is a failure when the repository holds files besides the pipeline files.
const checkoutAction = "actions/checkout"

// repoHoldsFiles is true when the workspace has a file that a job could read, apart from the pipeline files, the
// metadata of the converter and the trace log of the harness.
func repoHoldsFiles(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error { // #nosec G122 -- walks the harness own workspace
		if err != nil || found {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return nil
		}
		top, _, _ := strings.Cut(rel, string(filepath.Separator))
		switch top {
		case ".git", ".github", ".pipemaster":
			return fs.SkipDir
		}
		if d.Type().IsRegular() && d.Name() != "trace.log" {
			found = true
		}
		return nil
	})
	return found
}

// jobsWithoutCheckout lists the jobs of a workflow that run steps and have no checkout step. A job that calls a
// reusable workflow has no steps of its own, so it is not listed.
func jobsWithoutCheckout(raw []byte) ([]string, error) {
	var doc struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string `yaml:"uses"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var out []string
	for name, job := range doc.Jobs {
		if len(job.Steps) == 0 {
			continue
		}
		has := false
		for _, st := range job.Steps {
			if strings.Contains(st.Uses, checkoutAction+"@") {
				has = true
			}
		}
		if !has {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, nil
}

// missingCheckout names the first job, among the workflow files, that would run on an empty runner: it has no
// checkout step while the repository holds files. It returns "" when every job is fine.
func missingCheckout(dir string, workflows []string) (string, error) {
	if !repoHoldsFiles(dir) {
		return "", nil
	}
	for _, f := range workflows {
		raw, err := os.ReadFile(f) // #nosec G304 -- a workspace file of the harness
		if err != nil {
			return "", err
		}
		jobs, err := jobsWithoutCheckout(raw)
		if err != nil {
			return "", fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		if len(jobs) > 0 {
			return fmt.Sprintf("%s: job %s has no %s step, so a real runner has no repository files", filepath.Base(f), jobs[0], checkoutAction), nil
		}
	}
	return "", nil
}
