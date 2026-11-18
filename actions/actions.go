// Package actions maps GitHub Actions `uses:` refs to constructs on other
// providers. The mappings live in mappings.yaml so contributors add entries
// without touching adapter code.
package actions

import (
	"bytes"
	_ "embed"
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
	With     map[string]string
	Kind     string
	Image    string
	Support  caps.Support
	Script   []string
	Unmapped []string // `with` keys the entry does not map; the emitter must report them
}

//go:embed mappings.yaml
var embedded []byte

var reverseKinds = []string{"checkout", "cache", "upload", "download"}

var templateVar = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_-]+)\s*\}\}`)

type supportDoc struct {
	Level    string `yaml:"level"`
	Strategy string `yaml:"strategy"`
	Caveat   string `yaml:"caveat"`
}

type targetDoc struct {
	With     map[string]string `yaml:"with"`
	Defaults map[string]string `yaml:"defaults"`
	Support  supportDoc        `yaml:"support"`
	Kind     string            `yaml:"kind"`
	Image    string            `yaml:"image"`
	Script   []string          `yaml:"script"`
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
	out := Target{Kind: c.doc.Kind, Script: c.doc.Script, Support: c.support, With: map[string]string{}}
	if target == provider.GHA || c.doc.Kind == KindPassthrough {
		for k, v := range with {
			out.With[k] = v
		}
		return out
	}

	consumed := map[string]bool{}
	for in, dst := range c.doc.With {
		consumed[in] = true
		if v, ok := with[in]; ok {
			out.With[dst] = v
		}
	}
	if c.doc.Image != "" {
		out.Image, out.Support = renderImage(c, with, consumed)
	}
	for k := range with {
		if !consumed[k] {
			out.Unmapped = append(out.Unmapped, k)
		}
	}
	sort.Strings(out.Unmapped)
	return out
}

// renderImage substitutes inputs into the image template. A missing input
// downgrades to lost rather than yielding an image with an empty tag.
func renderImage(c compiled, with map[string]string, consumed map[string]bool) (string, caps.Support) {
	sup := c.support
	var problem string
	img := templateVar.ReplaceAllStringFunc(c.doc.Image, func(m string) string {
		name := templateVar.FindStringSubmatch(m)[1]
		consumed[name] = true
		consumed[name+"-file"] = true
		if v := with[name]; v != "" {
			return v
		}
		if v := c.doc.Defaults[name]; v != "" {
			return v
		}
		if problem == "" {
			if with[name+"-file"] != "" {
				problem = versionFileCaveat
			} else {
				problem = "missing input " + name
			}
		}
		return m
	})
	if problem != "" {
		sup.Level = caps.Lost
		sup.Caveat = problem
		return "", sup
	}
	return img, sup
}
