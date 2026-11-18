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

// actionStep maps an action step through the actions table; anything it cannot map is kept as a comment line.
func (e *emitter) actionStep(p *jobPrep, st *ir.Step) error {
	sp := stepPath(p.path, st.ID)
	if st.Action == nil || st.Kind == ir.StepPipelineCall {
		e.lose(sp, caps.StepAction, "step kind "+string(st.Kind)+" has no GitLab form")
		p.pending = append(p.pending, lostPrefix+commentSafe("step "+st.ID+" kind "+string(st.Kind)))
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
		return nil
	}
	e.led.Note(e.use(sp, caps.StepAction), t.Support, t.Support.Caveat)
	for _, k := range t.Unmapped {
		e.lose(sp, caps.StepAction, "input "+k+" has no GitLab equivalent")
	}
	if t.Support.Level == caps.Lost {
		p.pending = append(p.pending, lostPrefix+commentSafe("step "+st.ID+" uses "+ghaRef(st.Action)+": "+t.Support.Caveat))
		return nil
	}
	return e.applyTarget(p, sp, st, t)
}

func (e *emitter) applyTarget(p *jobPrep, sp string, st *ir.Step, t actions.Target) error {
	switch t.Kind {
	case actions.KindImplicit:
		for _, k := range []string{"GIT_DEPTH", "GIT_SUBMODULE_STRATEGY"} {
			if v, ok := t.With[k].(string); ok {
				p.vars = append(p.vars, ir.Var{Name: k, Value: ir.Value{Lit: v}})
			}
		}
	case actions.KindImage:
		e.setImage(p, sp, t.Image)
	case actions.KindCache:
		return e.cacheAction(p, sp, t)
	case actions.KindUpload:
		e.uploadAction(p, sp, t)
	case actions.KindDownload:
		name, _ := t.With["name"].(string)
		p.downs = append(p.downs, download{name: name})
		if _, ok := t.With["path"]; ok {
			e.lose(sp, caps.StepAction, "download path has no GitLab equivalent; artifacts restore to the project directory")
		}
	case actions.KindScript:
		for _, l := range t.Script {
			p.add(secScript, str(l))
		}
	default:
		e.lose(sp, caps.StepAction, "action kind "+t.Kind+" has no GitLab form")
		p.pending = append(p.pending, lostPrefix+commentSafe("step "+st.ID+" uses "+ghaRef(st.Action)))
	}
	return nil
}

func (e *emitter) setImage(p *jobPrep, sp, image string) {
	switch {
	case p.j.Container != nil:
		e.lose(sp, caps.StepAction, "the job already sets image "+p.j.Container.Ref+"; the action's image is dropped")
	case p.image != "":
		e.lose(sp, caps.StepAction, "the job image is already "+p.image+" from an earlier action; this image is dropped")
	default:
		p.image = image
	}
}

func (e *emitter) uploadAction(p *jobPrep, sp string, t actions.Target) {
	name, _ := t.With["name"].(string)
	if name == "" {
		name = defaultArtName
	}
	expire, _ := t.With["expire_in"].(string)
	paths, _ := t.With["path"].([]string)
	excl, _ := t.With["exclude"].([]string)
	if p.art.seen && p.art.name != name {
		e.lose(sp, caps.StepAction, "a GitLab job has one artifacts block; artifact "+name+" is merged into "+p.art.name)
	}
	if !p.art.seen {
		p.art.name, p.art.expire = name, expire
	}
	p.art.seen = true
	p.art.paths = appendNew(p.art.paths, paths)
	p.art.exclude = appendNew(p.art.exclude, excl)
	p.uploads = append(p.uploads, name)
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
func (e *emitter) cacheAction(p *jobPrep, sp string, t actions.Target) error {
	key, _ := t.With["key"].(string)
	paths, _ := t.With["paths"].([]string)
	if slices.Contains(t.Templated, "key") {
		parts, err := exprgha.ParseTemplate(key)
		if err != nil {
			return fmt.Errorf("cache key: %w", err)
		}
		canon, err := expr.PrintScriptTemplate(parts)
		if err != nil {
			return fmt.Errorf("cache key: %w", err)
		}
		key = canon
	}
	c := newMap()
	if err := e.cacheKey(sp, c, key); err != nil {
		return err
	}
	c.set("paths", strSeq(paths))
	p.caches = append(p.caches, c)
	return nil
}

// cacheKey writes the key of a cache block: key:files when the key is a hashFiles call over at most two
// literal files (with an optional literal prefix), otherwise the key as text.
func (e *emitter) cacheKey(path string, c *omap, key string) error {
	if key == "" {
		return nil
	}
	files, prefix, ok := hashKey(key)
	if !ok {
		t, err := e.field(path, caps.ScriptInterpolation, key)
		if err != nil {
			return err
		}
		c.set("key", str(t))
		return nil
	}
	pre, err := e.field(path, caps.ScriptInterpolation, prefix)
	if err != nil {
		return err
	}
	if len(files) > maxKeyFiles {
		e.lose(path, caps.StepAction, fmt.Sprintf("cache:key:files takes at most %d files, the key hashes %d", maxKeyFiles, len(files)))
		files = nil
	}
	if len(files) == 0 {
		if pre == "" {
			pre = defaultCacheID
		}
		c.set("key", str(pre))
		return nil
	}
	k := newMap()
	k.set("files", strSeq(files))
	if pre != "" {
		k.set("prefix", str(pre))
	}
	c.set("key", k.n)
	return nil
}

// hashKey recognises `[prefix-]${{ hashFiles('f', ...) }}` in canonical template text.
func hashKey(key string) (files []string, prefix string, ok bool) {
	parts, err := expr.ParseScriptTemplate(key)
	if err != nil || len(parts) == 0 || len(parts) > 2 {
		return nil, "", false
	}
	call, isCall := parts[len(parts)-1].Expr.(expr.Call)
	if !isCall || call.Name != hashFilesFn {
		return nil, "", false
	}
	for _, a := range call.Args {
		l, isLit := a.(expr.Lit)
		if !isLit || l.Kind != expr.LitStr {
			return nil, "", false
		}
		files = append(files, l.Str)
	}
	if len(parts) == 2 {
		t := parts[0].Text
		if parts[0].Expr != nil || !strings.HasSuffix(t, "-") {
			return nil, "", false
		}
		prefix = strings.TrimSuffix(t, "-")
	}
	return files, prefix, true
}
