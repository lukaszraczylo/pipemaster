package gitlab

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgha "github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	maxKeyFiles    = 2
	defaultCacheID = "default"
	hashFilesFn    = "hashFiles"
	defaultArtName = "artifact"
)

// artAcc is the single artifacts block a GitLab job can carry, merged from every upload.
type artAcc struct {
	name    string
	expire  string
	when    string
	paths   []string
	exclude []string
	seen    bool
}

// ghaRef spells an action reference the way a workflow does, for messages.
func ghaRef(a *ir.ActionRef) string {
	switch a.Kind {
	case ir.ActionDocker:
		return "docker://" + a.Image
	case ir.ActionLocal:
		return a.Path
	}
	s := a.Owner + "/" + a.Repo
	if a.Path != "" {
		s += "/" + a.Path
	}
	if a.Ref != "" {
		s += "@" + a.Ref
	}
	return s
}

// ghaText renders a with-value in workflow syntax, which actions.Lookup and its Templated keys expect.
func ghaText(v ir.Value) (string, bool) {
	switch v.Kind {
	case ir.ValueSecret:
		return "${{ secrets." + v.Secret + " }}", true
	case ir.ValueExpr:
		if c, ok := v.Expr.(expr.Call); ok {
			if parts, ok := emitkit.UnmixFormat(c); ok {
				s, err := exprgha.PrintTemplate(parts)
				return s, err == nil
			}
		}
		s, err := exprgha.Print(v.Expr)
		return "${{ " + s + " }}", err == nil
	}
	return v.Lit, true
}

// ghaCanon rewrites workflow template text as the canonical template text the field helpers read.
func ghaCanon(s string) (string, error) {
	if !strings.Contains(s, rawExprOpen) {
		return s, nil
	}
	parts, err := exprgha.ParseTemplate(s)
	if err != nil {
		return "", err
	}
	return expr.PrintScriptTemplate(parts)
}

// canonTarget moves every string of a lookup result from workflow syntax to canonical syntax, once. A value
// that does not parse is lost, so no raw expression reaches the output.
func (e *emitter) canonTarget(sp string, t *actions.Target) {
	conv := func(what, s string) (string, bool) {
		out, err := ghaCanon(s)
		if err != nil {
			e.lose(sp, caps.StepAction, what+" is not a valid expression template: "+err.Error())
			return "", false
		}
		return out, true
	}
	for k, v := range t.With {
		switch x := v.(type) {
		case string:
			if out, ok := conv("input "+k, x); ok {
				t.With[k] = out
			} else {
				delete(t.With, k)
			}
		case []string:
			var outs []string
			for _, item := range x {
				if out, ok := conv("input "+k, item); ok {
					outs = append(outs, out)
				}
			}
			t.With[k] = outs
		}
	}
	if out, ok := conv("image", t.Image); ok {
		t.Image = out
	} else {
		t.Image = ""
	}
	for i, l := range t.Script {
		out, _ := conv("script line", l)
		t.Script[i] = out
	}
}

