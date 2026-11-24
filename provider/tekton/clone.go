package tekton

import (
	"path"
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	checkoutRepository = "repository"
	checkoutRef        = "ref"
	checkoutPath       = "path"
	checkoutSparse     = "sparse-checkout"
	checkoutDepth      = "fetch-depth"
	ownRepoParam       = paramPrefix + "ctx-repo" + paramClose
	ownShaParam        = paramPrefix + "ctx-sha" + paramClose
	repoSuffix         = ".git"
)

// cloneRepo reads a clone url as the repository input of the checkout. The url of the pipeline's own repository
// is the ctx-repo param, and the checkout needs no input for it. Another host has no spelling.
func cloneRepo(x *texts, url string) (v ir.Value, own, ok bool) {
	if !strings.HasPrefix(url, cloneHost) || !strings.HasSuffix(url, repoSuffix) {
		return v, false, false
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(url, cloneHost), repoSuffix)
	switch {
	case rest == ownRepoParam:
		return v, true, true
	case !strings.Contains(rest, paramPrefix) && strings.Count(rest, "/") != 1:
		return v, false, false
	}
	return x.value(rest), false, true
}

// cloneSparse turns the comma list of git-clone into the line list of the checkout.
func cloneSparse(x *texts, list string) (ir.Value, bool) {
	if strings.Contains(list, paramPrefix) {
		return ir.Value{}, false
	}
	var lines []string
	for _, d := range strings.Split(list, ",") {
		if d = strings.TrimSpace(d); d != "" {
			lines = append(lines, d)
		}
	}
	return x.value(strings.Join(lines, "\n")), true
}

// clonePath joins the directory below the workspace and the subdirectory into the path input.
func clonePath(x *texts, parts ...string) (ir.Value, bool) {
	p := path.Join(parts...)
	if p == "." || p == "" {
		return ir.Value{}, false
	}
	return x.value(p), true
}

// cloneDefaults are the values at which a StepAction param has no effect on a checkout.
var cloneDefaults = map[string]string{
	"refspec": "", "submodulePaths": "", "sslVerify": "true", "crtFileName": "ca-bundle.crt",
	"deleteExisting": "true", "httpProxy": "", "httpsProxy": "", "noProxy": "", "verbose": "true",
	"userFriendlyErrors": "true", "ssh-directory-path": "", "basic-auth-path": "", "ssl-ca-directory-path": "",
}

// cloneInputs maps the params of the git-clone StepAction to checkout inputs. A param with no spelling is
// returned by name, and the step then has no checkout form.
func cloneInputs(x *texts, params map[string]string) (map[string]ir.Value, []string) {
	with := map[string]ir.Value{}
	var bad []string
	names := make([]string, 0, len(params))
	for n := range params {
		names = append(names, n)
	}
	sort.Strings(names)
	var own bool
	var rel, sub string
	if url, ok := params["url"]; ok {
		repo, isOwn, good := cloneRepo(x, url)
		switch {
		case !good:
			bad = append(bad, "url")
		case !isOwn:
			with[checkoutRepository] = repo
		default:
			own = true
		}
	}
	for _, n := range names {
		val := params[n]
		switch n {
		case "url":
		case "revision":
			if val != "" && (!own || val != ownShaParam) {
				with[checkoutRef] = x.value(val)
			}
		case "output-path":
			switch {
			case val == workspaceDir:
			case strings.HasPrefix(val, workspaceDir+"/"):
				rel = strings.TrimPrefix(val, workspaceDir+"/")
			default:
				bad = append(bad, n)
			}
		case "subdirectory":
			sub = val
		case "sparseCheckoutDirectories":
			if v, ok := cloneSparse(x, val); !ok {
				bad = append(bad, n)
			} else if val != "" {
				with[checkoutSparse] = v
			}
		case "depth":
			with[checkoutDepth] = x.value(val)
		case checkoutSubmodules:
			with[checkoutSubmodules] = x.value(val)
		default:
			if want, same := cloneDefaults[n]; !same || val != want {
				bad = append(bad, n)
			}
		}
	}
	if v, ok := clonePath(x, rel, sub); ok {
		with[checkoutPath] = v
	}
	if _, set := with[checkoutSubmodules]; !set {
		with[checkoutSubmodules] = ir.Value{Lit: "true"}
	}
	return with, bad
}

// gitClone reads a StepAction ref to the catalog git-clone as a checkout, when every param has a spelling. A
// step with a param that has none stays without an action, so every other target writes a failing step.
func (b *jobBuilder) gitClone(st *ir.Step, rv, pv any) {
	rm := asMapOrNil(rv)
	if rm == nil {
		return
	}
	if res, _ := text(rm.Vals["resolver"]); res != resolverGit {
		return
	}
	if p, _ := refParam(rm, paramPathInRepo); p != gitClonePath {
		return
	}
	params := map[string]string{}
	var bad []string
	list, _ := pv.([]any)
	for _, e := range list {
		m := asMapOrNil(e)
		name, _ := text(valueOf(m, "name"))
		val, isText := text(m.Vals["value"])
		if !isText {
			bad = append(bad, name)
			continue
		}
		params[name] = val
	}
	with, more := cloneInputs(b.tx, params)
	if bad = append(bad, more...); len(bad) > 0 {
		sort.Strings(bad)
		for _, name := range bad {
			b.c.p.rep.Add(report.Entry{
				IRPath: b.path + "/step/" + st.ID, Feature: caps.StepAction, Level: caps.Lost, Strategy: droppedStrategy,
				Detail:    "param " + name + " of the git-clone StepAction has no checkout input, or its value cannot be translated",
				SourceLoc: b.c.file,
			})
		}
		return
	}
	ref, ok := actions.Reverse(actionCheckout)
	if !ok {
		return
	}
	st.Action, st.With = &ref, with
}
