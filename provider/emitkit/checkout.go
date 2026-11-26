package emitkit

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	checkoutAction = "checkout"
	checkoutStepID = "source-checkout"
	depthInput     = "fetch-depth"
	submodulesIn   = "submodules"

	originConcourse  = "concourse"
	originGitLab     = "gitlab"
	originDrone      = "drone"
	originWoodpecker = "woodpecker"

	gitStrategyVar   = "GIT_STRATEGY"
	gitStrategyNone  = "none"
	gitDepthVar      = "GIT_DEPTH"
	gitSubmodulesVar = "GIT_SUBMODULE_STRATEGY"
	gitSubNormal     = "normal"
	gitSubRecursive  = "recursive"

	// SubmodulesOn is a value of Checkout.Submodules, spelled as the submodules input of the checkout action.
	SubmodulesOn = "true"
	// SubmodulesDeep is the recursive value of Checkout.Submodules.
	SubmodulesDeep = "recursive"

	// woodpeckerCloneImage is the prefix of the default clone plugin image of Woodpecker (plugin-git).
	woodpeckerCloneImage = "woodpeckerci/plugin-git"
)

// Checkout is the clone that the CI system of a source runs before the steps of a job. Depth 0 is the full history.
type Checkout struct {
	Submodules string
	Depth      int
}

// checkoutDetail explains why a target without an implicit clone writes the step, by origin.
var checkoutDetail = map[string]string{
	originGitLab:     "GitLab clones the repository before every job; GIT_DEPTH and GIT_SUBMODULE_STRATEGY map to the step inputs, and GHA does not clone",
	originDrone:      "Drone clones the repository before the steps (https://docs.drone.io/pipeline/docker/syntax/cloning/); clone.depth maps to the step input, and GHA does not clone",
	originWoodpecker: "Woodpecker clones the repository before the steps (https://woodpecker-ci.org/docs/usage/workflow-syntax); the depth and recursive settings of the clone map to the step inputs, and GHA does not clone",
	originConcourse:  "Concourse fetches the repository with the get of the job; the depth param of the get maps to the step input, and GHA does not clone",
}

// SourceCheckoutDetail is the report text for a checkout step that the target needs and the origin did not write. It is empty for an origin with no implicit clone.
func SourceCheckoutDetail(origin string) string { return checkoutDetail[origin] }

// SourceCheckout reads the implicit clone of a job from its origin. It reports false when the origin does not
// clone for the job: no steps, a checkout step already there, or the clone is turned off or replaced.
func SourceCheckout(origin string, pl *ir.Pipeline, j *ir.Job) (Checkout, bool) {
	if j == nil || len(j.Steps) == 0 {
		return Checkout{}, false
	}
	if first := j.Steps[0]; first != nil && first.Kind == ir.StepAction && first.Action != nil && first.Action.Repo == checkoutAction {
		return Checkout{}, false
	}
	ext, _ := j.Ext[origin].(map[string]any)
	switch origin {
	case originConcourse:
		return concourseCheckout(ext)
	case originGitLab:
		return gitlabCheckout(pl, j)
	case originDrone:
		return droneCheckout(ext)
	case originWoodpecker:
		return woodpeckerCheckout(ext)
	}
	return Checkout{}, false
}

func concourseCheckout(ext map[string]any) (Checkout, bool) {
	v, ok := ext[CheckoutKey]
	if !ok {
		return Checkout{}, false
	}
	m, _ := v.(map[string]any)
	return Checkout{Depth: depthOf(m["depth"])}, true
}

func gitlabCheckout(pl *ir.Pipeline, j *ir.Job) (Checkout, bool) {
	lit := func(name string) string {
		for _, vars := range [][]ir.Var{j.Env, pl.Env} {
			for _, v := range vars {
				if v.Name == name && v.Value.Kind == ir.ValueLit {
					return v.Value.Lit
				}
			}
		}
		return ""
	}
	if lit(gitStrategyVar) == gitStrategyNone {
		return Checkout{}, false
	}
	c := Checkout{Depth: depthOf(lit(gitDepthVar))}
	switch lit(gitSubmodulesVar) {
	case gitSubNormal:
		c.Submodules = SubmodulesOn
	case gitSubRecursive:
		c.Submodules = SubmodulesDeep
	}
	return c, true
}

// droneCheckout follows https://docs.drone.io/pipeline/docker/syntax/cloning/: the default clone runs unless
// clone.disable is true, clone.depth sets the depth, and the default clone fetches no submodules.
func droneCheckout(ext map[string]any) (Checkout, bool) {
	clone, _ := ext["clone"].(map[string]any)
	if truth(clone["disable"]) {
		return Checkout{}, false
	}
	return Checkout{Depth: depthOf(clone["depth"])}, true
}

