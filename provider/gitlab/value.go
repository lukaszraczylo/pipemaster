package gitlab

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// reader tracks which keys of a mapping a parser consumed, so the rest can go to Ext.
type reader struct {
	s    *state
	n    *yaml.Node
	used map[string]bool
	kept map[string]any
	ents []entry
	// nulls are keys set to null, which unset what a parent job gave; they go to Ext untouched.
	nulls []string
}

func (s *state) reader(n *yaml.Node, what string) (*reader, error) {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, s.errf(n, "%s must be a mapping", what)
	}
	return &reader{s: s, n: n, used: map[string]bool{}, ents: entries(n)}, nil
}

// take returns the value under key and marks it consumed; release undoes that when the form is not modelled.
func (r *reader) take(key string) *yaml.Node {
	for _, e := range r.ents {
		if e.name == key {
			r.used[key] = true
			return e.val
		}
	}
	return nil
}

func (r *reader) peek(key string) *yaml.Node {
	if n := get(r.n, key); !isNull(n) {
		return n
	}
	return nil
}

// setNullsAside moves the null-valued keys out of the entries a parser reads.
func (r *reader) setNullsAside() {
	live := r.ents[:0:0]
	for _, e := range r.ents {
		if isNull(e.val) {
			r.nulls = append(r.nulls, e.name)
			continue
		}
		live = append(live, e)
	}
	r.ents = live
}

func (r *reader) release(key string) { delete(r.used, key) }

// keep records a provider-native value for Ext that has no unconsumed key behind it.
func (r *reader) keep(key string, v any) {
	if r.kept == nil {
		r.kept = map[string]any{}
	}
	r.kept[key] = v
}

func (r *reader) ext() (ir.Ext, error) {
	var out map[string]any
	for _, e := range r.ents {
		if r.used[e.name] {
			continue
		}
		v, err := r.s.plain(e.val)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = map[string]any{}
		}
		out[e.name] = v
	}
	for _, k := range r.nulls {
		if out == nil {
			out = map[string]any{}
		}
		out[k] = nil
	}
	for k, v := range r.kept {
		if out == nil {
			out = map[string]any{}
		}
		out[k] = v
	}
	if out == nil {
		return nil, nil
	}
	return ir.Ext{dialect: out}, nil
}

func scalarText(n *yaml.Node) (string, bool) {
	if n == nil || n.Kind != yaml.ScalarNode {
		return "", false
	}
	if n.Tag == tagNull {
		return "", true
	}
	return n.Value, true
}

func (s *state) scalar(n *yaml.Node, what string) (string, error) {
	t, ok := scalarText(n)
	if !ok {
		return "", s.errf(n, "%s must be a scalar", what)
	}
	return t, nil
}

// stringList reads a scalar or a list of scalars; ok is false for any other shape.
func stringList(n *yaml.Node) ([]string, bool) {
	if n == nil {
		return nil, false
	}
	if t, ok := scalarText(n); ok {
		if isNull(n) {
			return nil, true
		}
		return []string{t}, true
	}
	if n.Kind != yaml.SequenceNode {
		return nil, false
	}
	out := make([]string, 0, len(n.Content))
	for _, c := range n.Content {
		t, ok := scalarText(c)
		if !ok {
			return nil, false
		}
		out = append(out, t)
	}
	return out, true
}

func boolNode(n *yaml.Node) (val, ok bool) {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag != "!!bool" {
		return false, false
	}
	var b bool
	if n.Decode(&b) != nil {
		return false, false
	}
	return b, true
}

func intNode(n *yaml.Node) (int, bool) {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
		return 0, false
	}
	i, err := strconv.Atoi(n.Value)
	return i, err == nil
}

// canonText rewrites known GitLab variables in s as canonical ${{ }} references and escapes a literal "${{".
func canonText(s string) (string, error) {
	parts := splitVars(s)
	out, err := expr.PrintScriptTemplate(parts)
	if err != nil {
		return "", fmt.Errorf("script template: %w", err)
	}
	return out, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isIdentPart(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

// splitVars cuts s into text and canonical refs for $NAME and ${NAME} where NAME is in the vocabulary; "$$" is left alone.
func splitVars(s string) []expr.Part {
	var parts []expr.Part
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			parts = append(parts, expr.Part{Text: text.String()})
			text.Reset()
		}
	}
	for i := 0; i < len(s); {
		if s[i] != '$' {
			text.WriteByte(s[i])
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' {
			text.WriteString("$$")
			i += 2
			continue
		}
		name, end := varAt(s, i)
		if canon, ok := exprgitlab.Vocab.Canon([]string{name}); ok && name != "" {
			flush()
			parts = append(parts, expr.Part{Expr: expr.Ref{Path: canon}})
			i = end
			continue
		}
		text.WriteByte('$')
		i++
	}
	flush()
	return parts
}

