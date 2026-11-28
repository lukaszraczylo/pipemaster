// Package vars translates the predefined variables that scripts read, such as $CI_COMMIT_SHA, between CI
// providers. One embedded table (vars.yaml) holds every spelling.
package vars

import (
	_ "embed"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	"github.com/lukaszraczylo/pipemaster/provider"
)

//go:embed vars.yaml
var tableYAML []byte

const defaultKey = "default"

// names is one spelling list; the first name is the one the prelude reads.
type names []string

func (n *names) UnmarshalYAML(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode {
		*n = names{v.Value}
		return nil
	}
	return v.Decode((*[]string)(n))
}

type entry struct {
	Names   map[string]names  `yaml:"names"`
	Derive  map[string]string `yaml:"derive"`
	Literal map[string]string `yaml:"literal"`
	Field   map[string]string `yaml:"field"`
	Canon   string            `yaml:"canon"`
	// PushCanon is the canonical ref that has the entry's meaning when the pipeline runs on push events only.
	PushCanon string `yaml:"push_canon"`
	Values    string `yaml:"values"`
	// FieldEvents lists, by target family or "default", the trigger kinds on which the field spellings of the entry
	// hold. No entry means every kind.
	FieldEvents map[string][]string `yaml:"field_events"`
	// LostOnPR lists the providers whose value differs on a pull request from every other provider.
	LostOnPR []string `yaml:"lost_on_pr"`
	// ScriptText keeps the name as text in a script, so the prelude spells it, because the canonical ref
	// has a different meaning on some event.
	ScriptText bool `yaml:"script_text"`
}

type rawTable struct {
	Docs     map[string]string           `yaml:"docs"`
	Vars     map[string]entry            `yaml:"vars"`
	Events   map[string]map[string]names `yaml:"events"`
	Known    map[string][]string         `yaml:"known"`
	Prefixes map[string][]string         `yaml:"prefixes"`
	Skip     []string                    `yaml:"skip"`
}

// Table is the parsed mapping table.
type Table struct {
	raw    rawTable
	owner  map[string]owner
	alt    map[string][]owner
	skip   map[string]bool
	known  map[string]map[string]bool
	sorted []string
}

type owner struct {
	id       string
	provider string
}

var (
	loaded    *Table
	loadErr   error
	loadOnce  sync.Once
	templated = map[string]bool{provider.Tekton: true, provider.Argo: true}
)

// Load returns the embedded table.
func Load() (*Table, error) {
	loadOnce.Do(func() { loaded, loadErr = parse(tableYAML) })
	return loaded, loadErr
}

func parse(data []byte) (*Table, error) {
	t := &Table{owner: map[string]owner{}, alt: map[string][]owner{}, skip: map[string]bool{}, known: map[string]map[string]bool{}}
	if err := yaml.Unmarshal(data, &t.raw); err != nil {
		return nil, fmt.Errorf("vars table: %w", err)
	}
	ids := make([]string, 0, len(t.raw.Vars))
	for id := range t.raw.Vars {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		e := t.raw.Vars[id]
		for prov, ns := range e.Names {
			for _, n := range ns {
				o := owner{id: id, provider: prov}
				// One name can mean different things on two providers (CI_COMMIT_BRANCH), so each meaning
				// is an entry of its own and the origin of the project picks one.
				if prev, dup := t.owner[n]; dup && prev.id != id {
					if prev.provider == prov {
						return nil, fmt.Errorf("vars table: %s is listed for %s and %s on %s", n, prev.id, id, prov)
					}
					t.alt[n] = append(t.alt[n], o)
					continue
				}
				t.owner[n] = o
			}
		}
		if e.Values != "" && t.raw.Events == nil {
			return nil, fmt.Errorf("vars table: %s names events but the table has none", id)
		}
	}
	for _, n := range t.raw.Skip {
		t.skip[n] = true
	}
	for prov, l := range t.raw.Known {
		t.known[prov] = map[string]bool{}
		for _, n := range l {
			t.known[prov][n] = true
		}
	}
	for id := range t.raw.Vars {
		t.sorted = append(t.sorted, id)
	}
	sort.Strings(t.sorted)
	return t, nil
}

// Docs returns the documentation URL of each provider that the table cites.
func (t *Table) Docs() map[string]string { return t.raw.Docs }

// IDs lists the entry ids in order.
func (t *Table) IDs() []string { return append([]string(nil), t.sorted...) }