// actionStep maps an action step through the actions table; anything it cannot map is kept as a comment line.
func (e *emitter) actionStep(p *jobPrep, st *ir.Step) error {
	sp := stepPath(p.path, st.ID)
	e.stepLosses(sp, st)
	if st.Action == nil || st.Kind == ir.StepPipelineCall {
		e.lose(sp, caps.StepAction, "step kind "+string(st.Kind)+" has no GitLab form")
		p.pending = append(p.pending, lostPrefix+commentSafe("step "+st.ID+" kind "+string(st.Kind)))
		e.unappliedFields(sp, st, "the step is lost")
		return nil
	}
	with := map[string]string{}
	for k, v := range st.With {
		s, ok := ghaText(v)
		if !ok {
			e.lose(sp, caps.StepAction, "input "+k+" is an expression with no workflow spelling")
			continue
		}
		with[k] = s
	}
	t, ok := actions.Lookup(*st.Action, "gitlab", with)
	if !ok {
		e.lose(sp, caps.StepAction, "no GitLab mapping for action "+ghaRef(st.Action))
		p.pending = append(p.pending, lostPrefix+commentSafe("step "+st.ID+" uses "+ghaRef(st.Action)))
		e.unappliedFields(sp, st, "the step is lost")
		return nil
	}
	e.led.Note(e.use(sp, caps.StepAction), t.Support, t.Support.Caveat)
	for _, k := range t.Unmapped {
		e.lose(sp, caps.StepAction, "input "+k+" has no GitLab equivalent")
	}
	if t.Support.Level == caps.Lost {
		p.pending = append(p.pending, lostPrefix+commentSafe("step "+st.ID+" uses "+ghaRef(st.Action)+": "+t.Support.Caveat))
		e.unappliedFields(sp, st, "the step is lost")
		return nil
	}
	if !carriedKind(t.Kind) {
		e.lose(sp, caps.ExtPassthrough, "the action reference "+ghaRef(st.Action)+" is not in the GitLab file; it is kept in round-trip metadata")
	}
	e.canonTarget(sp, &t)
	return e.applyTarget(p, sp, st, t)
}

// unappliedFields reports each step field that a job keyword cannot carry. When the step becomes script lines
// the guards apply instead, and when it becomes an artifacts block its status maps to artifacts:when.
func (e *emitter) unappliedFields(sp string, st *ir.Step, why string) {
	if st.If != nil {
		e.lose(sp, caps.StepIf, "a step condition cannot guard a job keyword: "+why)
	}
	e.unappliedEnvFields(sp, st, why)
}

func (e *emitter) unappliedEnvFields(sp string, st *ir.Step, why string) {
	if len(st.Env) > 0 {
		e.lose(sp, caps.StepEnv, "step env cannot attach to a job keyword: "+why)
	}
	if st.WorkDir != "" {
		e.lose(sp, caps.StepWorkdir, "step working directory cannot attach to a job keyword: "+why)
	}
	if st.ContinueOnError {
		e.lose(sp, caps.StepContinueOnError, "continue-on-error cannot attach to a job keyword: "+why)
	}
}

func (e *emitter) applyTarget(p *jobPrep, sp string, st *ir.Step, t actions.Target) error {
	switch t.Kind {
	case actions.KindScript:
		return e.placeLines(p, st, t.Script)
	case actions.KindUpload:
		e.unappliedEnvFields(sp, st, "the upload becomes the job's artifacts block")
		art, err := e.uploadAction(p, sp, st, t)
		if err != nil {
			return err
		}
		return e.markActionStep(p, sp, st, actionStepArg{Kind: actUpload, Art: art})
	}
	e.unappliedFields(sp, st, "the action becomes a job keyword")
	switch t.Kind {
	case actions.KindImplicit:
		var names []string
		for _, k := range []string{"GIT_DEPTH", "GIT_SUBMODULE_STRATEGY"} {
			if v, ok := t.With[k].(string); ok {
				p.vars = append(p.vars, ir.Var{Name: k, Value: ir.Value{Lit: v}})
				names = append(names, k)
			}
		}
		return e.markAction(p, sp, actionArg{Vars: names})
	case actions.KindImage:
		before := p.image
		if err := e.setImage(p, sp, t.Image); err != nil || p.image == before {
			return err
		}
		return e.markAction(p, sp, actionArg{Image: p.image})
	case actions.KindCache:
		c, err := e.cacheAction(p, sp, t)
		if err != nil || c == nil {
			if err == nil {
				p.pending = append(p.pending, lostPrefix+commentSafe("step "+st.ID+" uses "+ghaRef(st.Action)))
			}
			return err
		}
		return e.markActionStep(p, sp, st, actionStepArg{Kind: actCache, Cache: c})
	case actions.KindDownload:
		name, _ := t.With["name"].(string)
		p.downs = append(p.downs, download{name: name, step: st, sp: sp})
		if _, ok := t.With["path"]; ok {
			e.lose(sp, caps.StepAction, "download path has no GitLab equivalent; artifacts restore to the project directory")
		}
	default:
		e.lose(sp, caps.StepAction, "action kind "+t.Kind+" has no GitLab form")
		p.pending = append(p.pending, lostPrefix+commentSafe("step "+st.ID+" uses "+ghaRef(st.Action)))
	}
	return nil
}

