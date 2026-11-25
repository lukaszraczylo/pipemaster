package concourse

import (
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
)

// topExt lists the pipeline Ext keys that are top-level keys of the file, in the order they are written.
var topExt = []string{ExtResourceTypes, ExtVarSources, ExtGroups, ExtDisplay, ExtUserData, ExtPrototypes}

// pipeEmitter is the state of one pipeline file.
type pipeEmitter struct {
	ext      map[string]any
	pl       *ir.Pipeline
	doc      *outDoc
	primary  *source
	extRes   map[string]map[string]any
	extOrder map[string]int
	byName   map[string]*source
	jobName  map[string]string
	e        *emitter
	mpath    string
	path     string
	file     string
	sources  []*source
	sinks    []*ir.Job
	tagSplit bool
	serial   bool
}

// pipeline writes one IR pipeline as one file.
func (e *emitter) pipeline(pl *ir.Pipeline, first bool) {
	pp := "pipeline/" + pl.ID
	empty := len(pl.Jobs) == 0
	if empty {
		held := *pl
		held.Jobs = []*ir.Job{{ID: placeholderJob, Steps: []*ir.Step{{ID: placeholderJob, Kind: ir.StepScript, Script: []string{placeholderCmd}}}}}
		pl = &held
	}
	pe := &pipeEmitter{
		e: e, pl: pl, path: pp, doc: &outDoc{}, byName: map[string]*source{}, jobName: map[string]string{},
		extRes:   map[string]map[string]any{},
		extOrder: map[string]int{},
	}
	if e.own {
		pe.ext = extOf(pl.Ext)
	}
	e.owners[pp] = pe.doc
	pe.file = e.fileName(pl.ID, pp)
	pe.mpath = "pipeline/" + strings.TrimSuffix(strings.TrimPrefix(pe.file, dirName+"/"), ".yml")
	e.owners[pe.mpath] = pe.doc
	if base := strings.TrimPrefix(pe.mpath, "pipeline/"); base != pl.ID && pl.Name == "" {
		e.markArg(caps.DisplayName, pe.mpath, nameArg{ID: pl.ID, Written: base, Kind: "pipeline"})
	}
	if !e.own {
		e.foreignExt(pp, pl.Ext)
	}
	pe.loadExt(first)
	pe.pipelineMeta()
	pe.buildSources()
	jobs := pe.jobs()
	if empty {
		e.emulate(pp, caps.ExtPassthrough, "placeholder job", "the pipeline has no jobs, and a Concourse pipeline needs one; the job "+placeholderJob+" runs "+placeholderCmd)
		e.markArg(caps.ExtPassthrough, pe.mpath+"/placeholder", placeholderArg{Hold: nodeCanon(jobs[0])})
	}
	top := newMap()
	if n := pe.extNode(ExtResourceTypes); n != nil {
		top.Set(ExtResourceTypes, n)
	}
	top.Set("resources", pe.resourceNodes())
	top.Set("jobs", seqOf(jobs...))
	for _, k := range topExt[1:] {
		if n := pe.extNode(k); n != nil {
			top.Set(k, n)
		}
	}
	pe.unwrittenKeys(pp)
	pe.carryPipeline(top.N)
	pe.doc.Root = top.N
	e.files = append(e.files, &outFile{name: pe.file, doc: pe.doc})
}

// fileName is the file of a pipeline. The parser reads the pipeline ID from the base name, so a name that
// holds a path separator or a space is changed and the marker brings the ID back.
func (e *emitter) fileName(id, pp string) string {
	base := id
	if strings.ContainsAny(base, `/\ `) || base == "" {
		base = strings.Map(func(r rune) rune {
			if r == '/' || r == '\\' || r == ' ' {
				return '-'
			}
			return r
		}, base)
	}
	if base == "" {
		base = "pipeline"
	}
	name := unique(e.fileTaken, base)
	if name != id {
		e.emulate(pp, caps.ExtPassthrough, "file name changed", "the pipeline ID "+id+" is not a plain file name; the file is "+name+".yml")
	}
	return dirName + "/" + name + ".yml"
}

// loadExt reads the verbatim resource definitions of a Concourse-origin pipeline and the extra files.
func (pe *pipeEmitter) loadExt(first bool) {
	if pe.ext == nil {
		return
	}
	list, _ := pe.ext[ExtResources].([]any)
	for _, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := m["name"].(string); name != "" {
			if _, dup := pe.extRes[name]; !dup {
				pe.extRes[name] = m
				pe.extOrder[name] = len(pe.extOrder)
			}
		}
	}
	if files, ok := pe.ext[ExtFiles].(map[string]any); ok && first {
		pe.e.extra = map[string]any{}
		for n, v := range files {
			pe.e.extra[n] = v
		}
	}
}

// extNode writes a top-level Ext key of a Concourse-origin pipeline.
func (pe *pipeEmitter) extNode(key string) *yaml.Node {
	v, ok := pe.ext[key]
	if !ok {
		return nil
	}
	return pe.e.node(pe.path, "the top-level key "+key, v)
}

// pipelineMeta writes the markers for the pipeline name and the concurrency flag.
func (pe *pipeEmitter) pipelineMeta() {
	pl, e := pe.pl, pe.e
	if pl.Name != "" {
		e.markArg(caps.DisplayName, pe.mpath, nameArg{Name: pl.Name, ID: pl.ID, Written: strings.TrimPrefix(pe.mpath, "pipeline/"), Kind: "pipeline"})
	}
	if pl.Permissions != nil {
		e.lose(pe.path, caps.PipelinePermissions, "")
	}
	if len(pl.Inputs) > 0 {
		e.lose(pe.path, caps.PipelineInputs, "")
	}
	if c := pl.Concurrency; c != nil && c.CancelInProgress {
		e.lose(pe.path, caps.PipelineConcurrency, "cancel-in-progress is lost; a second build of a job in the group waits for the first")
	}
}

// deepCopy copies a decoded YAML value.
func deepCopy(v any) any { return ynode.DeepCopy(v) }

// extDef returns a copy of the verbatim definition of a resource, or nil.
func (pe *pipeEmitter) extDef(name string) map[string]any {
	if m, ok := pe.extRes[name]; ok {
		c, _ := deepCopy(m).(map[string]any)
		return c
	}
	return nil
}

// unwrittenKeys reports the unknown top-level keys of a Concourse-origin pipeline. Concourse rejects a key it does
// not know, so none is written. The IR keeps them, and the metadata returns them.
func (pe *pipeEmitter) unwrittenKeys(pp string) {
	extra, _ := pe.ext[ExtExtra].(map[string]any)
	holders := map[string]bool{}
	list, _ := pe.ext[ExtAnchorKeys].([]any)
	for _, k := range list {
		holders[strOf(k)] = true
	}
	for _, k := range sortedKeys(extra) {
		why := "the name is unknown to Concourse"
		if holders[k] {
			why = "it only holds YAML anchors, which the parser has resolved"
		}
		pe.e.lose(pp, caps.ExtPassthrough, "top-level key "+k+" is no Concourse key and is not written; "+why)
	}
}
