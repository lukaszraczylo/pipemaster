package tekton

import (
	"cmp"
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

// imageArg is the image that the emitter wrote where the IR named none.
type imageArg struct {
	Image string `json:"image"`
}

// heldOutput is one job output that the result step writes from a step env variable.
type heldOutput struct {
	Name string `json:"name"`
	Env  string `json:"env"`
}

// outputsArg describes the step that writes the output values to the result files.
type outputsArg struct {
	Image   string       `json:"image"`
	Script  string       `json:"script"`
	Outputs []heldOutput `json:"outputs"`
}

// needsArg lists the needs of a finally job on tasks that run before it.
type needsArg struct {
	Needs []ir.Need `json:"needs"`
}

// envArg holds the pipeline env as IR YAML text.
type envArg struct {
	Env string `json:"env"`
}

// copiedArg names the pipeline variables that a Task holds in its stepTemplate env.
type copiedArg struct {
	Names []string `json:"names"`
}

// guardArg holds a job condition that the emitter wrote as a guard line in every script step.
type guardArg struct {
	If   string `json:"if"`
	Back string `json:"back"`
}

// portsArg holds the port texts of a service whose host ports the sidecar cannot name.
type portsArg struct {
	Ports []string `json:"ports"`
}

// matrixArg is the matrix that the emitter wrote as a list of combinations.
type matrixArg struct {
	Axes    []ir.Axis  `json:"axes,omitempty"`
	Exclude []ir.Combo `json:"exclude,omitempty"`
	Include []ir.Combo `json:"include,omitempty"`
}

// marks holds the marker arguments of one document head.
type marks struct{ args map[ir.FeatureUse][]string }

func readMarks(head string) *marks { return &marks{args: emitkit.ParseArgs([]byte(head))} }

// arg decodes the argument line of the marker of feature f at path.
func (m *marks) arg(f caps.Feature, path string, v any) bool {
	if m == nil {
		return false
	}
	lines := m.args[ir.FeatureUse{IRPath: path, Feature: f}]
	return len(lines) > 0 && json.Unmarshal([]byte(lines[0]), v) == nil
}

// ignored reports a marker that the file no longer matches: the file content stands.
func (b *jobBuilder) ignored(path string, f caps.Feature, what string) {
	b.c.p.rep.Add(report.Entry{
		IRPath: path, Feature: f, Level: caps.Lost, Strategy: "marker ignored",
		Detail: "marker ignored: " + what + " was edited after pipemaster wrote it; the file content is kept", SourceLoc: b.c.file,
	})
}

// collapse undoes the emulations that the Task document marks, when the marked text is still as written.
func (b *jobBuilder) collapse() {
	if b.marks == nil {
		return
	}
	b.collapseNoop()
	b.collapseImages()
	b.collapseShell()
	b.collapseJobGuard()
	b.collapseActions()
	b.collapseOutputs()
	b.collapseMatrix()
	b.collapseFinallyNeeds()
	b.collapsePipelineEnv()
}

// collapsePipelineEnv takes the pipeline variables off the env of the job, when they are still as written.
func (b *jobBuilder) collapsePipelineEnv() {
	var a copiedArg
	if b.c.env == nil || !b.marks.arg(caps.PipelineEnv, b.path, &a) {
		return
	}
	n := len(a.Names)
	ok := n <= len(b.job.Env)
	for i := 0; ok && i < n; i++ {
		v := b.job.Env[i]
		pv, found := b.c.env[a.Names[i]]
		ok = v.Name == a.Names[i] && found && sameValue(v.Value, pv.Value)
	}
	if !ok {
		b.ignored(b.path, caps.PipelineEnv, "the pipeline variables in the stepTemplate")
		b.c.envBad = true
		return
	}
	b.job.Env = slices.Clone(b.job.Env[n:])
}

// sameValue compares env values; a written expression reads back in canonical form, so only its kind is compared.
func sameValue(a, b ir.Value) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind == ir.ValueExpr {
		return true
	}
	return a.Lit == b.Lit && a.Secret == b.Secret
}

// collapseFinallyNeeds restores the needs that a finally task holds implicitly. The ones that a result or status
// reference gave are part of them.
func (b *jobBuilder) collapseFinallyNeeds() {
	var a needsArg
	if !b.finally || !b.marks.arg(caps.JobNeeds, b.path, &a) {
		return
	}
	for _, n := range b.job.Needs {
		if !slices.ContainsFunc(a.Needs, func(h ir.Need) bool { return h.JobID == n.JobID }) {
			b.ignored(b.path, caps.JobNeeds, "the needs of the finally task")
			return
		}
	}
	b.job.Needs = slices.Clone(a.Needs)
}

