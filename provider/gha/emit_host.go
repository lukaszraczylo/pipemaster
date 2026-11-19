package gha

import (
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// whyDisabled is the detail for a condition the output replaces with false because the step's host has no form here.
const whyDisabled = "the condition is replaced by false because the action host has no form in this dialect"

// hostArg is the marker argument that carries the host a bare ref resolved against in its source.
type hostArg struct {
	Host string `json:"host"`
}

// uses renders the uses: text for a. ok is false when this dialect cannot express a's host; the caller then
// disables the step and the ledger has a lost entry.
func (e *emitter) uses(sp string, a *ir.ActionRef) (text string, ok bool, err error) {
	if a.Kind != ir.ActionRepo {
		return actionString(a), true, nil
	}
	f := func(s caps.Support, detail string) {
		e.led.Note(ir.FeatureUse{IRPath: sp, Feature: caps.StepAction}, s, detail)
	}
	native := caps.Support{Level: caps.Native}
	switch h := a.Host; {
	case h == "":
		return repoString(a), true, nil
	case h == ir.HostSelf || h == ir.HostBuiltin:
		if e.d.ID != provider.Gitea {
			e.lose(sp, caps.StepAction, h+" actions exist only in Gitea; the step is disabled")
			e.comment(string(caps.StepAction) + " " + sp + ": " + h + sourceName(a))
			return "", false, nil
		}
		if h == ir.HostBuiltin {
			return h + a.Repo, true, nil
		}
		return h + repoString(a), true, nil
	case h == e.d.DefaultHost:
		switch e.d.ID {
		case provider.Gitea:
			f(native, "resolves against DEFAULT_ACTIONS_URL; default github")
		case provider.Forgejo:
			f(native, "resolves against DEFAULT_ACTIONS_URL; default https://data.forgejo.org")
		}
		return repoString(a), true, nil
	case e.d.ID != provider.GHA:
		f(native, "printed as an absolute URL; an instance that cannot reach "+h+" needs the URL edited")
		return h + "/" + repoString(a), true, nil
	}
	e.emulate(sp, caps.StepAction, "bare ref",
		"resolved against "+a.Host+" in the source; GitHub resolves it on github.com, where the action can differ")
	return repoString(a), true, e.markArg(caps.StepAction, sp, hostArg{Host: a.Host})
}

// sourceName is the part of a self: or builtin: ref after the prefix.
func sourceName(a *ir.ActionRef) string {
	if a.Host == ir.HostBuiltin {
		return a.Repo
	}
	return repoString(a)
}
