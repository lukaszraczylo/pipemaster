package gitlab

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// Kinds of action step the marker can carry back.
const (
	actCache    = "cache"
	actUpload   = "upload"
	actDownload = "download"
	actScript   = "script"
)

type wireValue struct {
	Lit    string       `json:"l,omitempty"`
	Secret string       `json:"s,omitempty"`
	Expr   string       `json:"x,omitempty"`
	Kind   ir.ValueKind `json:"k,omitempty"`
}

type wireVar struct {
	Name  string    `json:"n"`
	Value wireValue `json:"v"`
}

// wireStep is the action step as the source wrote it, which the GitLab file holds only as a job keyword.
type wireStep struct {
	Action          *ir.ActionRef        `json:"action,omitempty"`
	With            map[string]wireValue `json:"with,omitempty"`
	Timeout         *ir.Duration         `json:"timeout,omitempty"`
	ID              string               `json:"id"`
	Name            string               `json:"name,omitempty"`
	If              string               `json:"if,omitempty"`
	Shell           string               `json:"shell,omitempty"`
	WorkDir         string               `json:"workdir,omitempty"`
	Env             []wireVar            `json:"env,omitempty"`
	ContinueOnError bool                 `json:"coe,omitempty"`
}

func toWireValue(v ir.Value) wireValue {
	w := wireValue{Kind: v.Kind, Lit: v.Lit, Secret: v.Secret}
	if v.Kind == ir.ValueExpr {
		w.Expr = expr.String(v.Expr)
	}
	return w
}

func (w wireValue) value() (ir.Value, bool) {
	v := ir.Value{Kind: w.Kind, Lit: w.Lit, Secret: w.Secret}
	if w.Kind == ir.ValueExpr {
		x, err := expr.ParseCanonical(w.Expr)
		if err != nil {
			return v, false
		}
		v.Expr = x
	}
	return v, true
}

func toWireStep(st *ir.Step) wireStep {
	w := wireStep{Action: st.Action, Timeout: st.Timeout, ID: st.ID, Name: st.Name, Shell: st.Shell,
		WorkDir: st.WorkDir, ContinueOnError: st.ContinueOnError}
	if st.If != nil {
		w.If = expr.String(st.If)
	}
	if len(st.With) > 0 {
		w.With = map[string]wireValue{}
		for k, v := range st.With {
			w.With[k] = toWireValue(v)
		}
	}
	for _, v := range st.Env {
		w.Env = append(w.Env, wireVar{v.Name, toWireValue(v.Value)})
	}
	return w
}

func (w wireStep) step() (*ir.Step, bool) {
	st := &ir.Step{Kind: ir.StepAction, Action: w.Action, Timeout: w.Timeout, ID: w.ID, Name: w.Name, Shell: w.Shell,
		WorkDir: w.WorkDir, ContinueOnError: w.ContinueOnError}
	if w.If != "" {
		x, err := expr.ParseCanonical(w.If)
		if err != nil {
			return nil, false
		}
		st.If = x
	}
	if len(w.With) > 0 {
		st.With = map[string]ir.Value{}
		for k, wv := range w.With {
			v, ok := wv.value()
			if !ok {
				return nil, false
			}
			st.With[k] = v
		}
	}
	for _, wv := range w.Env {
		v, ok := wv.Value.value()
		if !ok {
			return nil, false
		}
		st.Env = append(st.Env, ir.Var{Name: wv.Name, Value: v})
	}
	return st, true
}

// wireArt is what one upload added to the job's single artifacts block.
type wireArt struct {
	Name   string   `json:"name"`
	Expire string   `json:"expire,omitempty"`
	When   string   `json:"when,omitempty"`
	Paths  []string `json:"paths,omitempty"`
	First  bool     `json:"first,omitempty"`
}

// downJob is a needed job whose artifacts a download step fetched, with the flag the IR held before.
type downJob struct {
	ID  string `json:"id"`
	Was bool   `json:"was,omitempty"`
}

// actionStepArg is the marker argument of an action step that became cache, artifacts or needs.
type actionStepArg struct {
	Cache *ir.Cache `json:"cache,omitempty"`
	Art   *wireArt  `json:"art,omitempty"`
	Step  *wireStep `json:"step,omitempty"`
	Kind  string    `json:"kind"`
	Jobs  []downJob `json:"jobs,omitempty"`
	// Script is the text of the script lines that stand for the step.
	Script string   `json:"script,omitempty"`
	Vars   []string `json:"vars,omitempty"`
	Pos    int      `json:"pos"`
}

// markActionStep carries the original step beside the keyword it became.
func (e *emitter) markActionStep(p *jobPrep, sp string, st *ir.Step, a actionStepArg) error {
	ws := toWireStep(st)
	a.Step = &ws
	a.Pos = slices.Index(p.j.Steps, st)
	line, err := markerArg(a)
	if err != nil {
		return err
	}
	p.pending = append(p.pending, emitkit.Marker(caps.StepAction, sp), line)
	return nil
}

