package argo_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const ciTemplate = `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata: {name: ci}
spec:
  entrypoint: main
  templates:
  - {name: main, container: {image: x}}
`

func cron(spec string) string {
	return "apiVersion: argoproj.io/v1alpha1\nkind: CronWorkflow\nmetadata: {name: nightly, labels: {team: a}}\nspec:\n" + spec
}

func TestCronWorkflowIsAScheduleTrigger(t *testing.T) {
	p, rep := parseFiles(t, map[string]string{"t.yaml": ciTemplate, "c.yaml": cron(`  schedules: ["0 2 * * *", "0 14 * * 1"]
  timezone: Europe/London
  concurrencyPolicy: Forbid
  workflowSpec: {workflowTemplateRef: {name: ci}}
`)})
	trs := p.Pipelines[0].Triggers
	if len(trs) != 2 || trs[0].Kind != ir.TriggerSchedule || trs[0].Cron != "0 14 * * 1" || trs[1].Cron != "0 2 * * *" {
		t.Fatalf("triggers %+v", trs)
	}
	e := ext(trs[0].Ext)
	cw, _ := e["cronWorkflow"].(map[string]any)
	if cw["name"] != "nightly" || cw["timezone"] != "Europe/London" || cw["concurrencyPolicy"] != "Forbid" || cw["metadata"] == nil {
		t.Fatalf("cron data %v", e)
	}
	if !hasEntry(rep, "timezone") || !hasEntry(rep, "concurrencyPolicy") {
		t.Fatalf("entries %+v", rep.Entries)
	}
}

func TestLegacyScheduleKey(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"t.yaml": ciTemplate, "c.yaml": cron("  schedule: \"*/5 * * * *\"\n  workflowSpec: {workflowTemplateRef: {name: ci}}\n")})
	if trs := p.Pipelines[0].Triggers; len(trs) != 1 || trs[0].Cron != "*/5 * * * *" {
		t.Fatalf("triggers %+v", trs)
	}
}

func TestSuspendedCronNeverFires(t *testing.T) {
	p, rep := parseFiles(t, map[string]string{"t.yaml": ciTemplate, "c.yaml": cron("  schedules: [\"0 2 * * *\"]\n  suspend: true\n  workflowSpec: {workflowTemplateRef: {name: ci}}\n")})
	trs := p.Pipelines[0].Triggers
	if len(trs) != 1 || trs[0].Kind != ir.TriggerAPI || !hasEntry(rep, "suspended") {
		t.Fatalf("triggers %+v %+v", trs, rep.Entries)
	}
}

func TestCronWorkflowWithInlineSpecIsAPipeline(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"c.yaml": cron("  schedules: [\"0 2 * * *\"]\n  workflowSpec:\n    entrypoint: main\n    templates:\n    - {name: main, container: {image: x}}\n")})
	if len(p.Pipelines) != 1 || p.Pipelines[0].ID != "nightly" || len(p.Pipelines[0].Triggers) != 1 {
		t.Fatalf("pipelines %v", p.Pipelines)
	}
}

func TestCronOfAnUnknownTemplateIsKept(t *testing.T) {
	p, rep := parseFiles(t, map[string]string{"t.yaml": ciTemplate, "c.yaml": cron("  schedules: [\"0 2 * * *\"]\n  workflowSpec: {workflowTemplateRef: {name: nope}}\n")})
	res, _ := ext(p.Pipelines[0].Ext)["resources"].([]any)
	if len(res) != 1 || !hasEntry(rep, "kind: CronWorkflow has no IR form") {
		t.Fatalf("resources %v %+v", res, rep.Entries)
	}
}

const eventSource = `apiVersion: argoproj.io/v1alpha1
kind: EventSource
metadata: {name: gh, labels: {app: events}}
spec:
  service: {ports: [{port: 12000, targetPort: 12000}]}
  github:
    repo:
      repositories: [{owner: acme, names: [app]}]
      webhook: {endpoint: /push, port: "12000", method: POST, url: "https://hooks.example.com"}
      events: ["push", "pull_request"]
      apiToken: {name: gh-token, key: token}
      webhookSecret: {name: gh-secret, key: secret}
      contentType: json
`

