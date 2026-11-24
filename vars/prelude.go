package vars

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

// The prelude is a prefix of the first script line, so no line index of the script moves. The marker starts
// it and the end token closes it; Strip cuts everything between them.
const (
	startMark = "export PIPEMASTER_VARS="
	endMark   = ": pipemaster-end;"
)

const (
	eventsKey  = "events"
	canonical  = provider.GHA
	lostFormat = ": \"${%s:?pipemaster: lost: variable %s has no %s source}\""
)

// Result holds the report entries of one Prepare call.
type Result struct{ entries []report.Entry }

// Report adds the entries to rep.
func (r *Result) Report(rep *report.Report) {
	if r == nil || rep == nil {
		return
	}
	for _, e := range r.entries {
		rep.Add(e)
	}
}

// Prepare returns p with a variable prelude on each script step that reads a predefined variable that target
// does not set. It copies what it changes and leaves p as it was.
func Prepare(p *ir.Project, target string) (*ir.Project, *Result) {
	res := &Result{}
	t, err := Load()
	if p == nil || err != nil {
		return p, res
	}
	paths := map[*ir.Step]string{}
	for _, n := range ir.Nodes(p) {
		if n.Step != nil {
			paths[n.Step] = n.Path
		}
	}
	pr := &preparer{t: t, target: target, res: res, paths: paths}
	out := *p
	out.Pipelines = make([]*ir.Pipeline, len(p.Pipelines))
	for i, pl := range p.Pipelines {
		out.Pipelines[i] = pr.pipeline(pl)
	}
	return &out, res
}

type preparer struct {
	t      *Table
	res    *Result
	paths  map[*ir.Step]string
	target string
}

func (pr *preparer) pipeline(pl *ir.Pipeline) *ir.Pipeline {
	if pl == nil {
		return nil
	}
	cp := *pl
	cp.Jobs = pr.jobs(pl, pl.Jobs)
	cp.Finally = pr.jobs(pl, pl.Finally)
	return &cp
}

func (pr *preparer) jobs(pl *ir.Pipeline, jobs []*ir.Job) []*ir.Job {
	if jobs == nil {
		return nil
	}
	out := make([]*ir.Job, len(jobs))
	for i, j := range jobs {
		if j == nil {
			continue
		}
		cp := *j
		cp.Steps = make([]*ir.Step, len(j.Steps))
		for k, st := range j.Steps {
			cp.Steps[k] = pr.step(st, append(slices.Clone(pl.Env), j.Env...))
		}
		out[i] = &cp
	}
	return out
}

func scriptStep(st *ir.Step) bool {
	return st != nil && len(st.Script) > 0 && st.Action == nil && !emitkit.IsFailLoud(st.Script)
}

func (pr *preparer) step(st *ir.Step, outer []ir.Var) *ir.Step {
	if !scriptStep(st) {
		return st
	}
	env, order := effective(outer, st.Env)
	text := strings.Join(st.Script, "\n")
	var names []string
	for _, n := range Refs(text, true) {
		names = appendNew(names, n)
	}
	var reexport []string
	expand := provider.Family(pr.target) != provider.GitLab
	for _, name := range order {
		v := env[name]
		if v.Kind != ir.ValueLit || strings.ContainsRune(v.Lit, '\n') {
			continue
		}
		refs := slices.DeleteFunc(Refs(v.Lit, false), func(n string) bool { return !pr.t.predefined(n) })
		if len(refs) == 0 {
			continue
		}
		for _, n := range refs {
			names = appendNew(names, n)
		}
		if expand {
			reexport = append(reexport, name)
		}
	}
	var guards, defs, defined, lost []string
	for _, n := range names {
		if _, user := env[n]; user || pr.t.skip[n] || pr.t.Native(n, pr.target) {
			continue
		}
		rhs, ok := pr.resolve(n)
		switch {
		case ok:
			defs = append(defs, fmt.Sprintf("export %s=\"%s\"", n, rhs))
			defined = append(defined, n)
		case pr.t.foreign(n, pr.target):
			guards = append(guards, fmt.Sprintf(lostFormat, n, n, pr.target))
			lost = append(lost, n)
		}
	}
	for _, name := range reexport {
		defs = append(defs, fmt.Sprintf("export %s=\"%s\"", name, quoteValue(env[name].Lit)))
	}
	if len(guards)+len(defs) == 0 {
		return st
	}
	cp := *st
	cp.Script = slices.Clone(st.Script)
	if !place(cp.Script, startMark+fmt.Sprint(len(guards)+len(defs))+"; "+strings.Join(append(guards, defs...), "; ")+"; "+endMark) {
		pr.note(st, caps.Lost, "", "the script starts with a shebang line and has no other line, so the variable prelude has no place")
		return st
	}
	pr.report(st, defined, reexport, lost)
	return &cp
}