// servicePorts gives a sidecar back the port texts that the marker holds, when its containerPorts are the same.
func (b *jobBuilder) servicePorts(name string, ports []string) []string {
	path := b.path + "/service/" + name
	var a portsArg
	if b.marks == nil || !b.marks.arg(caps.JobServices, path, &a) {
		return ports
	}
	var containers []string
	for _, p := range a.Ports {
		containers = append(containers, p[strings.LastIndex(p, ":")+1:])
	}
	if !slices.Equal(containers, ports) {
		b.ignored(path, caps.JobServices, "the service ports")
		return ports
	}
	var out portsArg
	b.marks.arg(caps.JobServices, path, &out)
	return out.Ports
}

// collapseImages drops the default image from the steps that the emitter gave one.
func (b *jobBuilder) collapseImages() {
	for _, st := range b.job.Steps {
		path := b.path + "/step/" + st.ID
		var a imageArg
		if !b.marks.arg(caps.StepImage, path, &a) {
			continue
		}
		if st.Image == nil || st.Image.Ref != a.Image || len(st.Image.Entrypoint) > 0 {
			b.ignored(path, caps.StepImage, "the step image")
			continue
		}
		st.Image = nil
	}
}

// collapseNoop drops the step that stands in for a job with no step.
func (b *jobBuilder) collapseNoop() {
	var a imageArg
	if !b.marks.arg(caps.StepScript, b.path, &a) {
		return
	}
	if len(b.job.Steps) != 1 || !b.isPlaceholder(b.job.Steps[0], a.Image) {
		b.ignored(b.path, caps.StepScript, "the placeholder step")
		return
	}
	b.job.Steps = nil
}

func (b *jobBuilder) isPlaceholder(st *ir.Step, image string) bool {
	return st.ID == noopStepName && st.Kind == ir.StepScript && slices.Equal(st.Script, []string{noopScript}) &&
		b.plainStep(st) && b.holdsImage(st, image)
}

// holdsImage tells whether the step runs the given image, by its own key or through the job container.
func (b *jobBuilder) holdsImage(st *ir.Step, image string) bool {
	if st.Image == nil {
		return b.containerRef == image
	}
	return st.Image.Ref == image && len(st.Image.Entrypoint) == 0
}

// plainStep is true when the step holds nothing beyond its script and image.
func (b *jobBuilder) plainStep(st *ir.Step) bool {
	return st.If == nil && st.Shell == "" && st.WorkDir == "" && st.Timeout == nil && !st.ContinueOnError &&
		st.Name == "" && st.Ext == nil && st.Action == nil
}

// collapseOutputs turns the result step back into the values of the job outputs.
func (b *jobBuilder) collapseOutputs() {
	var a outputsArg
	if !b.marks.arg(caps.JobOutputs, b.path, &a) {
		return
	}
	at := slices.IndexFunc(b.job.Steps, func(s *ir.Step) bool { return s.ID == outputsStep })
	values, ok := map[string]string{}, at >= 0
	if ok {
		values, ok = b.heldValues(b.job.Steps[at], a)
	}
	if !ok {
		b.ignored(b.path, caps.JobOutputs, "the result step")
		return
	}
	b.job.Steps = slices.Delete(b.job.Steps, at, at+1)
	for _, o := range a.Outputs {
		b.tx.unkeep("$(results." + o.Name + ".path)")
	}
	for i := range b.job.Outputs {
		if v, held := values[b.job.Outputs[i].Name]; held {
			b.job.Outputs[i].Value = v
		}
	}
}

// heldValues reads the output values from the env of the result step, when the step is as written.
func (b *jobBuilder) heldValues(st *ir.Step, a outputsArg) (map[string]string, bool) {
	if !b.plainStep(st) || !b.holdsImage(st, a.Image) || strings.Join(st.Script, "\n") != a.Script || len(st.Env) != len(a.Outputs) {
		return nil, false
	}
	out := map[string]string{}
	for i, o := range a.Outputs {
		if st.Env[i].Name != o.Env {
			return nil, false
		}
		t, ok := valueTemplate(st.Env[i].Value)
		if !ok {
			return nil, false
		}
		out[o.Name] = t
	}
	return out, true
}

