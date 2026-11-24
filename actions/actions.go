// Package actions maps GitHub Actions `uses:` refs to constructs on other
// providers. The mappings live in mappings.yaml so contributors add entries
// without touching adapter code.
package actions

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// Target kinds.
const (
	KindImplicit    = "implicit"
	KindScript      = "script"
	KindImage       = "image"
	KindCache       = "cache"
	KindUpload      = "artifact_upload"
	KindDownload    = "artifact_download"
	KindPassthrough = "passthrough"
)

const versionFileCaveat = "version read from file at runtime"

// Target is how one provider expresses an action.
type Target struct {
	With      map[string]any
	Env       map[string]string
	EnvLit    map[string]string
	Kind      string
	Image     string
	Support   caps.Support
	Script    []string
	Unmapped  []string
	Templated []string
}

// WritesEquivalent is true when the target file holds the real equivalent of the action, so the action
// reference is round-trip data only: the implicit clone with a native or emulated verdict. An image choice
// does not count, because a return trip without metadata cannot restore the action from the image.
func (t Target) WritesEquivalent() bool {
	return t.Support.Level <= caps.Emulated && t.Kind == KindImplicit
}

//go:embed mappings.yaml
var embedded []byte

var reverseKinds = []string{"checkout", "cache", "upload", "download"}

var dockerTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// inputName is the shape of an action input name.
var inputName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

