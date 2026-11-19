package gitlab

import (
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

const quoted = `((?:[^"\\\x60]|\\.)*)`

var (
	cdRE     = regexp.MustCompile(`^cd "` + quoted + `" \|\| exit 1$`)
	exportRE = regexp.MustCompile(`^export ([A-Za-z_][A-Za-z0-9_]*)="` + quoted + `"$`)
	echoRE   = regexp.MustCompile(`^echo "([A-Za-z_][A-Za-z0-9_]*)=` + quoted + `" >> outputs\.env$`)
	unesc    = regexp.MustCompile(`\\(.)`)
)

func unquote(s string) string { return unesc.ReplaceAllString(s, "$1") }

// stepArgs picks the argument lines of the markers at one step path.
func stepArgs(uses []ir.FeatureUse, args map[ir.FeatureUse][]string, path string) (set map[caps.Feature]bool, byFeature map[caps.Feature][]string) {
	set, byFeature = map[caps.Feature]bool{}, map[caps.Feature][]string{}
	for _, u := range uses {
		if u.IRPath == path && isStepFeature(u.Feature) {
			set[u.Feature] = true
			byFeature[u.Feature] = args[u]
		}
	}
	return set, byFeature
}

// collapseStep undoes the nested step emulations in one go: the if guard outside, then the subshell with
// cd and exports, then the "|| true" on each line. Either every marked emulation restores or none does.
func collapseStep(pl *ir.Pipeline, path string, uses []ir.FeatureUse, args map[ir.FeatureUse][]string) map[caps.Feature]bool {
	set, byFeature := stepArgs(uses, args, path)
	fail := map[caps.Feature]bool{}
	st := findStep(pl, path)
	if st == nil || len(st.Script) == 0 {
		return fail
	}
	lines := append([]string{}, st.Script...)
	out := *st
	var ok bool
	if guard := guardArg(byFeature[caps.StepIf]); set[caps.StepIf] && guard != "" {
		if lines, ok = unguard(&out, lines, guard, sourceArg(byFeature[caps.StepIf])); !ok {
			return fail
		}
	}
	if set[caps.StepWorkdir] || set[caps.StepEnv] {
		if lines, ok = unsub(&out, lines, set[caps.StepWorkdir], set[caps.StepEnv]); !ok {
			return fail
		}
	}
	if set[caps.StepContinueOnError] {
		for i, l := range lines {
			t, has := strings.CutSuffix(l, continueSufx)
			if !has {
				return fail
			}
			lines[i] = t
		}
		out.ContinueOnError = true
	}
	out.Script = lines
	*st = out
	return set
}

// guardArg picks the step.if argument that holds the guard text; the other argument describes a move.
func guardArg(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "{") {
			return a
		}
	}
	return ""
}

// sourceArg is the condition text in the source dialect, when the emitter translated it.
func sourceArg(args []string) string {
	for _, a := range args {
		var s srcArg
		if strings.HasPrefix(a, `{"src"`) && json.Unmarshal([]byte(a), &s) == nil {
			return s.Src
		}
	}
	return ""
}

func unguard(st *ir.Step, lines []string, arg, src string) ([]string, bool) {
	var text string
	if json.Unmarshal([]byte(arg), &text) != nil || len(lines) < 3 || lines[len(lines)-1] != "fi" {
		return nil, false
	}
	x, err := expr.ParseCanonical(text)
	if err != nil {
		return nil, false
	}
	guard, ok := shellCond(x)
	if !ok {
		return nil, false
	}
	want, err := canonText("if " + guard + "; then")
	if err != nil || lines[0] != want {
		return nil, false
	}
	if src != "" {
		if x, err = expr.ParseCanonical(src); err != nil {
			return nil, false
		}
	}
	if st.If != nil {
		x = expr.Binary{Op: expr.OpAnd, L: st.If, R: x}
	}
	st.If = x
	return lines[1 : len(lines)-1], true
}

