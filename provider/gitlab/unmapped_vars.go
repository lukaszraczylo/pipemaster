package gitlab

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
)

// predefinedRefRE matches a reference to a GitLab-set variable ($CI_X, ${CI_X}, $GITLAB_X) or to a spec input
// ($[[ ... ]]); an escaped $$ is skipped first so that it never starts a match.
var predefinedRefRE = regexp.MustCompile(`\$\$|\$\{?((?:CI|GITLAB)_[A-Za-z0-9_]*)|\$\[\[[^\]]*\]\]`)

// unmappedRefs lists the GitLab-set variables and spec inputs in text that the vocabulary has no canonical
// reference for and that the pipeline does not define itself.
func unmappedRefs(text string, defined map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range predefinedRefRE.FindAllStringSubmatch(text, -1) {
		ref := m[0]
		switch {
		case ref == "$$":
			continue
		case m[1] != "":
			if _, mapped := exprgitlab.Vocab.Canon([]string{m[1]}); mapped || defined[m[1]] {
				continue
			}
			ref = "$" + m[1]
		}
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

// noteUnmappedRefs reports each GitLab-set variable and spec input under n that no other target sets or expands.
// Scripts and every string field are covered because it walks the YAML, not the IR. The variables block is
// skipped, since varValue reports its values.
func (s *state) noteUnmappedRefs(path string, n *yaml.Node, defined map[string]bool) {
	reported := map[string]bool{}
	var walk func(*yaml.Node)
	walk = func(c *yaml.Node) {
		switch c.Kind {
		case yaml.AliasNode:
			walk(c.Alias)
		case yaml.SequenceNode:
			for _, e := range c.Content {
				walk(e)
			}
		case yaml.MappingNode:
			for _, e := range entries(c) {
				if e.name != "variables" {
					walk(e.val)
				}
			}
		case yaml.ScalarNode:
			for _, ref := range unmappedRefs(c.Value, defined) {
				if reported[ref] {
					continue
				}
				reported[ref] = true
				s.note(path, caps.ScriptInterpolation, "kept as GitLab text", "value references "+ref+", which GitLab sets or expands; other targets leave it empty or unexpanded", c)
			}
		}
	}
	walk(n)
}

// definedVars lists the names a variables block sets.
func definedVars(defined map[string]bool, vars *yaml.Node) map[string]bool {
	out := map[string]bool{}
	for k := range defined {
		out[k] = true
	}
	for _, e := range entries(vars) {
		out[e.name] = true
	}
	return out
}

// noteVariableInputs reports spec inputs in a variables block; the other references there are varValue's.
func (s *state) noteVariableInputs(path string, vars *yaml.Node) {
	for _, e := range entries(vars) {
		for _, ref := range unmappedRefs(e.val.Value, nil) {
			if strings.HasPrefix(ref, "$[[") {
				s.note(path+"/env/"+e.name, caps.ScriptInterpolation, "kept as GitLab text", "value references "+ref+", which GitLab expands when it creates the pipeline; other targets do not", e.val)
			}
		}
	}
}
