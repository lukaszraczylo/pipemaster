package drone

import (
	"fmt"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/provider/internal/container"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	platformOS   = "os"
	platformArch = "arch"
	keyPlatform  = "platform"
)

// doc is the state of one kind: pipeline document.
type doc struct {
	tx     *texts
	rep    *report.Report
	ext    *sink
	job    *ir.Job
	trig   *trigger
	marks  *marks
	merged *merged
	file   string
	id     string
	// written is the step list as the file holds it, before an action marker puts steps back.
	written []*ir.Step
	// pipeEnv is the pipeline environment that the marker holds.
	pipeEnv []ir.Var
	// base is the number of file lines before the document.
	base int
	// docker is true when the type is empty or docker; other types are read as Docker steps and keep their type.
	docker bool
	// noop is true when the only step was the placeholder that stands in for a job with no step.
	noop bool
}

// resource is a document that is not a pipeline and has an Ext form.
type resource struct{ value any }

func (d *doc) jobPath() string { return pipelinePth + "/job/" + d.id }

// parseChunk reads one document. It returns the pipeline, or the resource that goes to Ext, or neither for a
// blank or dropped document.
func parseChunk(rep *report.Report, c chunk) (*doc, *resource, error) {
	v, b, err := decodeYAML([]byte(c.text))
	if err != nil {
		return nil, nil, err
	}
	base := c.start - 1
	res := newSink(rep, fileYML, pipelinePth, base)
	for _, line := range b.skipped {
		res.note(caps.ExtPassthrough, "a merge key (<<) whose value is not a mapping adds no key and is dropped", line)
	}
	for _, rp := range b.repeats {
		res.note(caps.ExtPassthrough, "key "+rp.key+" is defined twice; the last value wins and the earlier one is lost", rp.line)
	}
	if v == nil {
		return nil, nil, nil
	}
	top, ok := asMap(v)
	if !ok {
		return nil, nil, fmt.Errorf("a document must be a mapping")
	}
	kind, ok := text(top.vals["kind"])
	switch {
	case !ok || kind == "":
		return nil, nil, fmt.Errorf("kind is required")
	case kind == kindSignature:
		rep.Add(report.Entry{
			IRPath: pipelinePth, Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "dropped",
			Detail: "kind: signature is a server HMAC of the file; it is dropped", SourceLoc: fmt.Sprintf("%s:%d", fileYML, c.start),
		})
		return nil, nil, nil
	case kind != kindPipeline:
		res.note(caps.ExtPassthrough, "kind: "+kind+" is not a pipeline; the document is kept in Ext", top.lines["kind"])
		return nil, &resource{value: plain(top)}, nil
	}
	d, err := newDoc(rep, top, base)
	if err != nil {
		return nil, nil, err
	}
	d.marks = readMarks(c.text)
	collapseDoc(d)
	return d, nil, nil
}

func newDoc(rep *report.Report, top *omap, base int) (*doc, error) {
	r := newReader(top)
	r.take("kind")
	name, _ := textOf(r.take("name"))
	d := &doc{tx: &texts{}, rep: rep, file: fileYML, id: name, base: base, docker: true}
	if d.id == "" {
		d.id = fallbackID
	}
	d.ext = newSink(rep, fileYML, d.jobPath(), base)
	d.job = &ir.Job{ID: d.id}
	if typ, ok := textOf(r.take("type")); ok && typ != "" && typ != typeDocker {
		d.docker = false
		d.ext.keep("type", top.vals["type"], caps.ExtPassthrough, "pipeline type "+typ+" is not docker; kept in Ext and the steps are read as Docker steps", r.line("type"))
	}
	stepsNode, ok := r.take("steps")
	if !ok {
		return nil, fmt.Errorf("steps is required")
	}
	if err := d.steps(stepsNode); err != nil {
		return nil, err
	}
	if v, ok := r.take("services"); ok {
		if err := d.services(v); err != nil {
			return nil, err
		}
	}
	d.platform(r)
	d.node(r)
	if err := d.environment(r); err != nil {
		return nil, err
	}
	if err := d.needs(r); err != nil {
		return nil, err
	}
	if v, ok := r.take(keyTrigger); ok {
		t, err := d.readTrigger(v, r.line(keyTrigger))
		if err != nil {
			return nil, err
		}
		d.trig = t
	}
	d.ext.keepRest(r, map[string]caps.Feature{"concurrency": caps.PipelineConcurrency})
	d.noteKept()
	d.job.Ext = d.ext.ext()
	return d, nil
}

