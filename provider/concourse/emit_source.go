package concourse

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	keySource      = "source"
	keyName        = "name"
	keyType        = "type"
	tagsName       = "source-tags"
	scheduleName   = "schedule"
	anyTag         = "*"
	timeSourceKeys = "interval start stop days"
	// uriNote tells the operator what the generated git resources need.
	uriNote = "the git resources read their uri from ((git-uri)); set it with fly set-pipeline -v git-uri=<uri> or in the credential manager"
	// firstVersionNote explains the time resource start-up gap.
	firstVersionNote = "a time resource has no version until its first window or interval passes, so a person who starts the job before then finds no input; run fly check-resource -r <pipeline>/%s --from time:2000-01-01T00:00:00Z once after set-pipeline (initial_version: true is not written because it starts the job at once, outside the window)"
)

// source is one resource of the pipeline file.
type source struct {
	def  map[string]any
	name string
	typ  string
	// trigger is true when the resource starts the jobs that have no needs.
	trigger bool
	used    bool
}

func (pe *pipeEmitter) add(s *source) *source {
	pe.sources = append(pe.sources, s)
	pe.byName[s.name] = s
	return s
}

func (pe *pipeEmitter) taken(name string) bool { _, ok := pe.byName[name]; return ok }

// newGit makes a fresh git resource and reports the uri variable once.
func (pe *pipeEmitter) newGit(name string) map[string]any {
	if !pe.uriNoted() {
		pe.e.emulate(pe.path, caps.ExtPassthrough, "git-uri variable", uriNote)
	}
	return map[string]any{keyName: name, keyType: typeGit, keySource: map[string]any{"uri": gitURI}}
}

func (pe *pipeEmitter) uriNoted() bool {
	_, ok := pe.e.said[use(pe.path, caps.ExtPassthrough)]
	return ok
}

// ownNames returns the resource names that a Concourse-origin trigger came from, when each has a definition.
func (pe *pipeEmitter) ownNames(t *ir.Trigger) []string {
	if !pe.e.own {
		return nil
	}
	l, _ := extOf(t.Ext)[ExtResourcesRef].([]any)
	var out []string
	for _, v := range l {
		n, _ := v.(string)
		if _, ok := pe.extRes[n]; !ok || pe.taken(n) {
			return nil
		}
		out = append(out, n)
	}
	return out
}

func isGlob(s string) bool { return strings.ContainsAny(s, globChars) }

// buildSources makes the git and time resources the triggers need. Every pipeline has a primary source: the
// first git resource, or a generated one when no trigger gives a git resource, so every job gets one source and
// a passed list can order jobs.
func (pe *pipeEmitter) buildSources() {
	for i := range pe.pl.Triggers {
		t := &pe.pl.Triggers[i]
		tp := fmt.Sprintf("%s/trigger/%d", pe.path, i)
		pe.e.foreignTrigger(tp, t, pe.e.own)
		switch t.Kind {
		case ir.TriggerPush:
			pe.push(t, tp)
		case ir.TriggerTag:
			pe.tag(t, tp)
		case ir.TriggerSchedule:
			pe.schedule(t, tp)
		case ir.TriggerPR:
			pe.e.lose(tp, caps.TriggerPullRequest, "")
		case ir.TriggerManual:
		default:
			pe.restore(t, tp)
		}
	}
	gits := pe.sourcesOf(typeGit)
	if len(gits) == 0 {
		name := unique(map[string]bool{}, primaryName)
		if pe.taken(name) {
			name = unique(pe.nameSet(), primaryName)
		}
		pe.add(&source{name: name, typ: typeGit, def: pe.newGit(name)})
		gits = pe.sourcesOf(typeGit)
	}
	pe.primary = gits[0]
	sort.SliceStable(pe.sources, pe.sourceLess)
}

// sourceLess orders the resources: those of a Concourse file keep the order they had, and the rest list git first.
func (pe *pipeEmitter) sourceLess(i, j int) bool {
	ra, rb := pe.sourceRank(pe.sources[i]), pe.sourceRank(pe.sources[j])
	if ra[0] != rb[0] {
		return ra[0] < rb[0]
	}
	return ra[1] < rb[1]
}

func (pe *pipeEmitter) sourceRank(s *source) [2]int {
	if idx, ok := pe.extOrder[s.name]; ok {
		return [2]int{0, idx}
	}
	if s.typ == typeGit {
		return [2]int{1, 0}
	}
	return [2]int{1, 1}
}

func (pe *pipeEmitter) nameSet() map[string]bool {
	m := map[string]bool{}
	for _, s := range pe.sources {
		m[s.name] = true
	}
	return m
}