// place puts prefix before the first script line that is not a shebang.
func place(script []string, prefix string) bool {
	for i, entry := range script {
		lines := strings.Split(entry, "\n")
		for k, l := range lines {
			if strings.HasPrefix(l, "#!") {
				continue
			}
			lines[k] = prefix + " " + l
			script[i] = strings.Join(lines, "\n")
			return true
		}
	}
	return false
}

func (pr *preparer) report(st *ir.Step, defined, reexport, lost []string) {
	if len(defined)+len(reexport) > 0 {
		var parts []string
		if len(defined) > 0 {
			parts = append(parts, "the script reads "+strings.Join(defined, ", ")+", which "+pr.target+" does not set; a prelude defines them from the "+pr.target+" values")
		}
		if len(reexport) > 0 {
			parts = append(parts, pr.target+" does not expand variable values, so the prelude sets "+strings.Join(reexport, ", ")+" again in the shell")
		}
		pr.note(st, caps.Emulated, "variable prelude", strings.Join(parts, "; "))
	}
	if len(lost) > 0 {
		pr.note(st, caps.Lost, "", "the script reads "+strings.Join(lost, ", ")+", which "+pr.target+" has no source for; the step stops with a message when it starts")
	}
}

func (pr *preparer) note(st *ir.Step, level caps.Level, strategy, detail string) {
	pr.res.entries = append(pr.res.entries, report.Entry{
		IRPath: pr.paths[st], Feature: caps.ScriptInterpolation, Level: level, Strategy: strategy, Detail: detail,
	})
}

// resolve gives the shell text that holds the variable called name on the target.
func (pr *preparer) resolve(name string) (string, bool) {
	o, ok := pr.t.owner[name]
	if !ok {
		return "", false
	}
	e := pr.t.raw.Vars[o.id]
	if e.Values != "" {
		return pr.eventValue(o)
	}
	return pr.t.spell(o.id, pr.target, 0)
}

// eventValue maps an event value of the provider that owns the name to the target's value.
func (pr *preparer) eventValue(o owner) (string, bool) {
	cur, ok := pr.t.spell(o.id, pr.target, 0)
	if !ok {
		return "", false
	}
	col := provider.Family(pr.target)
	if templated[col] {
		col = canonical
	}
	var cases []string
	seen := map[string]bool{}
	keys := make([]string, 0, len(pr.t.raw.Events))
	for k := range pr.t.raw.Events {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		from, to := pr.t.raw.Events[k][o.provider], pr.t.raw.Events[k][col]
		if from == "" || to == "" || seen[to] {
			continue
		}
		seen[to] = true
		cases = append(cases, to+") printf %s "+from+";;")
	}
	cases = append(cases, "*) printf %s "+cur+";;")
	return "`case " + cur + " in " + strings.Join(cases, " ") + " esac`", true
}

// predefined reports whether name is a predefined variable of any provider.
func (t *Table) predefined(name string) bool {
	if t.skip[name] {
		return false
	}
	if _, ok := t.owner[name]; ok {
		return true
	}
	for _, k := range t.known {
		if k[name] {
			return true
		}
	}
	return false
}

// effective merges the variable lists, later ones winning, and returns the names in order of first use.
func effective(lists ...[]ir.Var) (map[string]ir.Value, []string) {
	env := map[string]ir.Value{}
	var order []string
	for _, l := range lists {
		for _, v := range l {
			if _, seen := env[v.Name]; !seen {
				order = append(order, v.Name)
			}
			env[v.Name] = v.Value
		}
	}
	return env, order
}

func appendNew(l []string, s string) []string {
	if slices.Contains(l, s) {
		return l
	}
	return append(l, s)
}

// quoteValue writes a variable value as the inside of a double-quoted shell string that still expands $NAME.
func quoteValue(v string) string {
	v = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$(", `\$(`, "$$", `\$`).Replace(v)
	return v
}

// Strip removes the prelude that Prepare wrote from the scripts of p.
func Strip(p *ir.Project) {
	if p == nil {
		return
	}
	for _, n := range ir.Nodes(p) {
		if n.Step == nil {
			continue
		}
		for i, entry := range n.Step.Script {
			if strings.Contains(entry, startMark) {
				n.Step.Script[i] = stripEntry(entry)
			}
		}
	}
}

func stripEntry(entry string) string {
	lines := strings.Split(entry, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, startMark) {
			continue
		}
		if k := strings.Index(l, endMark); k >= 0 {
			lines[i] = strings.TrimPrefix(l[k+len(endMark):], " ")
		}
	}
	return strings.Join(lines, "\n")
}
