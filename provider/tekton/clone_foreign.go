package tekton

import (
	"fmt"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	cloneImage    = "alpine/git:latest"
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
	b.job.Steps = append(b.job.Steps, &ir.Step{ID: id, Kind: ir.StepScript, Image: &ir.Image{Ref: cloneImage}, Script: script})
	b.c.p.rep.Add(report.Entry{
		IRPath: b.path, Feature: caps.StepAction, Level: caps.Emulated, Strategy: "git clone script",
		Detail:    "the git-clone url is another repository than the pipeline's own, so the step clones it with git; a checkout would clone the pipeline repository",
		SourceLoc: b.c.file,
	})
	return nil
}
