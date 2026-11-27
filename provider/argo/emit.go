package argo

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/provider/inline"
	"github.com/lukaszraczylo/pipemaster/provider/internal/dnsid"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/vars"
)

// Output paths. The parser reads every YAML file under .argo/, so the split is for people.
const (
	workflowsDir = dirName + "/workflows/"
	cronDir      = dirName + "/cron/"
	eventsDir    = dirName + "/events/"
	rbacDir      = dirName + "/rbac/"
	resourcesDir = dirName + "/resources/"

	apiVersion = apiArgo + "v1alpha1"
	maxName    = dnsid.MaxName

	// defaultImage runs a step that names no image, because a container needs one.
	defaultImage = "alpine:3.22"

	secretStore = "ci-secrets"
	configStore = "ci-vars"
)

var fit = dnsid.Fit

// Emit writes the project as Argo files under .argo/. Every feature use goes through the ledger; Finish fails
// the call when one was never accounted for.
func (a adapter) Emit(p *ir.Project) (map[string][]byte, *report.Report, error) {
	return inline.Emit(p, a.ID(), a.emitFlat)
}

func (a adapter) emitFlat(p *ir.Project) (map[string][]byte, *report.Report, error) {
	p, vres := vars.Prepare(p, dialect)
	p, checkouts := emitkit.WithSourceCheckout(p)
	p, noForm, injected := provider.WithDefaultTriggers(p, dialect)
	origin := ""
	if p != nil {
		origin = p.Origin.Provider
	}
	e := &emitter{
		led: emitkit.NewLedger("ir", dialect, a.Caps()), origin: origin, own: origin == dialect,
		files: map[string][]*outDoc{}, owners: map[string]*outDoc{}, marked: map[ir.FeatureUse]bool{},
		taken: map[string]bool{}, made: map[string]bool{}, injected: injected,
	}
	emitkit.RetainSourceCheckout(e.led, checkouts)
	e.gated = emitkit.GatedSteps(p)
	uses := ir.Features(p)
	for _, u := range uses {
		e.led.Decide(u)
	}
	e.noteDefaultEvents(noForm)
	if err := e.project(livePipelines(p)); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", dialect, err)
	}
	rep, err := e.led.Finish(uses)
	if err != nil {
		return nil, rep, fmt.Errorf("%s: %w", dialect, err)
	}
	e.markEmulations(rep)
	files, err := e.render()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", dialect, err)
	}
	vres.Report(rep)
	return files, rep, nil
}

func livePipelines(p *ir.Project) []*ir.Pipeline {
	if p == nil {
		return nil
	}
	var out []*ir.Pipeline
	for _, pl := range p.Pipelines {
		if pl != nil {
			out = append(out, pl)
		}
	}
	return out
}

// emitter carries the state of one Emit call.
type emitter struct {
	// gated holds the action steps that a later step reads, which no target may drop.
	gated  map[*ir.Step]bool
	led    *emitkit.Ledger
	said   map[ir.FeatureUse][2]string
	files  map[string][]*outDoc
	owners map[string]*outDoc
	marked map[ir.FeatureUse]bool
	taken  map[string]bool
	made   map[string]bool
	// injected names the pipelines whose triggers were made from the default events of their source.
	injected map[string]bool
	events   *eventDocs
	origin   string
	plans    []*plan
	own      bool
	needsSA  bool
	// nlost counts the Lost entries, so a caller can tell whether a step lost something.
	nlost int
}

func use(path string, f caps.Feature) ir.FeatureUse { return ir.FeatureUse{IRPath: path, Feature: f} }

func (e *emitter) lose(path string, f caps.Feature, detail string) {
	e.nlost++
	e.note(path, f, caps.Lost, "", detail)
}

func (e *emitter) emulate(path string, f caps.Feature, strategy, detail string) {
	e.note(path, f, caps.Emulated, strategy, detail)
}

func (e *emitter) external(path string, f caps.Feature, strategy, detail string) {
	e.note(path, f, caps.External, strategy, detail)
}

func (e *emitter) note(path string, f caps.Feature, lv caps.Level, strategy, detail string) {
	u := use(path, f)
	if prev, ok := e.said[u]; ok {
		strategy, detail = joinNew(prev[0], strategy), joinNew(prev[1], detail)
	}
	if e.said == nil {
		e.said = map[ir.FeatureUse][2]string{}
	}
	e.said[u] = [2]string{strategy, detail}
	if lv == caps.Lost {
		strategy = ""
	}
	e.led.Note(u, caps.Support{Level: lv, Strategy: strategy}, detail)
}

