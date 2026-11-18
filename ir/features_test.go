package ir_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/ir/irtest"
	"pgregory.net/rapid"
)

// notCarried are vocabulary features no IR field carries; the ledger derives them from script text.
var notCarried = map[caps.Feature]bool{caps.ScriptInterpolation: true}

// implied lists parent features a child feature always drags in.
var implied = map[caps.Feature]caps.Feature{
	caps.JobNeedsArtifacts:      caps.JobNeeds,
	caps.JobEnvironmentApproval: caps.JobEnvironment,
}

func TestFeaturesCoversEveryField(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		want := map[caps.Feature]bool{}
		for _, f := range caps.AllFeatures() {
			if notCarried[f] {
				continue
			}
			want[f] = rapid.Bool().Draw(t, string(f))
		}
		for child, parent := range implied {
			if want[child] {
				want[parent] = true
			}
		}
		p := irtest.GenProject(t, func(f caps.Feature) bool { return want[f] })
		got := map[caps.Feature]bool{}
		for _, u := range ir.Features(p) {
			got[u.Feature] = true
		}
		// expr.opaque is random per slot, so it is checked against what was generated, not the fragment.
		opaque := hasOpaque(reflect.ValueOf(p))
		if opaque && !want[caps.ExprOpaque] {
			t.Fatal("Opaque generated although the fragment disallows expr.opaque")
		}
		if got[caps.ExprOpaque] != opaque {
			t.Fatalf("expr.opaque reported=%v but generated=%v", got[caps.ExprOpaque], opaque)
		}
		for f, w := range want {
			if f == caps.ExprOpaque {
				continue
			}
			if w && !got[f] {
				t.Fatalf("feature %s generated but not reported", f)
			}
			if !w && got[f] {
				t.Fatalf("feature %s reported but not generated", f)
			}
		}
	})
}

var structural = map[string]map[string]bool{
	"Pipeline": {"ID": true, "Name": true, "Jobs": true, "Triggers": true},
	"Job":      {"ID": true, "Name": true, "Steps": true},
	"Step":     {"ID": true, "Name": true, "Kind": true},
	"Trigger":  {"Kind": true},
}

func fill(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64, reflect.Uint8:
		v.Set(reflect.ValueOf(1).Convert(v.Type()))
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(k)
		fill(e)
		v.SetMapIndex(k, e)
	case reflect.Interface:
		v.Set(reflect.ValueOf(expr.Opaque{Dialect: "d", Text: "t"}))
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fill(v.Field(i))
		}
	}
}

func TestFeaturesEveryIRFieldIsMapped(t *testing.T) {
	cases := map[string]struct {
		wrap func(any) *ir.Project
		node any
		path string
	}{
		"Pipeline": {
			node: &ir.Pipeline{ID: "p"}, path: "pipeline/p",
			wrap: func(n any) *ir.Project { return &ir.Project{Pipelines: []*ir.Pipeline{n.(*ir.Pipeline)}} },
		},
		"Job": {
			node: &ir.Job{ID: "j"}, path: "pipeline/p/job/j",
			wrap: func(n any) *ir.Project {
				return &ir.Project{Pipelines: []*ir.Pipeline{{ID: "p", Jobs: []*ir.Job{n.(*ir.Job)}}}}
			},
		},
		"Step": {
			node: &ir.Step{ID: "s"}, path: "pipeline/p/job/j/step/s",
			wrap: func(n any) *ir.Project {
				return &ir.Project{Pipelines: []*ir.Pipeline{{ID: "p", Jobs: []*ir.Job{{ID: "j", Steps: []*ir.Step{n.(*ir.Step)}}}}}}
			},
		},
		"Trigger": {
			node: &ir.Trigger{}, path: "pipeline/p/trigger/0",
			wrap: func(n any) *ir.Project {
				return &ir.Project{Pipelines: []*ir.Pipeline{{ID: "p", Triggers: []ir.Trigger{*n.(*ir.Trigger)}}}}
			},
		},
	}
	for name, c := range cases {
		typ := reflect.TypeOf(c.node).Elem()
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if structural[name][f.Name] {
				continue
			}
			t.Run(name+"."+f.Name, func(t *testing.T) {
				node := reflect.New(typ)
				if id := node.Elem().FieldByName("ID"); id.IsValid() {
					id.SetString(reflect.ValueOf(c.node).Elem().FieldByName("ID").String())
				}
				fill(node.Elem().Field(i))
				var hit []caps.Feature
				for _, u := range ir.Features(c.wrap(node.Interface())) {
					if u.IRPath == c.path {
						hit = append(hit, u.Feature)
					}
				}
				if len(hit) == 0 {
					t.Fatalf("field %s.%s carries data but reports no feature at %s", name, f.Name, c.path)
				}
			})
		}
	}
}

func TestFeaturesTriggerKinds(t *testing.T) {
	want := map[ir.TriggerKind]caps.Feature{
		ir.TriggerPush: caps.TriggerPush, ir.TriggerPR: caps.TriggerPullRequest, ir.TriggerTag: caps.TriggerTag,
		ir.TriggerSchedule: caps.TriggerSchedule, ir.TriggerManual: caps.TriggerManual, ir.TriggerAPI: caps.TriggerAPI,
		ir.TriggerPipelineDone: caps.TriggerPipelineDone, ir.TriggerResource: caps.TriggerResource,
	}
	for k, f := range want {
		p := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "p", Triggers: []ir.Trigger{{Kind: k}}}}}
		got := ir.Features(p)
		if len(got) != 1 || got[0].Feature != f || got[0].IRPath != "pipeline/p/trigger/0" {
			t.Errorf("%s: got %+v", k, got)
		}
	}
}

