package tekton

import (
	"go.yaml.in/yaml/v3"
	"slices"

	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

// Output paths. The parser reads every YAML file under .tekton/, so the split is for people and for kustomize.
const (
	pipelineFile  = dirName + "/pipeline.yaml"
	tasksDir      = dirName + "/tasks/"
	triggersDir   = dirName + "/triggers/"
	cronDir       = dirName + "/cron/"
	rbacFile      = dirName + "/rbac/triggers.yaml"
	resourcesDir  = dirName + "/resources/"
	kustomizeFile = dirName + "/kustomization.yaml"

	apiPipelinesV1 = "tekton.dev/v1"
	apiTriggersV1  = "triggers.tekton.dev/v1beta1"
	apiBatchV1     = "batch/v1"

	// defaultImage runs a step that names no image, because Tekton requires one.
	defaultImage = "alpine:3.22"
	// cronNameMax is the longest CronJob name the API server accepts.
	cronNameMax = 52
)

// Emit writes the project as Tekton files under .tekton/. Every feature use goes through the ledger; Finish
// fails the call when one was never accounted for.
func (a adapter) Emit(p *ir.Project) (map[string][]byte, *report.Report, error) {
	origin := ""
	if p != nil {
		origin = p.Origin.Provider
	}
	e := &emitter{
		led: emitkit.NewLedger("ir", dialect, a.Caps()), origin: origin, own: origin == dialect,
		files: map[string][]*outDoc{}, owners: map[string]*outDoc{}, marked: map[ir.FeatureUse]bool{},
		taken: map[string]bool{}, made: map[string]bool{}, skipped: map[string]bool{},
	}
	uses := ir.Features(p)
	for _, u := range uses {
		e.led.Decide(u)
	}
	if err := e.project(livePipelines(p)); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", dialect, err)
	}
	rep, err := e.led.Finish(uses)
	if err != nil {
		return nil, rep, fmt.Errorf("%s: %w", dialect, err)
	}
	rep.Entries = slices.DeleteFunc(rep.Entries, func(en report.Entry) bool {
		return en.Feature == caps.StepAction && e.skipped[en.IRPath]
	})
	rep.Withdrawn = e.withdrawn
	e.markEmulations(rep)
	files, err := e.render()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", dialect, err)
	}
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
	led    *emitkit.Ledger
	said   map[ir.FeatureUse][2]string
	files  map[string][]*outDoc
	owners map[string]*outDoc
	marked map[ir.FeatureUse]bool
	// taken holds the Task names in use; made holds "kind name" of every generated object, so a resource kept in
	// Ext is not written a second time.
	taken map[string]bool
	made  map[string]bool
	// pendingEL holds the EventListeners that a Tekton file kept as resources, by name.
	pendingEL map[string][]resourceDoc
	// lostWork holds the path of every step that lost script, action or image work.
	lostWork []string
	origin   string
	// keptEntries holds the names of EventListener entries that a Tekton file kept, so a generated one is not written twice.
	keptEntries map[string]bool
	plans       []*plan
	// skipped holds the paths of the parser-added checkout steps that the file does not write.
	skipped map[string]bool
	// withdrawn holds the parser entries for the checkout steps that the file does not write.
	withdrawn []report.Entry
	// listeners are the EventListeners in order of first use.
	listeners []*listener
	// own is true when the project came from Tekton, so its Ext keys are ours to write back.
	own               bool
	generatedListener bool
}

func use(path string, f caps.Feature) ir.FeatureUse { return ir.FeatureUse{IRPath: path, Feature: f} }

func (e *emitter) lose(path string, f caps.Feature, detail string) {
	if strings.Contains(path, "/step/") && (f == caps.StepScript || f == caps.StepAction || f == caps.StepImage) {
		e.lostWork = append(e.lostWork, path)
	}
	e.note(path, f, caps.Lost, "", detail)
}

// lostStepWork is true when a step of the job at jp lost its work.
func (e *emitter) lostStepWork(jp string) bool {
	return slices.ContainsFunc(e.lostWork, func(p string) bool { return strings.HasPrefix(p, jp+"/step/") })
}

func (e *emitter) noteLost(path string, f caps.Feature, detail string) {
	e.note(path, f, caps.Lost, "", detail)
}

func (e *emitter) emulate(path string, f caps.Feature, strategy, detail string) {
	e.note(path, f, caps.Emulated, strategy, detail)
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

// foreignExt reports Ext data of other providers: it is not in the Tekton files, only in the round-trip metadata.
func (e *emitter) foreignExt(path string, x ir.Ext) {
	emitkit.ForeignExt(e.led, dialect, "Tekton", path, x)
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

// render marshals every file and adds the kustomization that lists them.
func (e *emitter) render() (map[string][]byte, error) {
	out := map[string][]byte{}
	for name, docs := range e.files {
		b, err := marshalDocs(docs)
		if err != nil {
			return nil, err
		}
		out[name] = b
	}
	if len(out) == 0 {
		return out, nil
	}
	var res []string
	for name := range out {
		res = append(res, strings.TrimPrefix(name, dirName+"/"))
	}
	sort.Strings(res)
	k := newMap()
	k.SetStr("apiVersion", apiKustomize)
	k.SetStr("kind", kindKustomization)
	k.Set("resources", strSeq(res))
	b, err := marshalDocs([]*outDoc{{Root: k.N}})
	if err != nil {
		return nil, err
	}
	out[kustomizeFile] = b
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
	want := adapter{}.SanitizeID(id)
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

const (
	kustomizeAPI       = "kustomize.config.k8s.io/"
	apiKustomize       = kustomizeAPI + "v1beta1"
	kindKustomization  = "Kustomization"
	idMarkerPrefix     = "# " + idPrefix
	workspaceName      = "pipemaster-workspace"
	workspaceMount     = "/pipemaster"
	artifactsDirectory = workspaceMount + "/artifacts"
)

// idComment is the head comment that carries the IR ID of a node whose written name differs.
func idComment(id string) string { return idMarkerPrefix + commentSafe(id) }

// node encodes an Ext value; a value that cannot be written is reported lost and gives nil.
func (e *emitter) node(path, what string, v any) *yaml.Node {
	n, err := plainNode(v)
	if err != nil {
		e.lose(path, caps.ExtPassthrough, what+" of the Tekton file cannot be written: "+err.Error())
		return nil
	}
	return n
}

// withdrawCheckout retracts the parser's entry that says the job got its own checkout step.
func (e *emitter) withdrawCheckout(jobPath string) {
	e.withdrawn = append(e.withdrawn, report.Entry{IRPath: jobPath, Feature: caps.StepAction, Strategy: checkoutStrategy})
}
