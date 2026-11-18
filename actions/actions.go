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
	With     map[string]any // string, or []string for list-valued keys
	Kind     string
	Image    string
	Support  caps.Support
	Script   []string
	Unmapped []string // `with` keys the entry does not map; the emitter must report them
}

//go:embed mappings.yaml
var embedded []byte

var reverseKinds = []string{"checkout", "cache", "upload", "download"}

var dockerTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

var templateVar = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_-]+)\s*\}\}`)

type supportDoc struct {
	Level    string `yaml:"level"`
	Strategy string `yaml:"strategy"`
	Caveat   string `yaml:"caveat"`
}

// withDoc is `out-key` or `{to: out-key, transform: name}`.
type withDoc struct {
	To        string `yaml:"to"`
	Transform string `yaml:"transform"`
}

func (w *withDoc) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		w.To = n.Value
		return nil
	}
	type plain withDoc
	return n.Decode((*plain)(w))
}

type targetDoc struct {
	With     map[string]withDoc `yaml:"with"`
	Defaults map[string]string  `yaml:"defaults"`
	Support  supportDoc         `yaml:"support"`
	Kind     string             `yaml:"kind"`
	Image    string             `yaml:"image"`
	Script   []string           `yaml:"script"`
}

type entryDoc struct {
	Targets map[string]targetDoc `yaml:"targets"`
	Match   string               `yaml:"match"`
	Path    string               `yaml:"path"`
}

type fileDoc struct {
	Reverse map[string]string `yaml:"reverse"`
	Actions []entryDoc        `yaml:"actions"`
}

type entry struct {
	targets map[string]compiled
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
		ent := entry{owner: owner, repo: name, path: e.Path, targets: map[string]compiled{}}
		for id, t := range e.Targets {
			lvl, err := caps.ParseLevel(t.Support.Level)
			if err != nil {
				return nil, fmt.Errorf("actions[%d] %s: %w", i, id, err)
			}
			for in, w := range t.With {
				if _, ok := transforms[w.Transform]; !ok {
					return nil, fmt.Errorf("actions[%d] %s: with %q: unknown transform %q", i, id, in, w.Transform)
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

func parsePinned(s string) (ir.ActionRef, error) {
	nameRef, ref, ok := strings.Cut(s, "@")
	if !ok || ref == "" {
		return ir.ActionRef{}, fmt.Errorf("%q must be owner/repo@ref", s)
	}
	owner, name, ok := strings.Cut(nameRef, "/")
	if !ok || owner == "" || name == "" {
		return ir.ActionRef{}, fmt.Errorf("%q must be owner/repo@ref", s)
	}
	return ir.ActionRef{Kind: ir.ActionRepo, Owner: owner, Repo: name, Ref: ref}, nil
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
		c, ok := e.targets[target]
		if !ok {
			return Target{}, false
		}
		return resolve(c, target, with), true
	}
	return Target{}, false
}

func resolve(c compiled, target string, with map[string]string) Target {
	out := Target{Kind: c.doc.Kind, Script: c.doc.Script, Support: c.support, With: map[string]any{}}
	if target == provider.GHA || c.doc.Kind == KindPassthrough {
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
		if err != nil {
			out.Support = lose(out.Support, fmt.Sprintf("input %s: %v", in, err))
			continue
		}
		out.With[w.To] = val
	}
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
	"": func(v string) (any, error) {
		if strings.Contains(v, "${{") {
			return nil, errExpr
		}
		return v, nil
	},
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
