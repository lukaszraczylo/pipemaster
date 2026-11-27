//go:build behave

package behave

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ghaOn reads the on: key of a workflow in its three forms: a name, a list of names and a map of names.
func ghaOn(raw []byte) (map[string]map[string]any, error) {
	var doc struct {
		On any `yaml:"on"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := map[string]map[string]any{}
	switch on := doc.On.(type) {
	case string:
		out[on] = nil
	case []any:
		for _, n := range on {
			name, _ := n.(string)
			out[name] = nil
		}
	case map[string]any:
		for n, v := range on {
			cfg, _ := v.(map[string]any)
			out[n] = cfg
		}
	}
	return out, nil
}

// ghaAdmits applies the ref filters of on.push and on.pull_request to one run, as the GitHub docs state them: a
// filter of one kind (branches or tags) leaves the other kind of ref out, a run of a ref that has no filter is
// admitted only when neither kind is set, a pull request matches the base branch, and both kinds match as either
// (https://docs.github.com/en/actions/writing-workflows/workflow-syntax-for-github-actions, on.push.branches).
// act 0.2.90 ignores these filters, so the harness applies them. A path filter is not evaluated and admits the run.
func ghaAdmits(raw []byte, w *Workspace) (bool, error) {
	on, err := ghaOn(raw)
	if err != nil {
		return false, err
	}
	event, branch, tag := w.Ev.Kind, w.Ev.Branch, ""
	switch w.Ev.Kind {
	case kindTag:
		event, branch, tag = kindPush, "", w.Ev.Tag
	case kindPR:
		branch = w.Ev.Target
	}
	cfg, ok := on[event]
	if !ok {
		return false, nil
	}
	branches, tags := ghaFilter(cfg, "branches"), ghaFilter(cfg, "tags")
	bIgnore, tIgnore := ghaFilter(cfg, "branches-ignore"), ghaFilter(cfg, "tags-ignore")
	branchKind := len(branches)+len(bIgnore) > 0
	tagKind := len(tags)+len(tIgnore) > 0
	if err := ghaCheckGlobs(branches, tags, bIgnore, tIgnore); err != nil {
		return false, err
	}
	if event == kindPR {
		if !branchKind {
			return true, nil
		}
		return condMatches(branch, branches, bIgnore)
	}
	if !branchKind && !tagKind {
		return true, nil
	}
	if tag != "" {
		return refAdmitted(tag, tags, tIgnore, tagKind)
	}
	return refAdmitted(branch, branches, bIgnore, branchKind)
}

// ghaCheckGlobs refuses the filter patterns whose GitHub meaning differs from globMatch. In a GitHub filter ?
// matches zero or one of the preceding character, + matches one or more of it, and a leading ! negates the
// pattern (https://docs.github.com/en/actions/writing-workflows/workflow-syntax-for-github-actions#filter-pattern-cheat-sheet).
// globMatch reads ? as one character and + as a literal, so the harness does not guess.
func ghaCheckGlobs(lists ...[]string) error {
	for _, l := range lists {
		for _, p := range l {
			if strings.ContainsAny(p, "?+") || strings.HasPrefix(p, "!") {
				return fmt.Errorf("the filter pattern %q uses a GitHub glob form that the harness does not evaluate", p)
			}
		}
	}
	return nil
}

// refAdmitted admits a name that a filter of its kind admits. A kind with no filter admits nothing when the other kind has one.
func refAdmitted(name string, inc, exc []string, set bool) (bool, error) {
	if !set {
		return false, nil
	}
	return condMatches(name, inc, exc)
}

func ghaFilter(cfg map[string]any, key string) []string {
	var out []string
	for _, v := range asList(cfg[key]) {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ghaWorkflows returns the workflow files of the workspace that admit the run, and how many files exist.
func ghaWorkflows(w *Workspace) (admitted []string, total int, err error) {
	files, gerr := filepath.Glob(filepath.Join(w.Dir, ".github", "workflows", "*.y*ml"))
	if gerr != nil {
		return nil, 0, fmt.Errorf("list the workflow files: %w", gerr)
	}
	for _, f := range files {
		raw, rerr := os.ReadFile(f) // #nosec G304 -- a workspace file of the harness
		if rerr != nil {
			return nil, 0, rerr
		}
		ok, aerr := ghaAdmits(raw, w)
		if aerr != nil {
			return nil, 0, fmt.Errorf("%s: %w", filepath.Base(f), aerr)
		}
		if ok {
			admitted = append(admitted, f)
		}
	}
	return admitted, len(files), nil
}