// woodpeckerCheckout follows https://woodpecker-ci.org/docs/usage/workflow-syntax: skip_clone turns the clone off,
// a clone section with another image or no git step replaces the plugin, and the git settings carry depth and recursive.
func woodpeckerCheckout(ext map[string]any) (Checkout, bool) {
	if truth(ext["skip_clone"]) {
		return Checkout{}, false
	}
	clone, has := ext["clone"].(map[string]any)
	if !has {
		return Checkout{}, true
	}
	git, _ := clone["git"].(map[string]any)
	if image, _ := git["image"].(string); git == nil || image != "" && !strings.HasPrefix(image, woodpeckerCloneImage) {
		return Checkout{}, false
	}
	settings, _ := git["settings"].(map[string]any)
	c := Checkout{Depth: depthOf(settings["depth"])}
	if truth(settings["recursive"]) {
		c.Submodules = SubmodulesOn
	}
	return c, true
}

func truth(v any) bool { return fmt.Sprint(v) == "true" }

// depthOf reads a positive depth from a YAML scalar. Anything else is the full history.
func depthOf(v any) int {
	if v == nil {
		return 0
	}
	d, err := strconv.Atoi(strings.TrimSuffix(fmt.Sprint(v), ".0"))
	if err != nil {
		return 0
	}
	return max(d, 0)
}

// WithSourceCheckout returns a copy of p in which each job that its origin clones for starts with a checkout step, and
// the IR paths of those jobs. A target that fetches nothing by itself needs the step. The input p stays unchanged.
func WithSourceCheckout(p *ir.Project) (*ir.Project, []string) {
	ref, ok := actions.Reverse(checkoutAction)
	if p == nil || !ok || checkoutDetail[p.Origin.Provider] == "" {
		return p, nil
	}
	var paths []string
	out := *p
	out.Pipelines = slices.Clone(p.Pipelines)
	for i, pl := range out.Pipelines {
		if pl == nil {
			continue
		}
		pc := *pl
		pc.Jobs = slices.Clone(pl.Jobs)
		for k, j := range pc.Jobs {
			c, has := SourceCheckout(p.Origin.Provider, pl, j)
			if !has {
				continue
			}
			jc := *j
			st := &ir.Step{ID: checkoutStepID, Kind: ir.StepAction, Action: &ref}
			if c.Depth > 0 || c.Submodules != "" {
				st.With = map[string]ir.Value{}
			}
			if c.Depth > 0 {
				st.With[depthInput] = ir.Value{Lit: strconv.Itoa(c.Depth)}
			}
			if c.Submodules != "" {
				st.With[submodulesIn] = ir.Value{Lit: c.Submodules}
			}
			jc.Steps = append([]*ir.Step{st}, j.Steps...)
			pc.Jobs[k] = &jc
			paths = append(paths, "pipeline/"+pl.ID+"/job/"+j.ID)
		}
		out.Pipelines[i] = &pc
	}
	return &out, paths
}

// RetainSourceCheckout keeps the checkout steps that WithSourceCheckout added out of the report. The target file holds
// the clone, and a return to the source drops the step again.
func RetainSourceCheckout(l *Ledger, jobPaths []string) {
	for _, jp := range jobPaths {
		l.Retain(ir.FeatureUse{IRPath: jp + "/step/" + checkoutStepID, Feature: caps.StepAction}, "the target clones the repository that the source clones before the job")
		l.Retain(ir.FeatureUse{IRPath: jp, Feature: caps.ExtPassthrough}, "the clone adds target keys that a return without metadata keeps in Ext")
	}
}

// GeneratedCheckouts counts the checkout steps that an emitter marked in the YAML files of fsys. A step with the
// same name but no marker is native to the file, so a parser passes this count to DropSourceCheckout.
func GeneratedCheckouts(fsys fs.FS) int {
	n := 0
	_ = fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error { // #nosec G104 -- an unreadable file has no marker, so it counts zero
		if err != nil || d.IsDir() || (path.Ext(name) != ".yaml" && path.Ext(name) != ".yml") {
			return nil
		}
		src, rerr := fs.ReadFile(fsys, name)
		if rerr != nil {
			return nil
		}
		for _, m := range ParseMarkers(src) {
			if m.Feature == caps.StepAction && strings.HasSuffix(m.IRPath, "/step/"+checkoutStepID) {
				n++
			}
		}
		return nil
	})
	return n
}

// DropSourceCheckout removes the first step of each job when WithSourceCheckout added it, at most generated times.
// It runs when a target file is parsed, so the step does not come back as a step of the source.
func DropSourceCheckout(p *ir.Project, generated int) {
	if p == nil {
		return
	}
	for _, pl := range p.Pipelines {
		for _, j := range pl.Jobs {
			if generated > 0 && len(j.Steps) > 0 && j.Steps[0] != nil && j.Steps[0].ID == checkoutStepID && j.Steps[0].Kind == ir.StepAction {
				j.Steps = j.Steps[1:]
				generated--
			}
		}
	}
}