// varAt reads the variable name that starts at the "$" in s[i]; name is empty when none starts there.
func varAt(s string, i int) (name string, end int) {
	j := i + 1
	braced := j < len(s) && s[j] == '{'
	if braced {
		j++
	}
	start := j
	if j >= len(s) || !isIdentStart(s[j]) {
		return "", i + 1
	}
	for j < len(s) && isIdentPart(s[j]) {
		j++
	}
	if braced {
		if j >= len(s) || s[j] != '}' {
			return "", i + 1
		}
		return s[start:j], j + 1
	}
	return s[start:j], j
}

// scriptLines flattens a script value, canonicalises every entry and splits it into lines.
func (s *state) scriptLines(n *yaml.Node, what string) ([]string, error) {
	var out []string
	err := s.eachScriptItem(n, what, func(_ *yaml.Node, lines []string) { out = append(out, lines...) })
	return out, err
}

// eachScriptItem calls fn with every script item node and the lines it holds.
func (s *state) eachScriptItem(n *yaml.Node, what string, fn func(item *yaml.Node, lines []string)) error {
	var walk func(*yaml.Node) error
	walk = func(c *yaml.Node) error {
		switch c.Kind {
		case yaml.SequenceNode:
			for _, e := range c.Content {
				if err := walk(e); err != nil {
					return err
				}
			}
			return nil
		case yaml.ScalarNode:
			t, _ := scalarText(c)
			canon, err := canonText(t)
			if err != nil {
				return s.errf(c, "%v", err)
			}
			fn(c, strings.Split(strings.TrimSuffix(canon, "\n"), "\n"))
			return nil
		}
		return s.errf(c, "%s entries must be strings; quote a command that contains a colon", what)
	}
	return walk(n)
}

// text reads a scalar as a canonical template.
func (s *state) text(n *yaml.Node, what string) (string, error) {
	t, err := s.scalar(n, what)
	if err != nil {
		return "", err
	}
	canon, err := canonText(t)
	if err != nil {
		return "", s.errf(n, "%v", err)
	}
	return canon, nil
}

var (
	durTokenRE = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*([a-z]*)`)
	durWords   = strings.NewReplacer(
		"one", "1", "two", "2", "three", "3", "four", "4", "five", "5",
		"six", "6", "seven", "7", "eight", "8", "nine", "9", "ten", "10", " and ", " ", ",", " ",
	)
	durUnits = map[string]time.Duration{
		"s": time.Second, "sec": time.Second, "secs": time.Second, "second": time.Second, "seconds": time.Second, "": time.Second,
		"m": time.Minute, "min": time.Minute, "mins": time.Minute, "minute": time.Minute, "minutes": time.Minute,
		"h": time.Hour, "hr": time.Hour, "hrs": time.Hour, "hour": time.Hour, "hours": time.Hour,
		"d": 24 * time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour,
		"w": 7 * 24 * time.Hour, "week": 7 * 24 * time.Hour, "weeks": 7 * 24 * time.Hour,
	}
)

// parseDuration reads GitLab's natural-language durations: "3600 seconds", "3h 30m", "one hour", "1h30m".
func parseDuration(s string) (ir.Duration, bool) {
	s = durWords.Replace(strings.ToLower(strings.TrimSpace(s)))
	locs := durTokenRE.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return 0, false
	}
	var total float64
	pos := 0
	for _, m := range locs {
		if strings.TrimSpace(s[pos:m[0]]) != "" {
			return 0, false
		}
		pos = m[1]
		unit, ok := durUnits[s[m[4]:m[5]]]
		if !ok {
			return 0, false
		}
		f, err := strconv.ParseFloat(s[m[2]:m[3]], 64)
		if err != nil {
			return 0, false
		}
		total += f * float64(unit)
	}
	if strings.TrimSpace(s[pos:]) != "" || total > float64(1<<62) {
		return 0, false
	}
	return ir.Duration(time.Duration(total)), true
}