func textOf(v any, ok bool) (string, bool) {
	if !ok {
		return "", false
	}
	return text(v)
}

// platform maps platform {os, arch} to the runner label os/arch, the form the Woodpecker and GitLab parsers use
// for a platform. Other platform keys have no IR field and stay in Ext.
func (d *doc) platform(r *reader) {
	v, ok := r.take(keyPlatform)
	if !ok {
		return
	}
	m, isMap := asMap(v)
	osName, hasOS := text(valueOf(m, platformOS))
	arch, hasArch := text(valueOf(m, platformArch))
	if !isMap || !hasOS || !hasArch {
		r.release(keyPlatform)
		return
	}
	label, err := d.tx.template(osName + "/" + arch)
	if err != nil {
		r.release(keyPlatform)
		return
	}
	d.job.Runner.Labels = append(d.job.Runner.Labels, label)
	rest := &omap{vals: map[string]any{}, lines: map[string]int{}}
	for _, k := range m.keys {
		if k != platformOS && k != platformArch {
			rest.set(k, m.vals[k], m.lines[k])
		}
	}
	if len(rest.keys) == 0 {
		return
	}
	d.ext.vals[keyPlatform] = plain(rest)
	for _, k := range rest.keys {
		d.ext.note(caps.JobRunner, "platform."+k+" has no IR field; kept in Ext", rest.lines[k])
	}
}

func valueOf(m *omap, key string) any {
	if m == nil {
		return nil
	}
	return m.vals[key]
}

// node maps the runner labels of node when every value is a scalar. A key with the value true that is a valid label
// name is a runner tag, the form that the emitter writes; any other key is the runner selector.
func (d *doc) node(r *reader) {
	m, ok := asMap(r.m.vals["node"])
	if !ok {
		return
	}
	sel := map[string]string{}
	var tags []string
	for _, k := range m.keys {
		t, ok := text(m.vals[k])
		if !ok {
			return
		}
		v := unescape(t)
		if _, valid := emitkit.TagKey(k); valid && v == emitkit.TagValue {
			tags = append(tags, k)
			continue
		}
		sel[k] = v
	}
	r.take("node")
	d.job.Runner.Labels = append(d.job.Runner.Labels, tags...)
	if len(sel) > 0 {
		d.job.Runner.Selector = sel
	}
}

func (d *doc) environment(r *reader) error {
	v, ok := r.take("environment")
	if !ok {
		return nil
	}
	m, isMap := asMap(v)
	if !isMap {
		return fmt.Errorf("line %d: environment must be a mapping", r.line("environment"))
	}
	vars, left := container.Env(d.decoder(), m)
	d.job.Env = vars
	if len(left) > 0 {
		rest := m.subset(left)
		d.ext.vals["environment"] = plain(rest)
		for _, name := range rest.keys {
			d.ext.note(caps.JobEnv, "environment."+name+" has no Value form; kept in Ext", rest.lines[name])
		}
	}
	return nil
}

// needs maps pipeline depends_on to job needs.
func (d *doc) needs(r *reader) error {
	v, ok := r.take("depends_on")
	if !ok {
		return nil
	}
	ns, ok := textList(v)
	if !ok {
		return fmt.Errorf("line %d: depends_on must be a name or a list of names", r.line("depends_on"))
	}
	for _, n := range ns {
		d.job.Needs = append(d.job.Needs, ir.Need{JobID: n})
	}
	return nil
}

// noteKept reports each ${...} form that stayed shell text: Drone substitutes it before parsing, and no IR
// reference stands for it.
func (d *doc) noteKept() {
	seen := map[string]bool{}
	for _, f := range d.tx.kept {
		if seen[f] {
			continue
		}
		seen[f] = true
		d.rep.Add(report.Entry{
			IRPath: d.jobPath(), Feature: caps.ScriptInterpolation, Level: caps.Lost, Strategy: "kept as written",
			Detail: f + " is a Drone substitution with no IR reference; it stays in the text as written", SourceLoc: d.file,
		})
	}
}
