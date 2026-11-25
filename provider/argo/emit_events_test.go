package argo_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestSensorDestIndexesMatchArgumentOrder(t *testing.T) {
	j := scriptJob("build", "echo ${{ branch }} ${{ sha }} ${{ repo }} ${{ inputs.ver }}")
	p := project(j)
	p.Pipelines[0].Inputs = []ir.Input{{Name: "ver", Default: "1"}}
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerPR}}
	files, _ := emit(t, p)
	args := list(get(template(t, files), "spec", "arguments", "parameters"))
	order := map[string]int{}
	for i, a := range args {
		order[a.(doc)["name"].(string)] = i
	}
	if order["ver"] != 0 {
		t.Fatalf("argument order = %v", order)
	}
	sensor := docs(t, files[".argo/events/sensor.yaml"])[0]
	n := 0
	for _, tr := range list(get(sensor, "spec", "triggers")) {
		params := list(get(tr, "template", "k8s", "parameters"))
		if len(params) == 0 {
			t.Fatalf("trigger without parameters: %v", tr)
		}
		for _, pm := range params {
			dest := pm.(doc)["dest"].(string)
			idx, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(dest, "spec.arguments.parameters."), ".value"))
			if err != nil {
				t.Fatalf("dest %q", dest)
			}
			want := "ctx-" + ctxOfDest(t, args, idx)
			if want == "ctx-" {
				t.Fatalf("dest %q points at no argument", dest)
			}
			n++
		}
		wf := get(tr, "template", "k8s", "source", "resource", "spec", "arguments", "parameters")
		if len(list(wf)) != len(args) {
			t.Errorf("the Workflow arguments %v do not list the %d template arguments", wf, len(args))
		}
	}
	if n < 6 {
		t.Errorf("only %d parameters were bound", n)
	}
}

// ctxOfDest returns the context name behind the argument that dest indexes, and checks the Sensor source is for that name.
func ctxOfDest(t *testing.T, args []any, idx int) string {
	t.Helper()
	if idx >= len(args) {
		t.Fatalf("dest index %d is out of range", idx)
	}
	name := args[idx].(doc)["name"].(string)
	if !strings.HasPrefix(name, "ctx-") {
		t.Fatalf("dest index %d points at %q, which is no context argument", idx, name)
	}
	return strings.TrimPrefix(name, "ctx-")
}

func TestSensorParametersCarryTheEventData(t *testing.T) {
	p := project(scriptJob("build", "echo ${{ branch }} ${{ sha }} ${{ pr.number }}"))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerPR}}
	files, _ := emit(t, p)
	s := files[".argo/events/sensor.yaml"]
	for _, want := range []string{"dataKey: body.after", "dataKey: body.pull_request.head.sha", "dataKey: body.number", `trimPrefix "refs/heads/" .Input.body.ref`} {
		if !strings.Contains(s, want) {
			t.Errorf("sensor lacks %q:\n%s", want, s)
		}
	}
}

func TestEventSourceAndSensorParseBackToTheSameTriggers(t *testing.T) {
	p := project(scriptJob("build", "echo ${{ branch }}"))
	p.Pipelines[0].Triggers = []ir.Trigger{
		{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}}},
		{Kind: ir.TriggerPR, Branches: ir.Filter{Include: []string{"main"}}},
		{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v1.*"}}},
	}
	files, _ := emit(t, p)
	back := roundTrip(t, files)
	stripExt(back)
	ir.Normalize(p)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Fatalf("%s\n%s", diff, joinFiles(files))
	}
	if res := get(map[string]any{"e": back.Pipelines[0].Ext}, "e"); res != nil {
		t.Logf("pipeline Ext after the trip: %v", res)
	}
}

func TestEditedSensorFilterFailsSafe(t *testing.T) {
	p := project(scriptJob("build", "x"))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}}}}
	files, _ := emit(t, p)
	for n, s := range files {
		files[n] = strings.Replace(s, "type: string", "type: number", 1)
	}
	back := roundTrip(t, files)
	if k := back.Pipelines[0].Triggers[0].Kind; k != ir.TriggerAPI {
		t.Errorf("a filter the parser cannot read became a %s trigger, want api", k)
	}
}

func TestEditedRBACIsKeptAndMarkerIgnored(t *testing.T) {
	p := project(scriptJob("build", "x"))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}}
	files, _ := emit(t, p)
	files[".argo/rbac/workflow.yaml"] = strings.Replace(files[".argo/rbac/workflow.yaml"], "workflowtaskresults", "pods", 1)
	back, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joinExtP(back), "pods") {
		t.Error("the edited document is lost")
	}
	if len(entries(rep, "marker ignored")) == 0 {
		t.Error("no marker ignored entry")
	}
}

func TestCronParseBack(t *testing.T) {
	p := project(scriptJob("build", "x"))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerSchedule, Cron: "0 1 * * *"}}
	files, _ := emit(t, p)
	cron := files[".argo/cron/ci-1.yaml"]
	for _, want := range []string{"schedules:", "workflowTemplateRef:", "name: ci"} {
		if !strings.Contains(cron, want) {
			t.Errorf("cron lacks %q:\n%s", want, cron)
		}
	}
	if strings.Contains(cron, "\n  schedule:") {
		t.Errorf("the cron holds the schedule key:\n%s", cron)
	}
}

func TestTagTriggerSetsTheEventOfTheSourceDialect(t *testing.T) {
	for origin, want := range map[string]string{"gha": "value: push", "woodpecker": "value: tag", "gitlab": "value: tag"} {
		t.Run(origin, func(t *testing.T) {
			p := project(scriptJob("build", "echo ${{ event }}"))
			p.Origin = ir.Origin{Provider: origin, Dialect: origin}
			p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v1.*"}}}}
			files, _ := emit(t, p)
			s := files[".argo/events/sensor.yaml"]
			if !strings.Contains(s, want) {
				t.Fatalf("sensor lacks %q:\n%s", want, s)
			}
		})
	}
}

func TestPushBesideATagTriggerAdmitsBranchRefsOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		triggers []ir.Trigger
		want     bool
	}{
		"push and tag": {[]ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag}}, true},
		"push alone":   {[]ir.Trigger{{Kind: ir.TriggerPush}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			p := project(scriptJob("build", "x"))
			p.Pipelines[0].Triggers = tc.triggers
			files, _ := emit(t, p)
			if got := strings.Contains(files[".argo/events/sensor.yaml"], "^refs/heads/.*$"); got != tc.want {
				t.Fatalf("branch ref filter = %v, want %v:\n%s", got, tc.want, files[".argo/events/sensor.yaml"])
			}
			back := roundTrip(t, files)
			stripExt(back)
			ir.Normalize(p)
			if ok, diff := ir.Equal(p, back); !ok {
				t.Fatalf("round trip differs:\n%s", diff)
			}
		})
	}
}