func (e *emitter) setImage(p *jobPrep, sp, image string) error {
	image, err := e.field(sp, caps.StepAction, image)
	if err != nil {
		return err
	}
	switch {
	case image == "":
	case p.j.Container != nil:
		e.lose(sp, caps.StepAction, "the job already sets image "+p.j.Container.Ref+"; the action's image is dropped")
	case p.image != "":
		e.lose(sp, caps.StepAction, "the job image is already "+p.image+" from an earlier action; this image is dropped")
	default:
		p.image = image
	}
	return nil
}

// artifactWhen maps an upload step's status function to artifacts:when. Any other condition has no keyword.
func (e *emitter) artifactWhen(sp string, st *ir.Step) string {
	if st.If == nil {
		return ""
	}
	rest, status, ok := splitStatus(st.If)
	if !ok || rest != nil {
		e.lose(sp, caps.StepIf, "artifacts:when takes only always, on_failure or on_success, not a condition")
		return ""
	}
	switch status {
	case "success":
		e.led.Note(e.use(sp, caps.StepIf), caps.Support{Level: caps.Native, Strategy: "artifacts:when on_success is the default"}, "")
	case "always":
		e.emulate(sp, caps.StepIf, "artifacts:when", "the upload runs by artifacts:when: always")
		return whenAlways
	case "failure":
		e.emulate(sp, caps.StepIf, "artifacts:when", "the upload runs by artifacts:when: on_failure")
		return whenOnFailure
	}
	return ""
}

func (e *emitter) uploadAction(p *jobPrep, sp string, st *ir.Step, t actions.Target) (*wireArt, error) {
	text := func(v any) (string, error) {
		s, _ := v.(string)
		return e.field(sp, caps.StepAction, s)
	}
	list := func(v any) ([]string, error) {
		in, _ := v.([]string)
		out := make([]string, 0, len(in))
		for _, s := range in {
			t, err := e.field(sp, caps.StepAction, s)
			if err != nil {
				return nil, err
			}
			out = append(out, t)
		}
		return out, nil
	}
	name, err := text(t.With["name"])
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = defaultArtName
	}
	expire, err := text(t.With["expire_in"])
	if err != nil {
		return nil, err
	}
	paths, err := list(t.With["path"])
	if err != nil {
		return nil, err
	}
	excl, err := list(t.With["exclude"])
	if err != nil {
		return nil, err
	}
	when := e.artifactWhen(sp, st)
	if p.art.seen && p.art.name != name {
		e.lose(sp, caps.StepAction, "a GitLab job has one artifacts block; artifact "+name+" is merged into "+p.art.name)
	}
	if p.art.seen && when != p.art.when {
		e.lose(sp, caps.StepIf, "a GitLab job has one artifacts:when; the first upload's value applies")
	}
	first := !p.art.seen
	if first {
		p.art.name, p.art.expire, p.art.when = name, expire, when
	}
	p.art.seen = true
	p.art.paths = appendNew(p.art.paths, paths)
	p.art.exclude = appendNew(p.art.exclude, excl)
	p.uploads = append(p.uploads, name)
	art := &wireArt{Name: p.art.name, Expire: p.art.expire, When: p.art.when, Paths: paths, First: first}
	return art, nil
}

func appendNew(dst, src []string) []string {
	for _, s := range src {
		if !slices.Contains(dst, s) {
			dst = append(dst, s)
		}
	}
	return dst
}