// extractMoved gives each always() step that left the job's script its own step back. It runs before the
// other step collapsers, which look the step up by its path.
func extractMoved(pl *ir.Pipeline, uses []ir.FeatureUse, args map[ir.FeatureUse][]string) {
	type move struct {
		job  *ir.Job
		step string
		arg  movedArg
	}
	var moves []move
	for _, u := range uses {
		if u.Feature != caps.StepIf {
			continue
		}
		for _, a := range args[u] {
			var mv movedArg
			if !strings.HasPrefix(a, "{") || json.Unmarshal([]byte(a), &mv) != nil || mv.Moved != "always" {
				continue
			}
			rest, ok := strings.CutPrefix(u.IRPath, jobPrefix(pl))
			if !ok {
				continue
			}
			jobID, stepID, ok := strings.Cut(rest, stepSep)
			if j := findJob(pl, jobID); ok && j != nil {
				moves = append(moves, move{arg: mv, job: j, step: stepID})
			}
		}
	}
	sort.SliceStable(moves, func(a, b int) bool { return moves[a].arg.Offset > moves[b].arg.Offset })
	type made struct {
		job  *ir.Job
		step *ir.Step
		pos  *int
	}
	var out []made
	for _, m := range moves {
		after := stepByID(m.job, secAfter)
		end := m.arg.Offset + m.arg.Lines
		if after == nil || m.arg.Offset < 0 || end > len(after.Script) {
			continue
		}
		moved := &ir.Step{ID: m.step, Kind: ir.StepScript, If: expr.Call{Name: "always"}, ContinueOnError: m.arg.COE}
		moved.Script = append([]string{}, after.Script[m.arg.Offset:end]...)
		after.Script = append(append([]string{}, after.Script[:m.arg.Offset]...), after.Script[end:]...)
		out = append(out, made{job: m.job, step: moved, pos: m.arg.Pos})
		if len(after.Script) == 0 {
			m.job.Steps = removeStep(m.job.Steps, after)
		}
	}
	for i := len(out) - 1; i >= 0; i-- {
		j, at := out[i].job, len(out[i].job.Steps)
		if out[i].pos != nil {
			at = min(max(*out[i].pos, 0), at)
		}
		j.Steps = slices.Insert(j.Steps, at, out[i].step)
	}
}