func sensor(deps, triggers string) string {
	return "apiVersion: argoproj.io/v1alpha1\nkind: Sensor\nmetadata: {name: ci-sensor, namespace: events}\nspec:\n  template: {serviceAccountName: operate-workflow-sa}\n  dependencies:\n" + deps + "  triggers:\n" + triggers
}

const runCI = `  - template:
      name: run-ci
      k8s:
        operation: create
        source:
          resource:
            apiVersion: argoproj.io/v1alpha1
            kind: Workflow
            metadata: {generateName: ci-}
            spec:
              workflowTemplateRef: {name: ci}
              arguments: {parameters: [{name: ctx-branch}]}
        parameters:
        - src: {dependencyName: gh-dep, dataKey: body.ref}
          dest: spec.arguments.parameters.0.value
`

func dep(filters string) string {
	return "  - name: gh-dep\n    eventSourceName: gh\n    eventName: repo\n    filters:\n      data:\n" + filters
}

func triggersOf(t *testing.T, files map[string]string) ([]ir.Trigger, string) {
	t.Helper()
	files["t.yaml"] = ciTemplate
	p, rep := parseFiles(t, files)
	var b strings.Builder
	for _, e := range rep.Entries {
		b.WriteString(e.Detail + "\n")
	}
	return p.Pipelines[0].Triggers, b.String()
}

func TestSensorPushWithBranchFilter(t *testing.T) {
	trs, _ := triggersOf(t, map[string]string{"es.yaml": eventSource, "s.yaml": sensor(dep(`      - {path: headers.X-Github-Event, type: string, value: ["push"]}
      - {path: body.ref, type: string, value: ["refs/heads/main", "refs/heads/release-.*"]}
`), runCI)})
	if len(trs) != 1 || trs[0].Kind != ir.TriggerPush {
		t.Fatalf("triggers %+v", trs)
	}
	if !reflect.DeepEqual(trs[0].Branches.Include, []string{"main", "release-*"}) || len(trs[0].Tags.Include) != 0 {
		t.Fatalf("filters %+v", trs[0])
	}
	e := ext(trs[0].Ext)
	sn, _ := e["sensor"].(map[string]any)
	es, _ := e["eventSource"].(map[string]any)
	if sn["name"] != "ci-sensor" || sn["metadata"] == nil || e["trigger"] == nil || e["dependency"] == nil {
		t.Fatalf("sensor data %v", e)
	}
	if es["name"] != "gh" || es["metadata"] == nil || es["entry"] == nil || es["spec"] == nil {
		t.Fatalf("eventSource data %v", es)
	}
}

func TestSensorTagAndPullRequest(t *testing.T) {
	trs, _ := triggersOf(t, map[string]string{"es.yaml": eventSource, "s.yaml": sensor(dep(`      - {path: headers.X-Github-Event, type: string, value: ["push"]}
      - {path: body.ref, type: string, value: ["refs/tags/v.*"]}
`), runCI)})
	if len(trs) != 1 || trs[0].Kind != ir.TriggerTag || !reflect.DeepEqual(trs[0].Tags.Include, []string{"v*"}) {
		t.Fatalf("tag triggers %+v", trs)
	}
	trs, _ = triggersOf(t, map[string]string{"es.yaml": eventSource, "s.yaml": sensor(dep(`      - {path: headers.X-Github-Event, type: string, value: ["pull_request"]}
      - {path: body.action, type: string, value: ["opened", "synchronize", "reopened"]}
      - {path: body.pull_request.base.ref, type: string, value: ["main"]}
`), runCI)})
	if len(trs) != 1 || trs[0].Kind != ir.TriggerPR || !reflect.DeepEqual(trs[0].Branches.Include, []string{"main"}) {
		t.Fatalf("pr triggers %+v", trs)
	}
}

func TestSensorWithoutFilterUsesTheEventSourceEvents(t *testing.T) {
	trs, _ := triggersOf(t, map[string]string{"es.yaml": eventSource, "s.yaml": sensor("  - {name: gh-dep, eventSourceName: gh, eventName: repo}\n", runCI)})
	if len(trs) != 2 || trs[0].Kind != ir.TriggerPR || trs[1].Kind != ir.TriggerPush {
		t.Fatalf("triggers %+v", trs)
	}
}