func TestFeaturesPathsAndOneEntryPerPair(t *testing.T) {
	p := &ir.Project{Pipelines: []*ir.Pipeline{{
		ID:  "ci",
		Ext: ir.Ext{"gha": 1},
		Triggers: []ir.Trigger{{Kind: ir.TriggerResource, Resource: &ir.ResourceRef{Name: "r"},
			Branches: ir.Filter{Exclude: []string{"x"}}}},
		Jobs: []*ir.Job{{ID: "b", Steps: []*ir.Step{{ID: "s", Image: &ir.Image{Ref: "alpine"}, Ext: ir.Ext{"a": 1}}}}},
	}}}
	var got []string
	for _, u := range ir.Features(p) {
		got = append(got, u.IRPath+" "+string(u.Feature))
	}
	sort.Strings(got)
	want := []string{
		"pipeline/ci ext.passthrough",
		"pipeline/ci/job/b/step/s ext.passthrough",
		"pipeline/ci/job/b/step/s step.image",
		"pipeline/ci/trigger/0 trigger.filter.branches",
		"pipeline/ci/trigger/0 trigger.resource",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%v\nwant\n%v", got, want)
	}
}

func TestFeaturesOneEntryPerPathFeaturePair(t *testing.T) {
	op := ir.Value{Kind: ir.ValueExpr, Expr: expr.Opaque{Dialect: "gha", Text: "x"}}
	p := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "p", Env: []ir.Var{{Name: "A", Value: op}}, Jobs: []*ir.Job{
		{ID: "a"},
		{
			ID: "b", If: expr.Opaque{Dialect: "gha", Text: "y"},
			Needs: []ir.Need{{JobID: "a", Artifacts: true}, {JobID: "a", Artifacts: true}},
			Steps: []*ir.Step{{ID: "s", Env: []ir.Var{{Name: "X", Value: op}, {Name: "Y", Value: op}}, With: map[string]ir.Value{"k": op, "l": op}}},
		},
	}}}}
	counts := map[ir.FeatureUse]int{}
	for _, u := range ir.Features(p) {
		counts[u]++
	}
	want := []ir.FeatureUse{
		{IRPath: "pipeline/p", Feature: caps.PipelineEnv},
		{IRPath: "pipeline/p", Feature: caps.ExprOpaque},
		{IRPath: "pipeline/p/job/b", Feature: caps.JobNeeds},
		{IRPath: "pipeline/p/job/b", Feature: caps.JobNeedsArtifacts},
		{IRPath: "pipeline/p/job/b", Feature: caps.JobIf},
		{IRPath: "pipeline/p/job/b", Feature: caps.ExprOpaque},
		{IRPath: "pipeline/p/job/b/step/s", Feature: caps.StepEnv},
		{IRPath: "pipeline/p/job/b/step/s", Feature: caps.StepAction},
		{IRPath: "pipeline/p/job/b/step/s", Feature: caps.ExprOpaque},
	}
	if len(counts) != len(want) {
		t.Fatalf("got %v, want %d distinct entries", counts, len(want))
	}
	for _, w := range want {
		if counts[w] != 1 {
			t.Errorf("%+v reported %d times", w, counts[w])
		}
	}
}

// TestGeneratorMixesOpaqueAndStructured checks that a fragment allowing both if-features and expr.opaque
// reaches job.if and step.if with structured ASTs as well as Opaque nodes.
func TestGeneratorMixesOpaqueAndStructured(t *testing.T) {
	var structJobIf, opaqueJobIf, structStepIf, opaqueStepIf int
	rapid.Check(t, func(t *rapid.T) {
		p := irtest.GenProject(t, func(f caps.Feature) bool { return f == caps.JobIf || f == caps.StepIf || f == caps.ExprOpaque })
		for _, pl := range p.Pipelines {
			for _, j := range pl.Jobs {
				if _, ok := j.If.(expr.Opaque); ok {
					opaqueJobIf++
				} else if j.If != nil {
					structJobIf++
				}
				for _, s := range j.Steps {
					if _, ok := s.If.(expr.Opaque); ok {
						opaqueStepIf++
					} else if s.If != nil {
						structStepIf++
					}
				}
			}
		}
	})
	if structJobIf == 0 || opaqueJobIf == 0 || structStepIf == 0 || opaqueStepIf == 0 {
		t.Fatalf("want both kinds in both slots: job.if struct=%d opaque=%d, step.if struct=%d opaque=%d",
			structJobIf, opaqueJobIf, structStepIf, opaqueStepIf)
	}
}

func hasOpaque(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		return !v.IsNil() && hasOpaque(v.Elem())
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if hasOpaque(v.Index(i)) {
				return true
			}
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			if hasOpaque(v.MapIndex(k)) {
				return true
			}
		}
	case reflect.Struct:
		if _, ok := v.Interface().(expr.Opaque); ok {
			return true
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() && hasOpaque(v.Field(i)) {
				return true
			}
		}
	}
	return false
}