// Names lists the spellings of entry id on provider id; nil when the provider sets none.
func (t *Table) Names(id, prov string) []string { return t.raw.Vars[id].Names[provider.Family(prov)] }

// Native reports whether name is set by target for a project from origin: the entry that gives name its meaning
// there is spelled so on target, or the provider documents the name. A same-spelled name of another entry does
// not count, because it has another meaning.
func (t *Table) Native(name, origin, target string) bool {
	target = provider.Family(target)
	if _, ok := t.owner[name]; ok {
		o, _ := t.ownerFor(name, origin)
		return slices.Contains(t.raw.Vars[o.id].Names[target], name)
	}
	return t.known[target][name]
}

// ownerFor picks the entry that gives name its meaning for a project from origin.
func (t *Table) ownerFor(name, origin string) (owner, bool) {
	o, ok := t.owner[name]
	if !ok {
		return o, false
	}
	origin = provider.Family(origin)
	for _, c := range append([]owner{o}, t.alt[name]...) {
		if c.provider == origin {
			return c, true
		}
	}
	return o, true
}

// foreign reports whether name is a predefined variable of another provider that target does not set. A
// name that only shares a prefix with target stays the target's own, because no list of names is complete.
func (t *Table) foreign(name, origin, target string) bool {
	target = provider.Family(target)
	if t.skip[name] || t.Native(name, origin, target) {
		return false
	}
	if _, ok := t.owner[name]; ok {
		return true
	}
	for prov, k := range t.known {
		if prov != target && k[name] {
			return true
		}
	}
	own := t.raw.Prefixes[target]
	for prov, ps := range t.raw.Prefixes {
		if prov == target {
			continue
		}
		for _, p := range ps {
			if strings.HasPrefix(name, p) && !slices.Contains(own, p) {
				return true
			}
		}
	}
	return false
}

// spell gives the shell text that holds entry id on target, or false when the target has no source.
// depth stops a derive cycle.
func (t *Table) spell(id, target string, depth int) (string, bool) {
	target = provider.Family(target)
	e, ok := t.raw.Vars[id]
	if !ok || depth > 3 {
		return "", false
	}
	if ns := e.Names[target]; len(ns) > 0 {
		return "${" + ns[0] + "}", true
	}
	if templated[target] && e.Canon != "" {
		if _, ok := ctxparam.Param(strings.Split(e.Canon, ".")); ok {
			return "${{ " + e.Canon + " }}", true
		}
	}
	d, ok := e.Derive[target]
	if !ok {
		d, ok = e.Derive[defaultKey]
	}
	if ok {
		return t.expand(d, target, depth)
	}
	if v, ok := e.Literal[target]; ok {
		return v, true
	}
	return "", false
}

// expand replaces each @id@ of a derive text with the value of that entry on target.
func (t *Table) expand(d, target string, depth int) (string, bool) {
	var b strings.Builder
	for {
		i := strings.IndexByte(d, '@')
		if i < 0 {
			b.WriteString(d)
			return b.String(), true
		}
		j := strings.IndexByte(d[i+1:], '@')
		if j < 0 {
			return "", false
		}
		b.WriteString(d[:i])
		v, ok := t.spell(d[i+1:i+1+j], target, depth+1)
		if !ok {
			return "", false
		}
		b.WriteString(v)
		d = d[i+j+2:]
	}
}

// Canon returns the canonical expr ref of entry id, or "" when it has none.
func (t *Table) Canon(id string) string { return t.raw.Vars[id].Canon }

// Derives reports whether entry id can be computed on target without a name of its own.
func (t *Table) Derives(id, target string) bool {
	_, ok := t.spell(id, target, 0)
	return ok
}

// ScriptText reports whether script text keeps name as a shell variable instead of a canonical ref.
func ScriptText(name string) bool {
	t, err := Load()
	if err != nil {
		return false
	}
	o, ok := t.owner[name]
	return ok && t.raw.Vars[o.id].ScriptText
}

// ScriptRefLost reports whether the canonical ref path has no exact spelling in a script on target, because the
// ref means something else on some event there. origin is the provider that the project came from.
func ScriptRefLost(path []string, origin, target string) bool {
	t, err := Load()
	if err != nil || provider.Family(origin) == provider.Family(target) {
		return false
	}
	canon := strings.Join(path, ".")
	if canon == "tag" {
		return false
	}
	for _, e := range t.raw.Vars {
		if e.ScriptText && e.Canon == canon {
			return true
		}
	}
	return false
}