// A filter the parser cannot read must never widen the trigger: no push trigger without its branch filter.
func TestUnrecognisedSensorFiltersFailSafe(t *testing.T) {
	filters := map[string]string{
		"regex branch": `      - {path: body.ref, type: string, value: ["refs/heads/(main|dev)"]}
`,
		"other path": `      - {path: body.repository.private, type: bool, value: ["false"]}
`,
		"comparator": `      - {path: body.ref, type: string, value: ["refs/heads/main"], comparator: "!="}
`,
		"regex pr action": `      - {path: headers.X-Github-Event, type: string, value: ["pull_request"]}
      - {path: body.action, type: string, value: ["opened.*"]}
`,
		"or operator": `      - {path: body.ref, type: string, value: ["refs/heads/main"]}
      - {path: body.repository.fork, type: bool, value: ["false"]}
    dataLogicalOperator: or
`,
		"exprs filter": "      - {path: body.ref, type: string, value: [\"refs/heads/main\"]}\n    exprs: [{expr: \"a == b\", fields: [{name: a, path: body.x}]}]\n",
	}
	for name, f := range filters {
		t.Run(name, func(t *testing.T) {
			trs, entries := triggersOf(t, map[string]string{"es.yaml": eventSource, "s.yaml": sensor(dep(f), runCI)})
			if len(trs) != 1 || trs[0].Kind != ir.TriggerAPI {
				t.Fatalf("a filter with no IR form made %+v", trs)
			}
			if len(trs[0].Branches.Include)+len(trs[0].Tags.Include) != 0 {
				t.Fatalf("filters leaked: %+v", trs[0])
			}
			e := ext(trs[0].Ext)
			if e["dependency"] == nil || e["trigger"] == nil || !strings.Contains(entries, "api trigger") {
				t.Fatalf("filter not kept: %v / %s", e, entries)
			}
		})
	}
}

func TestSensorOfAnotherEventSourceTypeFailsSafe(t *testing.T) {
	gl := strings.Replace(eventSource, "  github:\n    repo:", "  gitlab:\n    repo:", 1)
	trs, _ := triggersOf(t, map[string]string{"es.yaml": gl, "s.yaml": sensor("  - {name: gh-dep, eventSourceName: gh, eventName: repo}\n", runCI)})
	if len(trs) != 1 || trs[0].Kind != ir.TriggerAPI {
		t.Fatalf("triggers %+v", trs)
	}
}

func TestSensorWithTwoDependenciesNeedsBoth(t *testing.T) {
	deps := "  - {name: gh-dep, eventSourceName: gh, eventName: repo}\n  - {name: other, eventSourceName: gh, eventName: repo}\n"
	trs, _ := triggersOf(t, map[string]string{"es.yaml": eventSource, "s.yaml": sensor(deps, runCI)})
	if len(trs) != 1 || trs[0].Kind != ir.TriggerAPI {
		t.Fatalf("an AND of two events made %+v", trs)
	}
	or := strings.Replace(runCI, "      name: run-ci\n", "      name: run-ci\n      conditions: \"gh-dep || other\"\n", 1)
	trs, _ = triggersOf(t, map[string]string{"es.yaml": eventSource, "s.yaml": sensor(deps, or)})
	if len(trs) != 4 {
		t.Fatalf("an OR of two push+pr deps made %d triggers", len(trs))
	}
}

