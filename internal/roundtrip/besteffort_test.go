package roundtrip_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

// irDiff is one node field that differs between two projects.
type irDiff struct {
	Path  string
	Field string
}

// fieldFeatures names the features whose lost or external entry accounts for a differing field. A field
// with no row cannot be accounted for, so a difference there always fails.
var fieldFeatures = map[string][]caps.Feature{
	"Triggers": {caps.TriggerPush, caps.TriggerPullRequest, caps.TriggerTag, caps.TriggerSchedule, caps.TriggerManual,
		caps.TriggerAPI, caps.TriggerPipelineDone, caps.TriggerResource, caps.TriggerFilterBranch,
		caps.TriggerFilterTags, caps.TriggerFilterPaths, caps.PipelineInputs, caps.ExtPassthrough},
	"Env":             {caps.PipelineEnv, caps.JobEnv, caps.StepEnv, caps.ExprOpaque, caps.ScriptInterpolation},
	"Concurrency":     {caps.PipelineConcurrency, caps.ScriptInterpolation},
	"Inputs":          {caps.PipelineInputs},
	"Permissions":     {caps.PipelinePermissions},
	"Name":            {caps.DisplayName},
	"Ext":             {caps.ExtPassthrough},
	"Runner":          {caps.JobRunner},
	"Container":       {caps.JobContainer},
	"Services":        {caps.JobServices},
	"Matrix":          {caps.JobMatrix},
	"Timeout":         {caps.JobTimeout, caps.StepTimeout},
	"Retry":           {caps.JobRetry},
	"AllowFailure":    {caps.JobAllowFailure},
	"Environment":     {caps.JobEnvironment, caps.JobEnvironmentApproval},
	"Manual":          {caps.JobManual},
	"Cache":           {caps.JobCache},
	"Artifacts":       {caps.JobArtifacts},
	"Outputs":         {caps.JobOutputs},
	"Needs":           {caps.JobNeeds, caps.JobNeedsArtifacts},
	"If":              {caps.JobIf, caps.StepIf, caps.ExprOpaque},
	"Script":          {caps.StepScript, caps.ScriptInterpolation, caps.StepAction, caps.ExtPassthrough},
	"Action":          {caps.StepAction, caps.ExtPassthrough},
	"With":            {caps.StepAction, caps.ExtPassthrough},
	"Kind":            {caps.StepAction, caps.ExtPassthrough},
	"Shell":           {caps.StepShell},
	"WorkDir":         {caps.StepWorkdir},
	"Image":           {caps.StepImage},
	"ContinueOnError": {caps.StepContinueOnError},
	"<node>":          {caps.StepAction, caps.PipelineFinally, caps.ProjectMultiPipeline},
}