func (pe *pipeEmitter) sourcesOf(typ string) []*source {
	var out []*source
	for _, s := range pe.sources {
		if s.typ == typ {
			out = append(out, s)
		}
	}
	return out
}

// foreignTrigger reports Ext data of other providers on a trigger.
func (e *emitter) foreignTrigger(path string, t *ir.Trigger, own bool) {
	if !own {
		e.foreignExt(path, t.Ext)
	}
}

// gitPaths sets the path filters of a git resource from an IR filter.
func gitPaths(src map[string]any, f ir.Filter) {
	delete(src, "paths")
	delete(src, "ignore_paths")
	if len(f.Include) > 0 {
		src["paths"] = toAny(f.Include)
	}
	if len(f.Exclude) > 0 {
		src["ignore_paths"] = toAny(f.Exclude)
	}
}

func toAny(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

func srcOf(def map[string]any) map[string]any {
	s, ok := def[keySource].(map[string]any)
	if !ok {
		s = map[string]any{}
		def[keySource] = s
	}
	return s
}

// push writes one git resource for each exact branch. A glob has no resource, because a git resource follows one branch.
func (pe *pipeEmitter) push(t *ir.Trigger, tp string) {
	e := pe.e
	var exact []string
	for _, b := range t.Branches.Include {
		if isGlob(b) || strings.Contains(b, varOpen) {
			e.lose(tp, caps.TriggerFilterBranch, "the branch pattern "+b+" has no resource: a git resource follows one exact branch")
			continue
		}
		exact = append(exact, b)
	}
	if len(t.Branches.Exclude) > 0 {
		e.lose(tp, caps.TriggerFilterBranch, "the branch exclude list "+strings.Join(t.Branches.Exclude, ", ")+" has no form: a git resource follows one exact branch")
	}
	if len(t.Tags.Include)+len(t.Tags.Exclude) > 0 {
		e.lose(tp, caps.TriggerFilterTags, "a push trigger with a tag filter has no form in one git resource")
	}
	if len(t.Branches.Include) == 0 {
		e.lose(tp, caps.TriggerFilterBranch, "the push trigger has no branch filter, and a git resource follows one branch; the resource follows the default branch of the repository")
		exact = []string{""}
	}
	if len(exact) == 0 {
		return
	}
	names := pe.ownNames(t)
	if len(names) != len(exact) {
		names = nil
	}
	for k, b := range exact {
		var name string
		var def map[string]any
		switch {
		case names != nil:
			name = names[k]
			def = pe.extDef(name)
		default:
			name = primaryName
			if len(exact) > 1 || pe.taken(name) {
				name = primaryName + "-" + sanitize(b)
			}
			name = unique(pe.nameSet(), name)
			def = pe.newGit(name)
		}
		src := srcOf(def)
		src["branch"] = b
		if b == "" {
			delete(src, "branch")
		}
		delete(src, "tag_filter")
		gitPaths(src, t.Paths)
		pe.add(&source{name: name, typ: typeGit, def: def, trigger: true})
	}
	if len(exact) > 1 {
		e.emulate(tp, caps.TriggerPush, "one git resource per branch",
			fmt.Sprintf("the branches %s are %d git resources, and the job runs once per branch event", strings.Join(exact, ", "), len(exact)))
		e.emulate(tp, caps.TriggerFilterBranch, "one git resource per branch", "the job runs once per branch event of the listed branches")
	}
}

// sanitize makes text a resource name part.
func sanitize(s string) string {
	id := adapter{}.SanitizeID(s)
	if id == "default" && s != "default" {
		return "any"
	}
	return id
}

// tag writes one git resource with a tag_filter for each include pattern.
func (pe *pipeEmitter) tag(t *ir.Trigger, tp string) {
	e := pe.e
	pats := t.Tags.Include
	if len(pats) == 0 {
		pats = []string{anyTag}
	}
	if len(t.Tags.Exclude) > 0 {
		e.lose(tp, caps.TriggerFilterTags, "the tag exclude list "+strings.Join(t.Tags.Exclude, ", ")+" has no form: a git resource takes one tag_filter pattern")
	}
	if len(t.Branches.Include)+len(t.Branches.Exclude) > 0 {
		e.lose(tp, caps.TriggerTag, "the tag trigger has a branch filter, which one git resource cannot hold together with tag_filter; the filter is dropped")
	}
	names := pe.ownNames(t)
	if len(names) != len(pats) {
		names = nil
	}
	for k, pat := range pats {
		var name string
		var def map[string]any
		if names != nil {
			name = names[k]
			def = pe.extDef(name)
		} else {
			name = unique(pe.nameSet(), tagsName)
			def = pe.newGit(name)
		}
		src := srcOf(def)
		src["tag_filter"] = pat
		delete(src, "branch")
		gitPaths(src, t.Paths)
		pe.add(&source{name: name, typ: typeGit, def: def, trigger: true})
	}
	if len(pats) > 1 {
		e.emulate(tp, caps.TriggerFilterTags, "one git resource per tag pattern", "each of the patterns "+strings.Join(pats, ", ")+" is its own git resource")
	}
}

// schedule writes a time resource for the cron lines that scheduleSource maps; any other line is lost with its text.
func (pe *pipeEmitter) schedule(t *ir.Trigger, tp string) {
	e := pe.e
	want, ok := scheduleSource(t.Cron)
	if !ok {
		e.lose(tp, caps.TriggerSchedule, "the cron line "+t.Cron+" has no time resource form; the cron text is kept in this comment and the trigger is not written")
		e.comment(tp, "cron "+t.Cron)
		return
	}
	var name string
	var def map[string]any
	if own := pe.ownNames(t); len(own) == 1 {
		name = own[0]
		def = pe.extDef(name)
		if typ, _ := def[keyType].(string); typ != typeTime {
			def = nil
		}
	}
	if def == nil {
		name = unique(pe.nameSet(), scheduleName)
		def = map[string]any{keyName: name, keyType: typeTime, keySource: map[string]any{}}
	}
	if own := extOf(t.Ext)[ExtCron]; own == t.Cron && def != nil && pe.e.own && pe.extDef(name) != nil {
		pe.add(&source{name: name, typ: typeTime, def: def, trigger: true})
		return
	}
	src := srcOf(def)
	for _, k := range strings.Fields(timeSourceKeys) {
		delete(src, k)
	}
	for k, v := range want {
		if l, isList := v.([]string); isList {
			v = toAny(l)
		}
		src[k] = v
	}
	e.emulate(tp, caps.TriggerSchedule, "time resource", fmt.Sprintf(firstVersionNote, name))
	e.markArg(caps.TriggerSchedule, tp, scheduleArg{Cron: t.Cron, Resource: name, Source: canonJSON(def[keySource])})
	pe.add(&source{name: name, typ: typeTime, def: def, trigger: true})
}

// restore writes the resources of a Concourse-origin trigger that has no IR form, with the source definition
// unchanged, and keeps their triggering gets. A trigger of another origin is lost.
func (pe *pipeEmitter) restore(t *ir.Trigger, tp string) {
	e := pe.e
	f := caps.TriggerResource
	if t.Kind == ir.TriggerAPI {
		f = caps.TriggerAPI
	}
	names := pe.ownNames(t)
	if len(names) == 0 {
		e.lose(tp, f, "")
		return
	}
	for _, n := range names {
		def := pe.extDef(n)
		typ, _ := def[keyType].(string)
		pe.add(&source{name: n, typ: typ, def: def, trigger: true})
	}
	e.lose(tp, f, "the resources "+strings.Join(names, ", ")+" are written as the Concourse file held them, with their triggering gets")
}

// resourceNodes writes the resources that a job uses; an unused resource is dropped, because fly rejects it.
func (pe *pipeEmitter) resourceNodes() *yaml.Node {
	var out []*yaml.Node
	sort.SliceStable(pe.sources, pe.sourceLess)
	for _, s := range pe.sources {
		if !s.used {
			pe.e.lose(pe.path, caps.ExtPassthrough, "the resource "+s.name+" is used by no job, and fly rejects an unused resource; it is not written")
			continue
		}
		out = append(out, orderedNode(s.def, []string{keyName, keyType, keySource}, sourceOrder))
	}
	return seqOf(out...)
}

var sourceOrder = []string{"uri", "branch", "tag_filter", "paths", "ignore_paths", "interval", "start", "stop", "days"}

// orderedNode writes a mapping with the keys in first before the others, which come sorted. The nested source
// map uses the order of nested.
func orderedNode(m map[string]any, first, nested []string) *yaml.Node {
	out := newMap()
	set := func(k string) {
		v, ok := m[k]
		if !ok {
			return
		}
		if k == keySource {
			if sm, isMap := v.(map[string]any); isMap {
				out.Set(k, orderedNode(sm, nested, nil))
				return
			}
		}
		n, err := plainNode(v)
		if err == nil {
			out.Set(k, n)
		}
	}
	for _, k := range first {
		set(k)
	}
	for _, k := range sortedKeys(m) {
		if !slices.Contains(first, k) {
			set(k)
		}
	}
	return out.N
}
