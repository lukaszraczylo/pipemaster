package emitkit

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	checkoutAction = "checkout"
	checkoutStepID = "source-checkout"
	concourseID    = "concourse"
	depthInput     = "fetch-depth"
)

// WithSourceCheckout returns a copy of p in which each job that carries CheckoutKey starts with a checkout step, and
// the IR paths of those jobs. A target that fetches nothing by itself needs the step. The input p stays unchanged.
func WithSourceCheckout(p *ir.Project) (*ir.Project, []string) {
	ref, ok := actions.Reverse(checkoutAction)
	if p == nil || !ok || p.Origin.Provider != concourseID {
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
			depth, has := sourceDepth(j)
			if !has {
				continue
			}
			jc := *j
			st := &ir.Step{ID: checkoutStepID, Kind: ir.StepAction, Action: &ref}
			if depth > 0 {
				st.With = map[string]ir.Value{depthInput: {Lit: strconv.Itoa(depth)}}
			}
			jc.Steps = append([]*ir.Step{st}, j.Steps...)
			pc.Jobs[k] = &jc
			paths = append(paths, "pipeline/"+pl.ID+"/job/"+j.ID)
		}
		out.Pipelines[i] = &pc
	}
	return &out, paths
}

// sourceDepth reads CheckoutKey of a job that has steps and no checkout step.
func sourceDepth(j *ir.Job) (int, bool) {
	if j == nil || len(j.Steps) == 0 {
		return 0, false
	}
	if first := j.Steps[0]; first != nil && first.Kind == ir.StepAction && first.Action != nil && first.Action.Repo == checkoutAction {
		return 0, false
	}
	ext, _ := j.Ext[concourseID].(map[string]any)
	v, ok := ext[CheckoutKey]
	if !ok {
		return 0, false
	}
	m, _ := v.(map[string]any)
	d, _ := strconv.Atoi(fmt.Sprint(m["depth"]))
	return max(d, 0), true
}

// RetainSourceCheckout keeps the checkout steps that WithSourceCheckout added out of the report. The target file holds
// the clone, and a return to the source drops the step again.
func RetainSourceCheckout(l *Ledger, jobPaths []string) {
	for _, jp := range jobPaths {
		l.Retain(ir.FeatureUse{IRPath: jp + "/step/" + checkoutStepID, Feature: caps.StepAction}, "the target clones the repository that the Concourse job fetches")
	}
}

// DropSourceCheckout removes the first step of each job when WithSourceCheckout added it. It runs when a target file is
// parsed, so the step does not come back as a step of the source.
func DropSourceCheckout(p *ir.Project) {
	if p == nil {
		return
	}
	for _, pl := range p.Pipelines {
		for _, j := range pl.Jobs {
			if len(j.Steps) > 0 && j.Steps[0] != nil && j.Steps[0].ID == checkoutStepID && j.Steps[0].Kind == ir.StepAction {
				j.Steps = j.Steps[1:]
			}
		}
	}
}
