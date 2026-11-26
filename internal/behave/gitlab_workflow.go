//go:build behave

package behave

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gitlab"
)

// glEnv resolves the variables of a run for the rules:if evaluator.
type glEnv map[string]string

func (v glEnv) Lookup(path []string) (expr.Value, bool) {
	name := ""
	if len(path) == 2 && path[0] == gitlab.Dialect {
		name = path[1]
	} else if native, ok := gitlab.Vocab.Dialect(path); ok && len(native) == 1 {
		name = native[0]
	}
	val, ok := v[name]
	return val, ok
}

func (glEnv) Status() expr.Status { return expr.Success }

// workflowAdmits applies workflow:rules to a run the way GitLab does: the first rule that matches decides, a rule
// with when: never creates no pipeline, and a file whose rules match none creates none. gitlab-ci-local 4.77.1
// reads only the variables of workflow:rules (dist/index.js, the workflow branch of the pipeline parser), so the
// harness applies the rest itself. A file without workflow:rules admits every run.
func workflowAdmits(raw []byte, vars map[string]string) (bool, error) {
	var doc struct {
		Workflow struct {
			Rules []struct {
				If   string `yaml:"if"`
				When string `yaml:"when"`
			} `yaml:"rules"`
		} `yaml:"workflow"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return false, err
	}
	if len(doc.Workflow.Rules) == 0 {
		return true, nil
	}
	for _, r := range doc.Workflow.Rules {
		match := true
		if strings.TrimSpace(r.If) != "" {
			e, err := gitlab.Parse(r.If)
			if err != nil {
				return false, fmt.Errorf("workflow rule %q: %w", r.If, err)
			}
			val, err := expr.Eval(e, glEnv(vars), gitlab.Semantics)
			if err != nil {
				return false, fmt.Errorf("workflow rule %q: %w", r.If, err)
			}
			match = gitlab.Semantics.Truthy(val)
		}
		if match {
			return r.When != "never", nil
		}
	}
	return false, nil
}