func TestUnmappedSensorTriggerAndEventSourceStayInExt(t *testing.T) {
	other := strings.Replace(runCI, "workflowTemplateRef: {name: ci}", "workflowTemplateRef: {name: nope}", 1)
	both := runCI + strings.Replace(other, "run-ci", "run-other", 1)
	p, rep := parseFiles(t, map[string]string{"t.yaml": ciTemplate, "es.yaml": eventSource + "  webhook:\n    hook: {port: \"13000\", endpoint: /x, method: POST}\n",
		"s.yaml": sensor("  - {name: gh-dep, eventSourceName: gh, eventName: repo}\n", both)})
	res, _ := ext(p.Pipelines[0].Ext)["resources"].([]any)
	var sens, es string
	for _, r := range res {
		s := fmtAny(r)
		switch {
		case strings.Contains(s, "kind:Sensor"):
			sens = s
		case strings.Contains(s, "kind:EventSource"):
			es = s
		}
	}
	if !strings.Contains(sens, "run-other") || strings.Contains(sens, "run-ci") {
		t.Fatalf("unmapped trigger not kept alone: %v", res)
	}
	if !strings.Contains(es, "webhook") || strings.Contains(es, "apiToken") {
		t.Fatalf("unused event source entries not kept alone: %v", es)
	}
	if !hasEntry(rep, "kind: Sensor has no IR form") || !hasEntry(rep, "kind: EventSource has no IR form") {
		t.Fatalf("entries %+v", rep.Entries)
	}
}

func TestSensorWithNoMappedTriggerKeepsTheWholeDocuments(t *testing.T) {
	p, rep := parseFiles(t, map[string]string{"t.yaml": ciTemplate, "es.yaml": eventSource,
		"s.yaml": sensor("  - {name: gh-dep, eventSourceName: gh, eventName: repo}\n", strings.Replace(runCI, "name: ci}", "name: nope}", 1))})
	res, _ := ext(p.Pipelines[0].Ext)["resources"].([]any)
	if len(res) != 2 || len(p.Pipelines[0].Triggers) != 0 || !hasEntry(rep, "kind: Sensor") {
		t.Fatalf("resources %d triggers %v", len(res), p.Pipelines[0].Triggers)
	}
}

func TestSensorTriggerThatIsNotACreateNeverStartsThePipeline(t *testing.T) {
	for _, op := range []string{"delete", "patch", "update", "resubmit", "retry", "resume", "suspend", "terminate", "stop"} {
		t.Run(op, func(t *testing.T) {
			run := strings.Replace(runCI, "operation: create", "operation: "+op, 1)
			trs, entries := triggersOf(t, map[string]string{"es.yaml": eventSource, "s.yaml": sensor(dep("      - {path: headers.X-Github-Event, type: string, value: [\"push\"]}\n"), run)})
			if len(trs) != 0 || !strings.Contains(entries, "operation "+op) {
				t.Fatalf("triggers %+v entries %s", trs, entries)
			}
		})
	}
}

// Argo Events v1.9.11 runs an argoWorkflow trigger with no operation as submit (pkg/sensors/triggers/argo-workflow/argo-workflow.go:100-103).
func TestSensorArgoWorkflowSubmitStartsThePipeline(t *testing.T) {
	for name, run := range map[string]string{
		"submit":  strings.Replace(runCI, "k8s:\n        operation: create", "argoWorkflow:\n        operation: submit", 1),
		"default": strings.Replace(runCI, "k8s:\n        operation: create", "argoWorkflow:", 1),
	} {
		t.Run(name, func(t *testing.T) {
			trs, entries := triggersOf(t, map[string]string{"es.yaml": eventSource, "s.yaml": sensor(dep("      - {path: headers.X-Github-Event, type: string, value: [\"push\"]}\n"), run)})
			if len(trs) != 1 || trs[0].Kind != ir.TriggerPush {
				t.Fatalf("triggers %+v entries %s", trs, entries)
			}
		})
	}
}

// Argo Events v1.9.11 runs a k8s trigger with no operation as create (pkg/sensors/triggers/standard-k8s/standard-k8s.go:145-148).
func TestSensorK8sDefaultOperationStartsThePipeline(t *testing.T) {
	run := strings.Replace(runCI, "        operation: create\n", "", 1)
	trs, entries := triggersOf(t, map[string]string{"es.yaml": eventSource, "s.yaml": sensor(dep("      - {path: headers.X-Github-Event, type: string, value: [\"push\"]}\n"), run)})
	if len(trs) != 1 || trs[0].Kind != ir.TriggerPush {
		t.Fatalf("triggers %+v entries %s", trs, entries)
	}
}