func stepByID(j *ir.Job, id string) *ir.Step {
	for _, s := range j.Steps {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func unsub(st *ir.Step, lines []string, workdir, env bool) ([]string, bool) {
	if len(lines) < 3 || lines[0] != "(" || lines[len(lines)-1] != ")" {
		return nil, false
	}
	inner := lines[1 : len(lines)-1]
	if workdir {
		m := cdRE.FindStringSubmatch(inner[0])
		if m == nil {
			return nil, false
		}
		st.WorkDir, inner = unquote(m[1]), inner[1:]
	}
	if env {
		n := 0
		for n < len(inner) {
			m := exportRE.FindStringSubmatch(inner[n])
			if m == nil {
				break
			}
			st.Env = append(st.Env, ir.Var{Name: m[1], Value: ir.Value{Lit: unquote(m[2])}})
			n++
		}
		if n == 0 {
			return nil, false
		}
		inner = inner[n:]
	}
	return inner, len(inner) > 0
}

// collapseOutputs turns the trailing dotenv writer lines back into job outputs.
func collapseOutputs(c *collapseCtx) bool {
	j := findJobAt(c.pl, c.path)
	if j == nil {
		return false
	}
	var st *ir.Step
	for _, s := range j.Steps {
		if s.ID == secScript {
			st = s
		}
	}
	if st == nil || !dotenvDeclared(j) {
		return false
	}
	var outs []ir.Output
	n := len(st.Script)
	for n > 0 {
		m := echoRE.FindStringSubmatch(st.Script[n-1])
		if m == nil {
			break
		}
		outs = append([]ir.Output{{Name: m[1], Value: unquote(m[2])}}, outs...)
		n--
	}
	if len(outs) == 0 {
		return false
	}
	j.Outputs = outs
	outputRefs(c.pl, j, markedLines(c.args))
	st.Script = st.Script[:n]
	if n == 0 {
		j.Steps = removeStep(j.Steps, st)
	}
	dropExt(&j.Ext, extArtifacts, reportsKey, dotenvKey)
	// The dotenv report was the only artifact a consumer's need could have fetched.
	if !declaresArtifacts(j) {
		for _, o := range allJobs(c.pl) {
			for i := range o.Needs {
				if o.Needs[i].JobID == j.ID {
					o.Needs[i].Artifacts = false
				}
			}
		}
	}
	return true
}

// outputRefs turns the $name variables a consumer reads from a producer's dotenv file back into needs references.
func outputRefs(pl *ir.Pipeline, producer *ir.Job, lines map[string]bool) {
	for _, j := range allJobs(pl) {
		if !slices.ContainsFunc(j.Needs, func(n ir.Need) bool { return n.JobID == producer.ID }) {
			continue
		}
		for _, o := range producer.Outputs {
			re := regexp.MustCompile(`\$(?:\{` + regexp.QuoteMeta(o.Name) + `\}|` + regexp.QuoteMeta(o.Name) + `\b)`)
			ref := "${{ needs." + producer.ID + ".outputs." + o.Name + " }}"
			for _, st := range j.Steps {
				rewriteLines(st.Script, lines, re, ref)
			}
		}
	}
}

// rewriteLines replaces re with ref in the lines the emitter marked as converted references.
func rewriteLines(script []string, marked map[string]bool, re *regexp.Regexp, ref string) {
	for i, l := range script {
		if marked[l] {
			script[i] = re.ReplaceAllLiteralString(l, ref)
		}
	}
}

// markedLines reads the lines a marker argument names; a marker without the argument names none.
func markedLines(args []string) map[string]bool {
	out := map[string]bool{}
	for _, a := range args {
		var r refArg
		if json.Unmarshal([]byte(a), &r) == nil {
			for _, l := range r.Lines {
				out[l] = true
			}
		}
	}
	return out
}

func dotenvDeclared(j *ir.Job) bool {
	a, _ := extOf(j.Ext)[extArtifacts].(map[string]any)
	r, _ := a[reportsKey].(map[string]any)
	f, _ := r[dotenvKey].(string)
	return f == dotenvFile
}

func removeStep(steps []*ir.Step, drop *ir.Step) []*ir.Step {
	var out []*ir.Step
	for _, s := range steps {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}

// dropExt removes the nested Ext[gitlab] key and prunes the maps it leaves empty.
func dropExt(e *ir.Ext, keys ...string) {
	m := extOf(*e)
	chain := []map[string]any{m}
	cur := m
	for _, k := range keys[:len(keys)-1] {
		next, _ := cur[k].(map[string]any)
		if next == nil {
			return
		}
		chain = append(chain, next)
		cur = next
	}
	delete(cur, keys[len(keys)-1])
	for i := len(chain) - 1; i > 0; i-- {
		if len(chain[i]) == 0 {
			delete(chain[i-1], keys[i-1])
		}
	}
	if len(m) == 0 {
		delete(*e, dialect)
		if len(*e) == 0 {
			*e = nil
		}
	}
}

// collapseMatrix restores include and exclude from the marker argument when the parsed entries expand to the same combinations.
func collapseMatrix(c *collapseCtx) bool {
	j := findJobAt(c.pl, c.path)
	if j == nil || j.Matrix == nil || len(c.args) != 1 {
		return false
	}
	var arg matrixArg
	if json.Unmarshal([]byte(c.args[0]), &arg) != nil {
		return false
	}
	orig := &ir.Matrix{Axes: arg.Axes, Include: arg.Include, Exclude: arg.Exclude}
	// The parsed entries hold the GitLab text of each value, so compare the original in that form too.
	got := ir.Expand(j.Matrix)
	if !reflect.DeepEqual(ir.Expand(orig), got) && !reflect.DeepEqual(ir.Expand(flatMatrix(orig)), got) {
		return false
	}
	j.Matrix = orig
	matrixRefs(j, orig, markedLines(c.args))
	return true
}

// flatMatrix returns m with each value as the parser reads it back from the GitLab text.
func flatMatrix(m *ir.Matrix) *ir.Matrix {
	flat := func(v string) string {
		t, lost, err := convert(v)
		if err != nil || len(lost) > 0 {
			return v
		}
		if c, err := canonText(t); err == nil {
			return c
		}
		return v
	}
	out := &ir.Matrix{}
	for _, a := range m.Axes {
		vals := make([]string, len(a.Values))
		for i, v := range a.Values {
			vals[i] = flat(v)
		}
		out.Axes = append(out.Axes, ir.Axis{Name: a.Name, Values: vals})
	}
	combos := func(cs []ir.Combo) []ir.Combo {
		var res []ir.Combo
		for _, c := range cs {
			n := make(ir.Combo, len(c))
			for i, kv := range c {
				n[i] = ir.KV{Key: kv.Key, Value: flat(kv.Value)}
			}
			res = append(res, n)
		}
		return res
	}
	out.Include, out.Exclude = combos(m.Include), combos(m.Exclude)
	return out
}

// matrixRefs turns the $axis variables GitLab sets for each parallel:matrix entry back into matrix references.
func matrixRefs(j *ir.Job, m *ir.Matrix, lines map[string]bool) {
	for _, a := range m.Axes {
		re := regexp.MustCompile(`\$(?:\{` + regexp.QuoteMeta(a.Name) + `\}|` + regexp.QuoteMeta(a.Name) + `\b)`)
		ref := "${{ matrix." + a.Name + " }}"
		rewriteLines(j.Runner.Labels, lines, re, ref)
		for _, st := range j.Steps {
			rewriteLines(st.Script, lines, re, ref)
		}
	}
}

// collapseConcurrency restores the pipeline concurrency group from the resource_group every job carries.
func collapseConcurrency(c *collapseCtx) bool {
	jobs := append(append([]*ir.Job{}, c.pl.Jobs...), c.pl.Finally...)
	group := ""
	for i, j := range jobs {
		g, ok := extOf(j.Ext)["resource_group"].(string)
		if !ok || (i > 0 && g != group) {
			return false
		}
		group = g
	}
	if len(jobs) == 0 {
		return false
	}
	text, err := canonText(group)
	if err != nil {
		return false
	}
	for _, j := range jobs {
		dropExt(&j.Ext, "resource_group")
	}
	c.pl.Concurrency = &ir.Concurrency{Group: text}
	return true
}

// collapseInputs restores pipeline inputs from the variables that carry them.
func collapseInputs(c *collapseCtx) bool {
	if len(c.args) != 1 {
		return false
	}
	var inputs []ir.Input
	if json.Unmarshal([]byte(c.args[0]), &inputs) != nil {
		return false
	}
	extras, _ := extOf(c.pl.Ext)[extVariables].(map[string]any)
	for _, in := range inputs {
		if !varIs(c.pl.Env, in.Name, in.Default) || !extraIs(extras[in.Name], in.Description) {
			return false
		}
	}
	for _, in := range inputs {
		c.pl.Env = dropVar(c.pl.Env, in.Name)
		dropExt(&c.pl.Ext, extVariables, in.Name)
	}
	c.pl.Inputs = inputs
	return true
}

func varIs(vs []ir.Var, name, def string) bool {
	for _, v := range vs {
		if v.Name == name {
			return v.Value.Kind == ir.ValueLit && v.Value.Lit == def
		}
	}
	return false
}

func extraIs(v any, description string) bool {
	if description == "" {
		return v == nil
	}
	m, _ := v.(map[string]any)
	return len(m) == 1 && m["description"] == description
}

func dropVar(vs []ir.Var, name string) []ir.Var {
	var out []ir.Var
	for _, v := range vs {
		if v.Name != name {
			out = append(out, v)
		}
	}
	return out
}

// collapseAction removes the image and variables an action step added to its job.
func collapseAction(c *collapseCtx) bool {
	if len(c.args) != 1 {
		return false
	}
	if as, ok := decodeActionStep(c.args[0]); ok {
		return collapseActionStep(c, as)
	}
	var a actionArg
	if json.Unmarshal([]byte(c.args[0]), &a) != nil {
		return false
	}
	jobPath, _, ok := strings.Cut(c.path, stepSep)
	if !ok {
		return false
	}
	j := findJobAt(c.pl, jobPath)
	if j == nil {
		return false
	}
	if a.Image != "" && j.Container != nil && j.Container.Ref == a.Image {
		j.Container = nil
	}
	var env []ir.Var
	for _, v := range j.Env {
		if !slices.Contains(a.Vars, v.Name) {
			env = append(env, v)
		}
	}
	j.Env = env
	return true
}

var triggerPathRE = regexp.MustCompile(`^pipeline/[^/]+/trigger/(\d+)$`)

func isTriggerMark(u ir.FeatureUse) bool { return triggerPathRE.MatchString(u.IRPath) }

// restoreTriggers replaces the parsed triggers with the ones the emitter carried in trigger markers, in index order.
func (s *state) restoreTriggers(pl *ir.Pipeline, uses []ir.FeatureUse, args map[ir.FeatureUse][]string) {
	byIndex := map[int]ir.Trigger{}
	for _, u := range uses {
		m := triggerPathRE.FindStringSubmatch(u.IRPath)
		if m == nil || !strings.HasPrefix(u.IRPath, "pipeline/"+pl.ID+"/") || len(args[u]) != 1 {
			continue
		}
		var w wireTrigger
		if json.Unmarshal([]byte(args[u][0]), &w) != nil {
			continue
		}
		i, _ := strconv.Atoi(m[1])
		byIndex[i] = w.trigger()
	}
	if len(byIndex) == 0 {
		return
	}
	var out []ir.Trigger
	for i := 0; i < len(byIndex); i++ {
		t, ok := byIndex[i]
		if !ok {
			return
		}
		out = append(out, t)
	}
	if !triggersDerive(pl.ID, out, pl.Triggers) {
		s.note("pipeline/"+pl.ID, caps.TriggerPush, "marker ignored", "marker ignored: the workflow rules were edited after conversion, so the parsed triggers stand", nil)
		return
	}
	pl.Triggers = out
}

// triggersDerive is true when writing marked as workflow rules and reading them back gives parsed: the
// marker still describes the file. A hand-edited rule block fails the check.
func triggersDerive(pid string, marked, parsed []ir.Trigger) bool {
	e := &emitter{led: emitkit.NewLedger("", dialect, adapter{}.Caps())}
	w, err := e.workflow(&ir.Pipeline{ID: pid, Triggers: marked})
	if err != nil {
		return false
	}
	got, ok, _ := newState(&report.Report{}).workflowTriggers(w)
	if !ok {
		got = defaultTriggers()
	}
	return sameTriggers(got, parsed)
}