func joinNew(prev, next string) string {
	if next == "" || strings.Contains(prev, next) {
		return prev
	}
	if prev == "" {
		return next
	}
	return prev + "; " + next
}

// foreignExt reports Ext data of other providers: it is not in the Argo files, only in the round-trip metadata.
func (e *emitter) foreignExt(path string, x ir.Ext) {
	emitkit.ForeignExt(e.led, dialect, "Argo", path, x)
}

// add appends a document to a file and registers it as the owner of the markers of the IR paths.
func (e *emitter) add(file string, d *outDoc, paths ...string) *outDoc {
	e.files[file] = append(e.files[file], d)
	for _, p := range paths {
		e.owners[p] = d
	}
	return d
}

// ownerOf finds the document that holds the markers of an IR path: the longest registered prefix.
func (e *emitter) ownerOf(path string) *outDoc {
	for p := path; p != ""; {
		if d, ok := e.owners[p]; ok {
			return d
		}
		i := strings.LastIndex(p, "/")
		if i < 0 {
			break
		}
		p = p[:i]
	}
	if len(e.plans) > 0 {
		return e.plans[0].doc
	}
	return nil
}

// mark writes the marker line of an emulated construct into the head of the document that owns the path.
func (e *emitter) mark(f caps.Feature, path string) {
	if d := e.ownerOf(path); d != nil {
		d.Head = append(d.Head, emitkit.Marker(f, path))
		e.marked[use(path, f)] = true
	}
}

// markArg writes a marker and one JSON argument line.
func (e *emitter) markArg(f caps.Feature, path string, v any) error {
	line, err := emitkit.ArgLine(v)
	if err != nil {
		return err
	}
	e.mark(f, path)
	if d := e.ownerOf(path); d != nil {
		d.Head = append(d.Head, line)
	}
	return nil
}

// comment queues a lost-text line for the document that owns the path.
func (e *emitter) comment(path, s string) {
	if d := e.ownerOf(path); d != nil {
		d.Head = append(d.Head, "# pipemaster:lost "+commentSafe(s))
	}
}

// markEmulations writes a bare marker for every emulated or external use that has none, so the parser can see
// what the file stands in for.
func (e *emitter) markEmulations(rep *report.Report) {
	for _, en := range rep.Entries {
		if en.Level != caps.Emulated && en.Level != caps.External {
			continue
		}
		if u := use(en.IRPath, en.Feature); !e.marked[u] && e.ownerOf(en.IRPath) != nil {
			e.mark(en.Feature, en.IRPath)
		}
	}
}

// render marshals every file.
func (e *emitter) render() (map[string][]byte, error) {
	out := map[string][]byte{}
	for name, docs := range e.files {
		b, err := marshalDocs(docs)
		if err != nil {
			return nil, err
		}
		out[name] = b
	}
	return out, nil
}

// unique returns base, or base with a numeric suffix when taken, and records the result.
func unique(taken map[string]bool, base string, limit int) string {
	name := base
	for i := 2; taken[name]; i++ {
		suffix := "-" + strconv.Itoa(i)
		name = fit(base, base, limit-len(suffix)) + suffix
	}
	taken[name] = true
	return name
}

// dnsName makes id a DNS-1123 label that no other name in taken holds. It reports a collision, and a rename when loud.
func (e *emitter) dnsName(taken map[string]bool, path, what, id string, limit int, loud bool) string {
	want := dnsid.Sanitize(id)
	if len(want) > limit {
		want = fit(want, id, limit)
	}
	name := unique(taken, want, limit)
	switch {
	case name != want:
		e.emulate(path, caps.ExtPassthrough, what+" renamed", what+" "+id+" collides with another name after DNS-1123 conversion; written as "+name)
	case name != id && loud:
		e.emulate(path, caps.ExtPassthrough, what+" renamed", what+" "+id+" is not a DNS-1123 label; written as "+name)
	}
	return name
}

// node encodes an Ext value; a value that cannot be written is reported lost and gives nil.
func (e *emitter) node(path, what string, v any) *yaml.Node {
	n, err := plainNode(v)
	if err != nil {
		e.lose(path, caps.ExtPassthrough, what+" of the Argo file cannot be written: "+err.Error())
		return nil
	}
	return n
}

func sortedPaths(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// noteDefaultEvents reports the default events of the source that have no IR trigger.
func (e *emitter) noteDefaultEvents(noForm map[string][]provider.SourceEvent) {
	for id, events := range noForm {
		for _, g := range provider.GroupByFeature(events) {
			e.lose("pipeline/"+id, g.Feature, provider.NoFormDetail(g.Names, "Argo"))
		}
	}
}