// valueTemplate prints an env value as canonical template text.
func valueTemplate(v ir.Value) (string, bool) {
	var parts []expr.Part
	switch v.Kind {
	case ir.ValueSecret:
		return "", false
	case ir.ValueExpr:
		parts = []expr.Part{{Expr: v.Expr}}
		if c, ok := v.Expr.(expr.Call); ok {
			if mixed, ok := emitkit.UnmixFormat(c); ok {
				parts = mixed
			}
		}
	default:
		parts = []expr.Part{{Text: v.Lit}}
	}
	t, err := expr.PrintScriptTemplate(parts)
	return t, err == nil
}

// collapseMatrix puts the matrix back when the combinations are the ones that the emitter wrote.
func (b *jobBuilder) collapseMatrix() {
	var a matrixArg
	if !b.marks.arg(caps.JobMatrix, b.path, &a) {
		return
	}
	mx := b.job.Matrix
	var want []ir.Combo
	for _, c := range expandAxes(a.Axes) {
		if !excluded(c, a.Exclude) {
			want = append(want, c)
		}
	}
	want = append(want, a.Include...)
	if mx == nil || len(mx.Axes) > 0 || !reflect.DeepEqual(normalCombos(mx.Include), normalCombos(want)) {
		b.ignored(b.path, caps.JobMatrix, "the matrix combinations")
		return
	}
	if names, ok := b.k.vals["matrix_include_names"].([]any); ok {
		// The names of the expanded combinations are the emitter's; only the include entries can carry one.
		tail := slices.Clone(names[min(len(names), len(want)-len(a.Include)):])
		if len(tail) == 0 || !slices.ContainsFunc(tail, func(n any) bool { return n != "" }) {
			delete(b.k.vals, "matrix_include_names")
		} else {
			b.k.vals["matrix_include_names"] = tail
		}
	}
	b.job.Matrix = &ir.Matrix{Axes: a.Axes, Include: a.Include, Exclude: a.Exclude}
}

func normalCombos(l []ir.Combo) []ir.Combo {
	out := make([]ir.Combo, len(l))
	for i, c := range l {
		out[i] = slices.Clone(c)
	}
	return out
}

// printValue fingerprints a parsed mapping the way printNode fingerprints the node that the emitter wrote.
func printValue(v any) string {
	b, err := json.Marshal(plain(v))
	if err != nil {
		return ""
	}
	return string(b)
}

// heldAction is one action step that the file holds as a different step, or not at all.
type heldAction struct {
	step *ir.Step
	path string
	arg  actionArg
}

// collapseActions puts back the action steps that the emitter wrote as a StepAction ref, a script, a copy step
// or nothing. A step that the file changed after conversion stays as the file has it.
func (b *jobBuilder) collapseActions() {
	var held []heldAction
	for path, lines := range b.marksAt(caps.StepAction) {
		h := heldAction{path: path, step: &ir.Step{}}
		if json.Unmarshal([]byte(lines[0]), &h.arg) != nil || yaml.Unmarshal([]byte(h.arg.Step), h.step) != nil || h.arg.At < 0 {
			b.ignored(path, caps.StepAction, "the action step marker")
			continue
		}
		h.step.Ext = plainExt(h.step.Ext)
		held = append(held, h)
	}
	slices.SortFunc(held, func(x, y heldAction) int { return cmp.Or(x.arg.At-y.arg.At, strings.Compare(x.path, y.path)) })
	added := 0
	for _, h := range held {
		st := *h.step
		at := slices.IndexFunc(b.job.Steps, func(s *ir.Step) bool { return s.ID == st.ID })
		switch {
		case h.arg.Wrote == "" && at >= 0:
			b.ignored(h.path, caps.StepAction, "the step that stands in for the action")
			continue
		case h.arg.Wrote != "" && (at < 0 || b.prints[st.ID] != h.arg.Wrote):
			b.ignored(h.path, caps.StepAction, "the step that stands in for the action")
			continue
		case at >= 0:
			if st.If == nil {
				st.If = b.job.Steps[at].If
			}
			b.job.Steps[at] = &st
		default:
			b.job.Steps = slices.Insert(b.job.Steps, min(h.arg.At+added, len(b.job.Steps)), &st)
			added++
		}
		b.dropImage(h.arg.Image)
	}
}

// dropImage clears the image that a setup action gave to the steps after it.
func (b *jobBuilder) dropImage(ref string) {
	if ref == "" {
		return
	}
	for _, st := range b.job.Steps {
		if st.Image != nil && st.Image.Ref == ref && len(st.Image.Entrypoint) == 0 {
			st.Image = nil
		}
	}
}