// collapseActionStep puts the action step back and removes the keyword it stands for. It acts only when the
// parsed job still holds exactly what the step produced; an edited file keeps its fields.
func collapseActionStep(c *collapseCtx, a actionStepArg) bool {
	jobPath, _, ok := cutStepPath(c.path)
	if !ok || a.Step == nil {
		return false
	}
	j := findJobAt(c.pl, jobPath)
	st, sok := a.Step.step()
	if j == nil || !sok {
		return false
	}
	at := slices.IndexFunc(j.Steps, func(o *ir.Step) bool { return o.ID == st.ID })
	if a.Kind == actScript && at >= 0 && strings.Join(j.Steps[at].Script, "\n") == a.Script {
		j.Env = slices.DeleteFunc(slices.Clone(j.Env), func(v ir.Var) bool { return slices.Contains(a.Vars, v.Name) })
		j.Steps[at] = st
		return true
	}
	if at >= 0 && len(j.Steps[at].Script) > 0 {
		c.ignore(editedNote(st.ID, "script"))
		return false
	}
	var done bool
	switch a.Kind {
	case actCache:
		done = a.Cache != nil && removeCache(j, *a.Cache)
	case actUpload:
		done = a.Art != nil && removeUpload(j, *a.Art)
	case actDownload:
		done = restoreNeeds(j, a.Jobs)
	}
	if !done {
		c.ignore(editedNote(st.ID, a.Kind))
		return false
	}
	if at >= 0 {
		// The step marker the script carries for it parses as an empty script step.
		j.Steps[at] = st
		return true
	}
	j.Steps = slices.Insert(j.Steps, min(max(a.Pos, 0), len(j.Steps)), st)
	return true
}

func editedNote(step, what string) string {
	return "marker ignored: file edited, so the parsed " + what + " stands and step " + step + " is not restored"
}

func cutStepPath(path string) (jobPath, stepID string, ok bool) {
	for i := 0; i+len(stepSep) <= len(path); i++ {
		if path[i:i+len(stepSep)] == stepSep {
			return path[:i], path[i+len(stepSep):], true
		}
	}
	return "", "", false
}

func removeCache(j *ir.Job, want ir.Cache) bool {
	for i, c := range j.Cache {
		if !reflect.DeepEqual(c, want) {
			continue
		}
		j.Cache = slices.Delete(slices.Clone(j.Cache), i, i+1)
		if extras, ok := extOf(j.Ext)[extCache].([]any); ok && len(extras) == len(j.Cache)+1 {
			extras = slices.Delete(slices.Clone(extras), i, i+1)
			dropExt(&j.Ext, extCache)
			if anyNonEmpty(extras) {
				setExt(&j.Ext, extCache, extras)
			}
		}
		return true
	}
	return false
}

func anyNonEmpty(l []any) bool {
	for _, x := range l {
		if m, ok := x.(map[string]any); !ok || len(m) > 0 {
			return true
		}
	}
	return false
}

func removeUpload(j *ir.Job, a wireArt) bool {
	if len(j.Artifacts) != 1 {
		return false
	}
	art := j.Artifacts[0]
	if a.First && (art.Name != a.Name || art.Expire != a.Expire) {
		return false
	}
	for _, p := range a.Paths {
		if !slices.Contains(art.Paths, p) {
			return false
		}
	}
	art.Paths = slices.DeleteFunc(slices.Clone(art.Paths), func(p string) bool { return slices.Contains(a.Paths, p) })
	if len(art.Paths) == 0 {
		j.Artifacts = nil
		if a.When != "" {
			if w, _ := extOf(j.Ext)[extArtifacts].(map[string]any); w != nil && w["when"] == a.When {
				dropExt(&j.Ext, extArtifacts, "when")
			}
		}
		return true
	}
	j.Artifacts = []ir.Artifact{art}
	return true
}

func restoreNeeds(j *ir.Job, jobs []downJob) bool {
	for _, d := range jobs {
		i := slices.IndexFunc(j.Needs, func(n ir.Need) bool { return n.JobID == d.ID })
		if i < 0 || !j.Needs[i].Artifacts {
			return false
		}
	}
	for _, d := range jobs {
		i := slices.IndexFunc(j.Needs, func(n ir.Need) bool { return n.JobID == d.ID })
		j.Needs[i].Artifacts = d.Was
	}
	return true
}

func decodeActionStep(arg string) (actionStepArg, bool) {
	var a actionStepArg
	if json.Unmarshal([]byte(arg), &a) != nil || a.Kind == "" {
		return a, false
	}
	return a, true
}

// carriedKind is true for the action kinds whose step the marker carries, so the metadata need not.
func carriedKind(k string) bool {
	return k == actions.KindCache || k == actions.KindUpload || k == actions.KindDownload
}

// markDownload carries a download step, whose effect lands in the needs the emitter writes later.
func (e *emitter) markDownload(d download, p *jobPrep, jobs []downJob) error {
	ws := toWireStep(d.step)
	pid, _, _ := strings.Cut(strings.TrimPrefix(p.path, "pipeline/"), "/")
	for i := range jobs {
		jobs[i].ID = strings.TrimPrefix(jobs[i].ID, pid+foldSep)
	}
	line, err := markerArg(actionStepArg{Kind: actDownload, Jobs: jobs, Step: &ws, Pos: slices.Index(p.j.Steps, d.step)})
	if err != nil {
		return err
	}
	e.comments = append(e.comments, emitkit.Marker(caps.StepAction, d.sp), line)
	return nil
}
