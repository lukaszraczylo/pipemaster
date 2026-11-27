package tekton

import (
	"fmt"
	"strings"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	// cloneImage is the pin of the checkout mapping in actions/mappings.yaml.
	cloneImage    = "alpine/git:v2.54.1@sha256:29cff25811f790b9f72fc266bf8009a5a911714c0e03c31c47339c50909d9aef"
	cloneDepthDef = "1"
	cloneNoQuote  = "\"`\\"
)

// cloneParam gives the text of the param of a git-clone param list with the folded name key.
func cloneParam(list []any, key string) (string, bool) {
	for _, e := range list {
		m := asMapOrNil(e)
		name, _ := text(valueOf(m, "name"))
		if paramKey(name) != key {
			continue
		}
		if val, ok := text(m.Vals["value"]); ok {
			return val, true
		}
	}
	return "", false
}

// foreignCloneStep adds the step that clones another repository than the pipeline's own. A value that a shell
// double quote cannot hold has no spelling, and the conversion fails with an error then.
func (b *jobBuilder) foreignCloneStep(id, url string, with map[string]ir.Value, list []any) error {
	get := func(key, def string) string {
		if v, ok := cloneParam(list, key); ok {
			return v
		}
		return def
	}
	rev, depth, dir, subs := get("revision", ""), get("depth", cloneDepthDef), get("subdirectory", ""), get("submodules", "true")
	if _, sparse := with[checkoutSparse]; sparse || strings.Contains(subs, paramPrefix) || strings.ContainsAny(url+rev+depth+dir, cloneNoQuote) {
		err := fmt.Errorf("the git-clone of another repository has no script spelling: a sparse list, a variable submodules value or a quote in a value")
		b.c.p.rep.Add(report.Entry{
			IRPath: b.path, Feature: caps.StepAction, Level: caps.Lost, Strategy: droppedStrategy,
			Detail: err.Error(), SourceLoc: b.c.file,
		})
		return err
	}
	words := map[string]string{}
	for k, v := range map[string]string{"url": url, "revision": rev, "depth": depth, "subdirectory": dir} {
		t, err := b.px.template(v)
		if err != nil {
			return err
		}
		words[k] = t
	}
	target := words["subdirectory"]
	if target == "" {
		target = "."
	}
	fetch := "git fetch -q"
	if d := words["depth"]; d != "" && d != "0" {
		fetch += " --depth " + d
	}
	fetch += ` origin "` + firstNonEmpty(words["revision"], "HEAD") + `"`
	script := []string{
		`git init -q "` + target + `"`,
		`cd "` + target + `"`,
		`git remote add origin "` + words["url"] + `"`,
		fetch,
		"git checkout -q FETCH_HEAD",
	}
	if subs != "false" {
		script = append(script, "git submodule update --init --recursive")
	}
	b.job.Steps = append(b.job.Steps, &ir.Step{ID: id, Kind: ir.StepScript, Image: &ir.Image{Ref: cloneImage, Entrypoint: []string{""}}, Script: script})
	b.c.p.rep.Add(report.Entry{
		IRPath: b.path, Feature: caps.StepAction, Level: caps.Emulated, Strategy: "git clone script",
		Detail:    "the git-clone url is another repository than the pipeline's own, so the step clones it with git; a checkout would clone the pipeline repository",
		SourceLoc: b.c.file,
	})
	return nil
}

// sourceClone is a clone of another repository that filled a source workspace.
type sourceClone struct {
	task *catalogTask
	list []any
}

// otherClone reports whether a clone param list names a repository other than the pipeline's own.
func otherClone(list []any) bool {
	url, ok := cloneParam(list, "url")
	if !ok || url == "" {
		return false
	}
	_, own, spelled := cloneSpelling(url)
	return !own || !spelled
}

// sourceCloneStep gives a job that reads the workspace of a clone of another repository the same clone, in
// place of a checkout of the pipeline repository. The Tekton emitter skips it, because the clone task fills the
// workspace.
func (b *jobBuilder) sourceCloneStep(id string) {
	sc := b.srcClone
	var st *ir.Step
	if url, foreign := foreignClone(sc.list); foreign {
		if err := b.foreignCloneStep(id, url, nil, sc.list); err != nil {
			return
		}
		n := len(b.job.Steps) - 1
		st = b.job.Steps[n]
		b.job.Steps = b.job.Steps[:n]
	} else {
		ref, ok := actions.Reverse(actionCheckout)
		if !ok {
			return
		}
		with, _ := b.checkoutWith(sc.task, sc.list, false)
		if len(with) == 0 {
			with = nil
		}
		st = &ir.Step{ID: id, Kind: ir.StepAction, Action: &ref, With: with}
		b.c.p.rep.Add(report.Entry{
			IRPath: b.path, Feature: caps.StepAction, Level: caps.Emulated, Strategy: checkoutStrategy,
			Detail: "the Task reads the workspace that the clone task filled, which is not shared between jobs, so the job runs the same clone", SourceLoc: b.c.file,
		})
	}
	st.Ext = ir.Ext{dialect: map[string]any{ExtSourceCheckout: true}}
	b.job.Steps = append([]*ir.Step{st}, b.job.Steps...)
}