// diffNodes lists the fields that differ per node path; a node present on one side only differs as "<node>".
func diffNodes(a, b *ir.Project) []irDiff {
	index := func(p *ir.Project) map[string]ir.NodeRef {
		m := map[string]ir.NodeRef{}
		for _, n := range ir.Nodes(p) {
			m[n.Path] = n
		}
		return m
	}
	ia, ib := index(a), index(b)
	var out []irDiff
	for path, na := range ia {
		nb, ok := ib[path]
		if !ok {
			out = append(out, irDiff{path, "<node>"})
			continue
		}
		for _, f := range nodeFields(na, nb) {
			out = append(out, irDiff{path, f})
		}
	}
	for path := range ib {
		if _, ok := ia[path]; !ok {
			out = append(out, irDiff{path, "<node>"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path+out[i].Field < out[j].Path+out[j].Field })
	return out
}

func nodeFields(a, b ir.NodeRef) []string {
	switch {
	case a.Pipeline != nil && b.Pipeline != nil:
		x, y := *a.Pipeline, *b.Pipeline
		x.Jobs, x.Finally, y.Jobs, y.Finally = nil, nil, nil, nil
		return structDiff(x, y)
	case a.Job != nil && b.Job != nil:
		x, y := *a.Job, *b.Job
		x.Steps, y.Steps = nil, nil
		return structDiff(x, y)
	case a.Step != nil && b.Step != nil:
		return structDiff(*a.Step, *b.Step)
	}
	return []string{"<node>"}
}

func stripBookkeeping(v reflect.Value) reflect.Value {
	ext, ok := v.Interface().(ir.Ext)
	if !ok {
		return v
	}
	out := ir.Ext{}
	for prov, raw := range ext {
		m, isMap := raw.(map[string]any)
		if !isMap {
			out[prov] = raw
			continue
		}
		kept := map[string]any{}
		for k, val := range m {
			if !emitkit.BookkeepingExt[k] && !strings.HasPrefix(k, "_") {
				kept[k] = val
			}
		}
		if len(kept) > 0 {
			out[prov] = kept
		}
	}
	return reflect.ValueOf(out)
}

func structDiff(a, b any) []string {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	var out []string
	for i := range va.NumField() {
		if !va.Type().Field(i).IsExported() {
			continue
		}
		if !cmp.Equal(stripBookkeeping(va.Field(i)).Interface(), stripBookkeeping(vb.Field(i)).Interface(), cmpopts.EquateEmpty()) {
			out = append(out, va.Type().Field(i).Name)
		}
	}
	return out
}

// atOrBelow is true when entry is the node at path or a node under it, such as a trigger of a pipeline.
func atOrBelow(entry, path string) bool { return entry == path || strings.HasPrefix(entry, path+"/") }

// covered reports whether a lost or external entry at the node, or below it, names a feature that
// accounts for the differing field.
func covered(d irDiff, reps ...*report.Report) bool {
	accepted := fieldFeatures[d.Field]
	for _, r := range reps {
		for _, e := range r.Entries {
			if e.Level >= caps.External && atOrBelow(e.IRPath, d.Path) && slices.Contains(accepted, e.Feature) {
				return true
			}
		}
	}
	return false
}

func TestRoundTripWithoutMetaBestEffort(t *testing.T) {
	for _, f := range allFixtures(t) {
		t.Run(f.provider+"/"+f.name, func(t *testing.T) {
			a, b := prov(t, f.provider), prov(t, other(f.provider))
			r := hops(t, os.DirFS(f.dir), a, b, false, false)
			if same, _ := ir.Equal(r.src, r.back); same {
				return
			}
			if len(r.src.Pipelines) == 1 && len(r.back.Pipelines) == 1 {
				// The pipeline id is the file stem on GHA and a fixed name on GitLab, so it is no loss.
				r.back.Pipelines[0].ID = r.src.Pipelines[0].ID
			}
			for _, d := range diffNodes(r.src, r.back) {
				if !covered(d, r.srcRep, r.toRep, r.backRep) {
					t.Errorf("%s: %s differs after A->B->A with no metadata and no lost or external report entry covers it", d.Path, d.Field)
				}
			}
		})
	}
}

// TestCoverageRejectsUnreportedDifference injects a difference and checks the helper finds it and refuses
// loose cover: no entry, an entry at an ancestor, and an entry with the wrong feature.
func TestCoverageRejectsUnreportedDifference(t *testing.T) {
	src, _, err := prov(t, "gha").Parse(os.DirFS(filepath.Join(featuresRoot, "gha", "features", "job.timeout")))
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := prov(t, "gha").Parse(os.DirFS(filepath.Join(featuresRoot, "gha", "features", "job.timeout")))
	if err != nil {
		t.Fatal(err)
	}
	back.Pipelines[0].Jobs[0].Timeout = nil
	diffs := diffNodes(src, back)
	if len(diffs) != 1 || diffs[0].Field != "Timeout" {
		t.Fatalf("diffs = %+v, want one Timeout difference", diffs)
	}
	job := diffs[0].Path
	entry := func(path string, f caps.Feature, l caps.Level) *report.Report {
		return &report.Report{Entries: []report.Entry{{IRPath: path, Feature: f, Level: l}}}
	}
	for name, rep := range map[string]*report.Report{
		"no entry":         {},
		"ancestor entry":   entry("pipeline/ci", caps.JobTimeout, caps.Lost),
		"wrong feature":    entry(job, caps.JobRetry, caps.Lost),
		"emulated only":    entry(job, caps.JobTimeout, caps.Emulated),
		"sibling job path": entry(job+"x", caps.JobTimeout, caps.Lost),
	} {
		if covered(diffs[0], rep) {
			t.Errorf("%s must not cover the difference", name)
		}
	}
	if !covered(diffs[0], entry(job, caps.JobTimeout, caps.Lost)) {
		t.Error("an exact lost entry must cover the difference")
	}
}
