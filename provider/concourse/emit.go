package concourse

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ymap"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	gitURI          = "((git-uri))"
	defaultImage    = "ubuntu"
	defaultTag      = "latest"
	shellCommand    = "sh"
	defaultFlags    = "-ec"
	workspaceName   = "workspace"
	primaryName     = "source"
	defaultPlatform = "linux"
	placeholderCmd  = "true"
	disabledScript  = "exit 0 " + disabledTag
	disabledTag     = "# pipemaster:guard the step condition has no shell form"
	lostPrefix      = "# pipemaster:lost "
	markerIgnored   = "marker ignored: "
)

type (
	wmap   = ymap.Map
	outDoc = ymap.Doc
)

var (
	newMap    = ymap.New
	str       = ymap.Str
	boolNode  = ymap.Bool
	intNode   = ymap.Int
	seqOf     = ymap.Seq
	strSeq    = ymap.StrSeq
	block     = ymap.Block
	plainNode = ymap.PlainNode
)

// Emit writes the project as Concourse pipeline files under .concourse/, one per IR pipeline. Every feature use
// goes through the ledger; Finish fails the call when one was never accounted for.
func (a adapter) Emit(p *ir.Project) (map[string][]byte, *report.Report, error) {
	origin := ""
	if p != nil {
		origin = p.Origin.Provider
	}
	e := &emitter{
		led: emitkit.NewLedger("ir", dialect, a.Caps()), origin: origin, own: origin == dialect,
		owners: map[string]*outDoc{}, marked: map[ir.FeatureUse]bool{}, fileTaken: map[string]bool{},
	}
	uses := ir.Features(p)
	for _, u := range uses {
		e.led.Decide(u)
	}
	pls := livePipelines(p)
	for i, pl := range pls {
		e.pipeline(pl, i == 0)
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
	led       *emitkit.Ledger
	said      map[ir.FeatureUse][2]string
	owners    map[string]*outDoc
	marked    map[ir.FeatureUse]bool
	fileTaken map[string]bool
	extra     map[string]any
	origin    string
	files     []*outFile
	own       bool
}

// outFile is one output file with its document.
type outFile struct {
	doc  *outDoc
	name string
}

func use(path string, f caps.Feature) ir.FeatureUse { return ir.FeatureUse{IRPath: path, Feature: f} }

func (e *emitter) lose(path string, f caps.Feature, detail string) {
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

// foreignExt reports Ext data of other providers: it is not in the file, only in the round-trip metadata.
func (e *emitter) foreignExt(path string, x ir.Ext) {
	emitkit.ForeignExt(e.led, dialect, "Concourse", path, x)
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
func (e *emitter) markArg(f caps.Feature, path string, v any) {
	line, err := emitkit.ArgLine(v)
	if err != nil {
		e.lose(path, f, "the marker cannot be written: "+err.Error())
		return
	}
	e.mark(f, path)
	if d := e.ownerOf(path); d != nil {
		d.Head = append(d.Head, line)
	}
}

// comment queues a lost-text line for the document that owns the path.
func (e *emitter) comment(path, s string) {
	if d := e.ownerOf(path); d != nil {
		d.Head = append(d.Head, lostPrefix+commentSafe(s))
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
	for _, f := range e.files {
		b, err := ymap.Marshal([]*outDoc{f.doc})
		if err != nil {
			return nil, err
		}
		out[f.name] = b
	}
	for name, v := range e.extra {
		n, err := plainNode(v)
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", name, err)
		}
		b, err := ymap.Marshal([]*outDoc{{Root: n}})
		if err != nil {
			return nil, err
		}
		out[name] = b
	}
	return out, nil
}

// node encodes an Ext value; a value that cannot be written is reported lost and gives nil.
func (e *emitter) node(path, what string, v any) *yaml.Node {
	n, err := plainNode(v)
	if err != nil {
		e.lose(path, caps.ExtPassthrough, what+" of the Concourse file cannot be written: "+err.Error())
		return nil
	}
	return n
}

// commentSafe makes s fit on one comment line.
func commentSafe(s string) string {
	s = strings.ReplaceAll(s, "${{", "{{")
	return strings.NewReplacer("\r\n", `\n`, "\n", `\n`, "\r", `\n`).Replace(s)
}

func extOf(e ir.Ext) map[string]any {
	m, _ := e[dialect].(map[string]any)
	return m
}

func sortedKeys[V any](m map[string]V) []string { return ymap.SortedKeys(m) }

// unique returns base, or base with a numeric suffix when taken, and records the result.
func unique(taken map[string]bool, base string) string {
	name := base
	for i := 2; taken[name]; i++ {
		name = base + "-" + strconv.Itoa(i)
	}
	taken[name] = true
	return name
}

// canonJSON is the canonical text of a decoded YAML value: map keys sorted, so two spellings of one mapping compare equal.
func canonJSON(v any) string {
	b, err := json.Marshal(jsonable(v))
	if err != nil {
		return ""
	}
	return string(b)
}

// jsonable turns the maps of a yaml decode into JSON-encodable values.
func jsonable(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = jsonable(x)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[fmt.Sprint(k)] = jsonable(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = jsonable(x)
		}
		return out
	case *omap:
		return jsonable(plain(t))
	}
	return v
}

// nodeCanon is the canonical text of an output node.
func nodeCanon(n *yaml.Node) string {
	var v any
	if err := n.Decode(&v); err != nil {
		return ""
	}
	return canonJSON(v)
}

// carryYAML renders an IR value as YAML text for a marker argument.
func carryYAML(v any) (string, error) {
	b, err := yaml.Marshal(v)
	return string(b), err
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