// marksAt lists the argument lines of the markers of feature f in the Task document, by path.
func (b *jobBuilder) marksAt(f caps.Feature) map[string][]string {
	out := map[string][]string{}
	prefix := b.path + "/step/"
	for u, lines := range b.marks.args {
		if u.Feature == f && strings.HasPrefix(u.IRPath, prefix) && len(lines) > 0 {
			out[u.IRPath] = lines
		}
	}
	return out
}

// plainExt reads Ext that came through YAML as the maps that the parsers hold.
func plainExt(e ir.Ext) ir.Ext {
	if e == nil {
		return nil
	}
	b, err := yaml.Marshal(e)
	if err != nil {
		return e
	}
	var out ir.Ext
	if yaml.Unmarshal(b, &out) != nil {
		return e
	}
	return out
}

// collapseShell turns the shell variables that stand for references back into references.
func (b *jobBuilder) collapseShell() {
	for path, lines := range b.marksAt(caps.ScriptInterpolation) {
		var a shellArg
		st := b.stepAt(path)
		if st == nil || json.Unmarshal([]byte(lines[0]), &a) != nil || !b.shellAsWritten(st, a) {
			b.ignored(path, caps.ScriptInterpolation, "the step script")
			continue
		}
		at := map[string]map[int]string{}
		for _, u := range a.Refs {
			if at[u.Name] == nil {
				at[u.Name] = map[int]string{}
			}
			for _, n := range u.At {
				at[u.Name][n] = u.Ref
			}
			if family, _, _ := strings.Cut(u.Ref, "."); family != expr.PrefixEnv {
				st.Env = slices.DeleteFunc(st.Env, func(v ir.Var) bool { return v.Name == u.Name && reflect.DeepEqual(v.Value, refValue(u.Ref)) })
			}
		}
		seen := map[string]int{}
		for i, l := range st.Script {
			st.Script[i] = shellVarRE.ReplaceAllStringFunc(l, func(m string) string {
				name := m[2 : len(m)-1]
				n := seen[name]
				seen[name]++
				if ref, ok := at[name][n]; ok {
					return "${{ " + ref + " }}"
				}
				return m
			})
		}
	}
}

var shellVarRE = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

func (b *jobBuilder) stepAt(path string) *ir.Step {
	id := strings.TrimPrefix(path, b.path+"/step/")
	for _, st := range b.job.Steps {
		if st.ID == id {
			return st
		}
	}
	return nil
}

// refValue is the env value that the emitter writes for a secret or a variable reference.
func refValue(ref string) ir.Value {
	family, name, _ := strings.Cut(ref, ".")
	if family == expr.PrefixSecrets {
		return ir.Value{Kind: ir.ValueSecret, Secret: name}
	}
	return ir.Value{Kind: ir.ValueExpr, Expr: expr.Ref{Path: []string{family, name}}}
}

// shellAsWritten checks that the script still holds the variables that the marker counts, and the env entries
// that a secret or a variable needs.
func (b *jobBuilder) shellAsWritten(st *ir.Step, a shellArg) bool {
	for _, u := range a.Refs {
		held := 0
		for _, l := range st.Script {
			held += strings.Count(l, "${"+u.Name+"}")
		}
		if held != u.Of {
			return false
		}
		if family, _, _ := strings.Cut(u.Ref, "."); family != expr.PrefixEnv &&
			!slices.ContainsFunc(st.Env, func(v ir.Var) bool { return v.Name == u.Name && reflect.DeepEqual(v.Value, refValue(u.Ref)) }) {
			return false
		}
	}
	return true
}

// collapseJobGuard takes the job condition back from the steps that all hold it as their condition.
func (b *jobBuilder) collapseJobGuard() {
	var a guardArg
	if !b.marks.arg(caps.JobIf, b.path, &a) {
		return
	}
	var steps []*ir.Step
	for _, st := range b.job.Steps {
		if len(st.Script) > 0 {
			steps = append(steps, st)
		}
	}
	orig, err := expr.ParseCanonical(a.If)
	ok := err == nil && len(steps) > 0
	for _, st := range steps {
		ok = ok && st.If != nil && expr.String(st.If) == a.Back
	}
	if !ok {
		b.ignored(b.path, caps.JobIf, "the job guard")
		return
	}
	for _, st := range steps {
		st.If = nil
	}
	b.job.If = orig
}
