//go:build behave

package behave

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// droneRun is the build that Drone matches a pipeline trigger against.
type droneRun struct{ event, branch, ref string }

// droneTriggerKeys are the trigger conditions that the harness evaluates. Drone evaluates a trigger in the
// server and in runner-go (manifest/cond.go); drone exec ignores it, so the harness evaluates it before it runs
// a pipeline.
var droneTriggerKeys = []string{"event", "branch", "ref"}

// triggerMatches reports whether the trigger block of a pipeline admits the run. A key that the harness does not
// evaluate is an error, never an admitted run.
func (r droneRun) triggerMatches(trigger map[string]any) (bool, error) {
	for key, raw := range trigger {
		if key == keyStatus {
			continue
		}
		if !slices.Contains(droneTriggerKeys, key) {
			return false, fmt.Errorf("the trigger key %q is not evaluated by the harness", key)
		}
		inc, exc, err := droneCondition(raw)
		if err != nil {
			return false, fmt.Errorf("trigger %s: %w", key, err)
		}
		v := map[string]string{"event": r.event, "branch": r.branch, "ref": r.ref}[key]
		ok, err := condMatches(v, inc, exc)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// keyStatus is the trigger key that tests the status of the build so far, success only by default
// (https://docs.drone.io/pipeline/triggers/#by-status). The harness evaluates it while it runs the pipelines.
const keyStatus = "status"

const (
	statusSuccess = "success"
	statusFailure = "failure"
)

// droneStatus is the status condition of a pipeline trigger.
type droneStatus struct{ inc, exc []string }

// statusOf reads the status condition of a trigger.
func statusOf(trigger map[string]any) (droneStatus, error) {
	raw, ok := trigger[keyStatus]
	if !ok {
		return droneStatus{inc: []string{statusSuccess}}, nil
	}
	inc, exc, err := droneCondition(raw)
	if err != nil {
		return droneStatus{}, fmt.Errorf("trigger %s: %w", keyStatus, err)
	}
	for _, v := range append(slices.Clone(inc), exc...) {
		if v != statusSuccess && v != statusFailure {
			return droneStatus{}, fmt.Errorf("trigger %s: the value %q is unknown", keyStatus, v)
		}
	}
	return droneStatus{inc: inc, exc: exc}, nil
}

// admits reports whether a pipeline with this status condition starts when the build so far is build.
func (s droneStatus) admits(build string) bool {
	if slices.Contains(s.exc, build) {
		return false
	}
	return len(s.inc) == 0 || slices.Contains(s.inc, build)
}

// droneCondition reads a condition: a string, a list, or a map with include and exclude.
func droneCondition(raw any) (inc, exc []string, err error) {
	switch v := raw.(type) {
	case string:
		return []string{v}, nil, nil
	case []any:
		inc, err = stringsOf(v)
		return inc, nil, err
	case map[string]any:
		for k := range v {
			if k != "include" && k != "exclude" {
				return nil, nil, fmt.Errorf("the condition key %q is unknown", k)
			}
		}
		if inc, err = stringsOf(asList(v["include"])); err != nil {
			return nil, nil, err
		}
		exc, err = stringsOf(asList(v["exclude"]))
		return inc, exc, err
	}
	return nil, nil, fmt.Errorf("the condition %v has no known form", raw)
}

func asList(v any) []any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return []any{x}
	case []any:
		return x
	}
	return []any{v}
}

func stringsOf(l []any) ([]string, error) {
	var out []string
	for _, e := range l {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("the value %v is no string", e)
		}
		out = append(out, s)
	}
	return out, nil
}

// condMatches follows Condition.Match of drone/runner-go: an excluded value fails first, then an empty include
// list admits the value and a non-empty one needs a match.
func condMatches(v string, inc, exc []string) (bool, error) {
	for _, p := range exc {
		m, err := globMatch(p, v)
		if err != nil || m {
			return false, err
		}
	}
	if len(inc) == 0 {
		return true, nil
	}
	for _, p := range inc {
		m, err := globMatch(p, v)
		if err != nil {
			return false, err
		}
		if m {
			return true, nil
		}
	}
	return false, nil
}

// globMatch matches the doublestar subset that the converter writes: literal text, ?, * and **. Other
// metacharacters are an error, so the harness never guesses.
func globMatch(pattern, v string) (bool, error) {
	if strings.ContainsAny(pattern, `[]{}\`) {
		return false, fmt.Errorf("the glob %q uses syntax that the harness does not evaluate", pattern)
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MatchString(b.String(), v)
}

// droneDoc is the part of a pipeline document that the harness reads.
type droneDoc struct {
	Trigger   map[string]any `yaml:"trigger"`
	Kind      string         `yaml:"kind"`
	Name      string         `yaml:"name"`
	DependsOn []string       `yaml:"depends_on"`
}

func parseDroneDocs(raw string) ([]droneDoc, error) {
	var out []droneDoc
	for _, doc := range strings.Split(raw, "\n---") {
		var d droneDoc
		if err := yaml.Unmarshal([]byte(doc), &d); err != nil {
			return nil, err
		}
		if d.Kind == "pipeline" {
			out = append(out, d)
		}
	}
	return out, nil
}