var templateVar = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_-]+)\s*\}\}`)

type supportDoc struct {
	Level    string `yaml:"level"`
	Strategy string `yaml:"strategy"`
	Caveat   string `yaml:"caveat"`
}

// withDoc is `out-key` or `{to: out-key, transform: name, exclude: out-key}`.
type withDoc struct {
	To        string `yaml:"to"`
	Transform string `yaml:"transform"`
	Exclude   string `yaml:"exclude"`
}

var withDocKeys = map[string]bool{"to": true, "transform": true, "exclude": true}

func (w *withDoc) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		w.To = n.Value
	case yaml.MappingNode:
		for i := 0; i < len(n.Content); i += 2 {
			if k := n.Content[i].Value; !withDocKeys[k] {
				return fmt.Errorf("line %d: unknown with key %q", n.Content[i].Line, k)
			}
		}
		type plain withDoc
		if err := n.Decode((*plain)(w)); err != nil {
			return err
		}
	default:
		return fmt.Errorf("line %d: with value must be a key or a mapping", n.Line)
	}
	if w.To == "" {
		return fmt.Errorf("line %d: with mapping needs a non-empty to", n.Line)
	}
	return nil
}

// emulateDoc describes the script step that stands in for inputs with no keyword on a target.
type emulateDoc struct {
	Env      map[string]string `yaml:"env"`
	Defaults map[string]string `yaml:"defaults"`
	HostEnv  string            `yaml:"host_env"`
	Image    string            `yaml:"image"`
	Script   string            `yaml:"script"`
	Strategy string            `yaml:"strategy"`
	Caveat   string            `yaml:"caveat"`
	Inputs   []string          `yaml:"inputs"`
	Targets  []string          `yaml:"targets"`
	External []string          `yaml:"external"`
}

type targetDoc struct {
	With map[string]withDoc `yaml:"with"`
	// Gating names inputs that make the step lost on this target alone.
	Gating   map[string][]string `yaml:"gating"`
	Defaults map[string]string   `yaml:"defaults"`
	Support  supportDoc          `yaml:"support"`
	Kind     string              `yaml:"kind"`
	Image    string              `yaml:"image"`
	Script   []string            `yaml:"script"`
}

type entryDoc struct {
	Targets map[string]targetDoc `yaml:"targets"`
	// Gating names inputs whose value makes the step fail or branch, which a target without that input cannot keep.
	Gating map[string][]string `yaml:"gating"`
	// Emulate turns a step with inputs that no keyword carries into a script step on every target but GHA.
	Emulate *emulateDoc `yaml:"emulate"`
	Match   string      `yaml:"match"`
	Path    string      `yaml:"path"`
}

type fileDoc struct {
	Reverse map[string]string `yaml:"reverse"`
	Actions []entryDoc        `yaml:"actions"`
}

type entry struct {
	targets map[string]compiled
	gating  map[string][]string
	emulate *emulateDoc
	owner   string
	repo    string
	path    string
}

type compiled struct {
	doc     targetDoc
	support caps.Support
}

type registry struct {
	reverse map[string]ir.ActionRef
	entries []entry
}

var (
	loadOnce sync.Once
	loaded   *registry
	loadErr  error
)

// Load parses the embedded mappings once and returns any parse error.
func Load() error {
	loadOnce.Do(func() { loaded, loadErr = parse(embedded) })
	return loadErr
}

// MustLoad panics when the embedded mappings are malformed.
func MustLoad() {
	if err := Load(); err != nil {
		panic(fmt.Sprintf("actions: embedded mappings.yaml: %v", err))
	}
}

func parse(src []byte) (*registry, error) {
	var doc fileDoc
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	r := &registry{reverse: map[string]ir.ActionRef{}}
	for i, e := range doc.Actions {
		owner, name, ok := strings.Cut(e.Match, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return nil, fmt.Errorf("actions[%d]: match %q must be owner/repo", i, e.Match)
		}
		for in, vals := range e.Gating {
			if len(vals) == 0 {
				return nil, fmt.Errorf("actions[%d]: gating %q needs at least one value", i, in)
			}
		}
		for in := range e.Gating {
			if !inputName.MatchString(in) {
				return nil, fmt.Errorf("actions[%d]: gating input %q is not an input name", i, in)
			}
		}
		if em := e.Emulate; em != nil {
			if len(em.Inputs) == 0 || em.Script == "" || len(em.Targets) == 0 {
				return nil, fmt.Errorf("actions[%d]: emulate needs inputs and a script", i)
			}
			for _, in := range append(slices.Clone(em.Inputs), em.External...) {
				if !inputName.MatchString(in) {
					return nil, fmt.Errorf("actions[%d]: emulate input %q is not an input name", i, in)
				}
			}
			for v, in := range em.Env {
				if !inputName.MatchString(in) || v == "" {
					return nil, fmt.Errorf("actions[%d]: emulate env %q maps to %q, which is not an input name", i, v, in)
				}
			}
		}
		ent := entry{owner: owner, repo: name, path: e.Path, gating: e.Gating, emulate: e.Emulate, targets: map[string]compiled{}}
		for id, t := range e.Targets {
			lvl, err := caps.ParseLevel(t.Support.Level)
			if err != nil {
				return nil, fmt.Errorf("actions[%d] %s: %w", i, id, err)
			}
			for in, vals := range t.Gating {
				if !inputName.MatchString(in) || len(vals) == 0 {
					return nil, fmt.Errorf("actions[%d] %s: gating %q is not an input name with values", i, id, in)
				}
			}
			for in, w := range t.With {
				if _, ok := transforms[w.Transform]; !ok {
					return nil, fmt.Errorf("actions[%d] %s: with %q: unknown transform %q", i, id, in, w.Transform)
				}
				if w.Exclude != "" && w.Transform != "lines" {
					return nil, fmt.Errorf("actions[%d] %s: with %q: exclude needs transform lines", i, id, in)
				}
			}
			if t.Kind == "" {
				return nil, fmt.Errorf("actions[%d] %s: kind required", i, id)
			}
			ent.targets[id] = compiled{doc: t, support: caps.Support{Level: lvl, Strategy: t.Support.Strategy, Caveat: t.Support.Caveat}}
		}
		r.entries = append(r.entries, ent)
	}
	for _, k := range reverseKinds {
		ref, err := parsePinned(doc.Reverse[k])
		if err != nil {
			return nil, fmt.Errorf("reverse.%s: %w", k, err)
		}
		r.reverse[k] = ref
	}
	return r, nil
}

// githubHost is where the pinned actions live; Lookup ignores it.
const githubHost = "https://github.com"

func parsePinned(s string) (ir.ActionRef, error) {
	nameRef, ref, ok := strings.Cut(s, "@")
	if !ok || ref == "" {
		return ir.ActionRef{}, fmt.Errorf("%q must be owner/repo@ref", s)
	}
	owner, name, ok := strings.Cut(nameRef, "/")
	if !ok || owner == "" || name == "" {
		return ir.ActionRef{}, fmt.Errorf("%q must be owner/repo@ref", s)
	}
	return ir.ActionRef{Kind: ir.ActionRepo, Host: githubHost, Owner: owner, Repo: name, Ref: ref}, nil
}

// Reverse returns the pinned GitHub action for a GitLab-side construct:
// checkout, cache, upload or download.
func Reverse(kind string) (ir.ActionRef, bool) {
	MustLoad()
	ref, ok := loaded.reverse[kind]
	return ref, ok
}

// Lookup resolves a `uses:` ref and its `with` inputs for the target provider.
// Docker and local refs never match; unknown refs or targets return false.
func Lookup(ref ir.ActionRef, target string, with map[string]string) (Target, bool) {
	MustLoad()
	return loaded.lookup(ref, target, with)
}

func (r *registry) lookup(ref ir.ActionRef, target string, with map[string]string) (Target, bool) {
	if ref.Kind != ir.ActionRepo {
		return Target{}, false
	}
	for _, e := range r.entries {
		if !strings.EqualFold(e.owner, ref.Owner) || !strings.EqualFold(e.repo, ref.Repo) || e.path != ref.Path {
			continue
		}
		c, ok := e.targets[provider.Family(target)]
		if !ok {
			return Target{}, false
		}
		if provider.Family(target) != provider.GHA && (gates(e.gating, with) || gates(c.doc.Gating, with)) {
			return Target{}, false
		}
		if provider.Family(target) != provider.GHA && e.emulate != nil && slices.Contains(e.emulate.Targets, provider.Family(target)) && sets(withoutDefaults(with, e.emulate.Defaults), e.emulate.Inputs) {
			return emulated(*e.emulate, ref, withoutDefaults(with, e.emulate.Defaults), resolve(c, target, with)), true
		}
		return resolve(c, target, with), true
	}
	return Target{}, false
}

// withoutDefaults drops the inputs that hold their default.
func withoutDefaults(with, defaults map[string]string) map[string]string {
	out := make(map[string]string, len(with))
	for k, v := range with {
		if d, ok := defaults[k]; !ok || strings.TrimSpace(v) != d {
			out[k] = v
		}
	}
	return out
}

// sets is true when the step gives a non-empty value to one of the inputs.
func sets(with map[string]string, inputs []string) bool {
	for _, in := range inputs {
		if strings.TrimSpace(with[in]) != "" {
			return true
		}
	}
	return false
}

// emulated builds the script target for a step whose inputs have no keyword.
// The inputs that the target maps itself stay mapped, and the script carries only the ones it does not.
func emulated(em emulateDoc, ref ir.ActionRef, with map[string]string, native Target) Target {
	t := Target{Kind: KindScript, Script: []string{em.Script}, Image: em.Image, With: native.With, Env: map[string]string{}}
	if t.With == nil {
		t.With = map[string]any{}
	}
	for _, u := range native.Unmapped {
		if !slices.Contains(em.Inputs, u) {
			t.Unmapped = append(t.Unmapped, u)
		}
	}
	for v, in := range em.Env {
		if strings.TrimSpace(with[in]) != "" {
			t.Env[v] = in
		}
	}
	if em.HostEnv != "" {
		host := ref.Host
		if host == "" {
			host = githubHost
		}
		t.EnvLit = map[string]string{em.HostEnv: host}
	}
	level := caps.Emulated
	if sets(with, em.External) {
		level = caps.External
	}
	t.Support = caps.Support{Level: level, Strategy: em.Strategy, Caveat: em.Caveat}
	return t
}

// gatingAny in a gating list stands for every value except an empty one and false.
const gatingAny = "*"

// gates is true when an input of the step holds a value that makes the step fail or branch, which no other
// target can keep; the step then has no mapping and the emitters write a failing step.
func gates(gating map[string][]string, with map[string]string) bool {
	for in, vals := range gating {
		v, ok := with[in]
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		for _, want := range vals {
			if want == gatingAny && v != "" && !strings.EqualFold(v, "false") || strings.EqualFold(v, want) {
				return true
			}
		}
	}
	return false
}

func resolve(c compiled, target string, with map[string]string) Target {
	out := Target{Kind: c.doc.Kind, Script: c.doc.Script, Support: c.support, With: map[string]any{}}
	if provider.Family(target) == provider.GHA || c.doc.Kind == KindPassthrough {
		for k, v := range with {
			out.With[k] = v
		}
		return out
	}

	consumed := map[string]bool{}
	ins := make([]string, 0, len(c.doc.With))
	for in := range c.doc.With {
		ins = append(ins, in)
		consumed[in] = true
	}
	sort.Strings(ins)
	for _, in := range ins {
		v, ok := with[in]
		if !ok {
			continue
		}
		w := c.doc.With[in]
		val, err := transforms[w.Transform](v)
		if err == nil && w.Transform == "lines" {
			val, err = splitNegated(val.([]string), w, out.With)
		}
		if err != nil {
			out.Support = lose(out.Support, fmt.Sprintf("input %s: %v", in, err))
			continue
		}
		out.With[w.To] = val
		if w.Transform == "" && strings.Contains(v, "${{") {
			out.Templated = append(out.Templated, w.To)
		}
	}
	sort.Strings(out.Templated)
	if c.doc.Image != "" {
		img, problem := renderImage(c, with, consumed)
		if problem != "" {
			out.Support = lose(out.Support, problem)
		} else {
			out.Image = img
		}
	}
	for k := range with {
		if !consumed[k] {
			out.Unmapped = append(out.Unmapped, k)
		}
	}
	sort.Strings(out.Unmapped)
	return out
}

// lose downgrades to lost, keeping the first caveat.
func lose(s caps.Support, caveat string) caps.Support {
	if s.Level == caps.Lost {
		return s
	}
	s.Level = caps.Lost
	s.Caveat = caveat
	return s
}

// renderImage substitutes inputs into the image template. A missing or
// non-tag input yields a problem string rather than a bad image reference.
func renderImage(c compiled, with map[string]string, consumed map[string]bool) (string, string) {
	var problem string
	img := templateVar.ReplaceAllStringFunc(c.doc.Image, func(m string) string {
		name := templateVar.FindStringSubmatch(m)[1]
		consumed[name] = true
		consumed[name+"-file"] = true
		v := with[name]
		if v == "" {
			v = c.doc.Defaults[name]
		}
		switch {
		case v == "" && with[name+"-file"] != "":
			problem = firstNonEmpty(problem, versionFileCaveat)
		case v == "":
			problem = firstNonEmpty(problem, "missing input "+name)
		case strings.Contains(v, "${{"):
			problem = firstNonEmpty(problem, "input "+name+": version is an expression")
		case !dockerTag.MatchString(v):
			problem = firstNonEmpty(problem, "input "+name+": not a plain tag")
		}
		return v
	})
	if problem != "" {
		return "", problem
	}
	return img, ""
}

// splitNegated moves GHA `!pattern` entries into the target's exclude key;
// GitLab path lists have no negation syntax.
func splitNegated(lines []string, w withDoc, with map[string]any) ([]string, error) {
	var pos, neg []string
	for _, l := range lines {
		if rest, ok := strings.CutPrefix(l, "!"); ok {
			neg = append(neg, rest)
		} else {
			pos = append(pos, l)
		}
	}
	if len(neg) > 0 {
		if w.Exclude == "" {
			return nil, errors.New("negated path pattern has no GitLab equivalent")
		}
		with[w.Exclude] = neg
	}
	if len(pos) == 0 {
		return nil, errors.New("no paths")
	}
	return pos, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var (
	digits  = regexp.MustCompile(`^[0-9]+$`)
	posInt  = regexp.MustCompile(`^[1-9][0-9]*$`)
	errExpr = errors.New("value is an expression")
)

// transforms is the closed set of per-key value conversions; "" is identity.
var transforms = map[string]func(string) (any, error){
	// Identity keeps `${{ }}` verbatim; the emitter translates it (see Target.Templated).
	"": func(v string) (any, error) { return v, nil },
	// GitLab expire_in without a unit is seconds; GHA retention-days 0 means the repository default.
	"days": func(v string) (any, error) {
		v = strings.TrimSpace(v)
		switch {
		case strings.Contains(v, "${{"):
			return nil, errExpr
		case v == "1":
			return "1 day", nil
		case posInt.MatchString(v):
			return v + " days", nil
		}
		return nil, fmt.Errorf("%q is not a positive day count", v)
	},
	"submodules": func(v string) (any, error) {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true":
			return "normal", nil
		case "false":
			return "none", nil
		case "recursive":
			return "recursive", nil
		}
		if strings.Contains(v, "${{") {
			return nil, errExpr
		}
		return nil, fmt.Errorf("%q is not true, false or recursive", v)
	},
	// GHA fetch-depth 0 and GitLab GIT_DEPTH 0 both mean full history.
	"depth": func(v string) (any, error) {
		v = strings.TrimSpace(v)
		if strings.Contains(v, "${{") {
			return nil, errExpr
		}
		if !digits.MatchString(v) {
			return nil, fmt.Errorf("%q is not a non-negative integer", v)
		}
		return v, nil
	},
	"lines": func(v string) (any, error) {
		if strings.Contains(v, "${{") {
			return nil, errExpr
		}
		var out []string
		for _, l := range strings.Split(v, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		if len(out) == 0 {
			return nil, errors.New("no paths")
		}
		return out, nil
	},
}
