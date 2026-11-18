package meta

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// setNonZero makes v a non-zero value of its type.
func setNonZero(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(1)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		if v.Elem().Kind() == reflect.Struct && v.Elem().IsZero() {
			setNonZero(v.Elem())
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		setNonZero(v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() && v.Field(i).Kind() != reflect.Interface {
				setNonZero(v.Field(i))
				return
			}
		}
	case reflect.Interface:
		v.Set(reflect.ValueOf(expr.Lit{Kind: expr.LitStr, Str: "x"}))
	}
}

func node(k kind) (ir.NodeRef, *ir.Project) {
	pl := &ir.Pipeline{ID: "p"}
	j := &ir.Job{ID: "j"}
	st := &ir.Step{ID: "s", Kind: ir.StepAction}
	j.Steps = []*ir.Step{st}
	pl.Jobs = []*ir.Job{j}
	p := &ir.Project{Pipelines: []*ir.Pipeline{pl}}
	for _, n := range ir.Nodes(p) {
		if kindOf(n) == k {
			return n, p
		}
	}
	panic("no node")
}

// TestEveryFeatureIsRecordedOrListed fails when a feature a node kind can carry is neither stored nor named
// in the unrecorded list, and checks each stored feature restores its fields.
func TestEveryFeatureIsRecordedOrListed(t *testing.T) {
	for _, k := range []kind{kindPipeline, kindJob, kindStep} {
		for _, f := range caps.AllFeatures() {
			fields, isRecorded := recorded[k][f]
			_, isListed := unrecorded[k][f]
			switch {
			case notApplicable(k, f):
				if isRecorded || isListed {
					t.Errorf("kind %d feature %s is not applicable but has an entry", k, f)
				}
			case !isRecorded && !isListed:
				t.Errorf("kind %d feature %s is neither recorded nor listed as unrecorded", k, f)
			case isRecorded && isListed:
				t.Errorf("kind %d feature %s is both recorded and unrecorded", k, f)
			case isRecorded:
				checkRestores(t, k, f, fields)
			}
		}
	}
}

func checkRestores(t *testing.T, k kind, f caps.Feature, fields []string) {
	t.Helper()
	n, p := node(k)
	var target reflect.Value
	switch {
	case n.Pipeline != nil:
		target = reflect.ValueOf(n.Pipeline).Elem()
	case n.Job != nil:
		target = reflect.ValueOf(n.Job).Elem()
	default:
		target = reflect.ValueOf(n.Step).Elem()
	}
	for _, name := range fields {
		if name == recordViaExt {
			continue
		}
		fv := target.FieldByName(name)
		if !fv.IsValid() {
			t.Fatalf("feature %s lists field %s that kind %d lacks", f, name, k)
		}
		setNonZero(fv)
	}
	lost, skipped := CollectLost(p, []report.Entry{{IRPath: n.Path, Feature: f, Level: caps.Lost}})
	if len(skipped) != 0 {
		t.Fatalf("feature %s skipped: %+v", f, skipped)
	}
	if len(fields) == 1 && fields[0] == recordViaExt {
		return
	}
	l, ok := lost[n.Path]
	if !ok {
		t.Fatalf("feature %s left no Lost record", f)
	}
	var rec reflect.Value
	switch {
	case n.Pipeline != nil:
		rec = reflect.ValueOf(l.Pipeline).Elem()
	case n.Job != nil:
		rec = reflect.ValueOf(l.Job).Elem()
	default:
		rec = reflect.ValueOf(l.Step).Elem()
	}
	for _, name := range fields {
		if name != recordViaExt && rec.FieldByName(name).IsZero() {
			t.Errorf("feature %s did not record field %s", f, name)
		}
	}
}

func TestUnknownFeatureIsReportedNotDropped(t *testing.T) {
	n, p := node(kindJob)
	_, skipped := CollectLost(p, []report.Entry{{IRPath: n.Path, Feature: "made.up", Level: caps.Lost}})
	if len(skipped) != 1 || skipped[0].Feature != "made.up" {
		t.Errorf("skipped = %+v", skipped)
	}
	_, skipped = CollectLost(p, []report.Entry{{IRPath: "pipeline/p/job/none", Feature: caps.JobIf, Level: caps.Lost}})
	if len(skipped) != 1 {
		t.Errorf("a path with no node must be reported: %+v", skipped)
	}
}
