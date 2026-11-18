package roundtrip_test

import (
	"os"
	"reflect"
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

// fieldFeatures names the feature prefixes whose lost or external entry accounts for a differing field.
// A field absent from the table has no feature of its own, so only the path decides.
var fieldFeatures = map[string][]string{
	"Triggers":     {"trigger.", "pipeline.inputs", "ext.passthrough"},
	"Env":          {"pipeline.env", "job.env", "step.env", "script."},
	"Concurrency":  {"pipeline.concurrency"},
	"Inputs":       {"pipeline.inputs"},
	"Permissions":  {"pipeline."},
	"Runner":       {"job.runner"},
	"Container":    {"job.container"},
	"Services":     {"job.services"},
	"Matrix":       {"job.matrix"},
	"Timeout":      {"job.timeout", "step.timeout"},
	"Retry":        {"job.retry"},
	"AllowFailure": {"job.allow_failure"},
	"Environment":  {"job.environment"},
	"Manual":       {"job.manual"},
	"Cache":        {"job.cache"},
	"Artifacts":    {"job.artifacts"},
	"Outputs":      {"job.outputs"},
	"Needs":        {"job.needs"},
	"If":           {"job.if", "step.if", "expr.opaque"},
	"Script":       {"step.script", "script.", "step.action", "ext.passthrough"},
	"Action":       {"step.action", "ext.passthrough"},
	"With":         {"step.action", "ext.passthrough"},
	"Shell":        {"step.shell"},
	"WorkDir":      {"step.workdir"},
	"Image":        {"step.image"},
	"Kind":         {"step.action", "ext.passthrough"},
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

// displayOnly lists fields with no behaviour and no feature in the capability matrix: the display names a
// target without a name key cannot hold. Round-trip metadata restores them; without it they are not reported.
var displayOnly = map[string]bool{"Name": true}

func structDiff(a, b any) []string {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	var out []string
	for i := range va.NumField() {
		if !va.Type().Field(i).IsExported() || displayOnly[va.Type().Field(i).Name] {
			continue
		}
		if !cmp.Equal(stripBookkeeping(va.Field(i)).Interface(), stripBookkeeping(vb.Field(i)).Interface(), cmpopts.EquateEmpty()) {
			out = append(out, va.Type().Field(i).Name)
		}
	}
	return out
}

func related(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// covered reports whether a lost or external entry accounts for d.
func covered(d irDiff, reps ...*report.Report) bool {
	for _, r := range reps {
		for _, e := range r.Entries {
			if e.Level < caps.External || !related(e.IRPath, d.Path) {
				continue
			}
			prefixes, known := fieldFeatures[d.Field]
			if !known {
				return true
			}
			for _, p := range prefixes {
				if strings.HasPrefix(string(e.Feature), p) {
					return true
				}
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