// cacheAction maps actions/cache to a cache block; hashFiles keys become cache:key:files.
func (e *emitter) cacheAction(p *jobPrep, sp string, t actions.Target) (*ir.Cache, error) {
	key, _ := t.With["key"].(string)
	paths, _ := t.With["paths"].([]string)
	c := newMap()
	if err := e.cacheKey(sp, c, key); err != nil {
		return nil, err
	}
	cp := make([]string, 0, len(paths))
	for _, pa := range paths {
		t, err := e.field(sp, caps.StepAction, pa)
		if err != nil {
			return nil, err
		}
		cp = append(cp, t)
	}
	c.set("paths", strSeq(cp))
	parsed, _, err := newState(nil).cacheEntry(c.n)
	if err != nil {
		// Without the parsed block no marker can restore the step, so the action must not vanish unreported.
		e.lose(sp, caps.StepAction, "the cache block cannot be read back: "+err.Error())
		return nil, nil
	}
	p.caches = append(p.caches, c)
	return &parsed, nil
}

// cacheKey writes the key of a cache block: key:files when the key ends in a hashFiles call over at most
// two literal files, otherwise the key as text. Text and variable parts before the call become the prefix.
func (e *emitter) cacheKey(path string, c *omap, key string) error {
	if key == "" {
		return nil
	}
	files, prefix, ok, err := e.hashKey(path, key)
	if err != nil {
		return err
	}
	if !ok {
		t, err := e.field(path, caps.ScriptInterpolation, key)
		if err != nil {
			return err
		}
		c.set("key", str(t))
		return nil
	}
	if len(files) > maxKeyFiles {
		e.lose(path, caps.StepAction, fmt.Sprintf("cache:key:files takes at most %d files, the key hashes %d", maxKeyFiles, len(files)))
		files = nil
	}
	if len(files) == 0 {
		if prefix == "" {
			prefix = defaultCacheID
		}
		c.set("key", str(prefix))
		return nil
	}
	k := newMap()
	k.set("files", strSeq(files))
	if prefix != "" {
		k.set("prefix", str(prefix))
	}
	c.set("key", k.n)
	return nil
}

// hashKey recognises `[parts-]${{ hashFiles('f', ...) }}` in canonical template text. A part GitLab has no
// variable for (runner.os) leaves the prefix and is reported; any other kind of part makes the key not a hash key.
func (e *emitter) hashKey(path, key string) (files []string, prefix string, ok bool, err error) {
	parts, perr := expr.ParseScriptTemplate(key)
	if perr != nil || len(parts) == 0 {
		return nil, "", false, nil
	}
	call, isCall := parts[len(parts)-1].Expr.(expr.Call)
	if !isCall || call.Name != hashFilesFn {
		return nil, "", false, nil
	}
	for _, a := range call.Args {
		l, isLit := a.(expr.Lit)
		if !isLit || l.Kind != expr.LitStr {
			return nil, "", false, nil
		}
		files = append(files, l.Str)
	}
	var kept []expr.Part
	var dropped []string
	for _, p := range parts[:len(parts)-1] {
		if p.Expr == nil {
			kept = append(kept, p)
			continue
		}
		r, isRef := p.Expr.(expr.Ref)
		if !isRef {
			return nil, "", false, nil
		}
		if _, has := refVar(r.Path); has {
			kept = append(kept, p)
		} else {
			dropped = append(dropped, expr.String(p.Expr))
		}
	}
	text, lost, err := convertParts(kept)
	if err != nil || len(lost) > 0 {
		return nil, "", false, err
	}
	for _, d := range dropped {
		e.emulate(path, caps.StepAction, "cache key prefix without "+d, d+" has no GitLab variable, so the cache is shared across it")
	}
	text = strings.TrimSuffix(text, "-")
	if len(dropped) > 0 {
		text = strings.TrimLeft(text, "-")
	}
	return files, text, true, nil
}

// actionArg names what an action step added to the job, so Parse can remove it again.
type actionArg struct {
	Image string   `json:"image,omitempty"`
	Vars  []string `json:"vars,omitempty"`
}

func (e *emitter) markAction(p *jobPrep, sp string, a actionArg) error {
	if a.Image == "" && len(a.Vars) == 0 {
		return nil
	}
	line, err := markerArg(a)
	if err != nil {
		return err
	}
	p.pending = append(p.pending, emitkit.Marker(caps.StepAction, sp), line)
	return nil
}
