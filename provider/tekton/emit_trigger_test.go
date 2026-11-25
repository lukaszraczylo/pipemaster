package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	listenerFile = ".tekton/triggers/eventlistener.yaml"
	bindingFile  = ".tekton/triggers/triggerbinding.yaml"
	templateFile = ".tekton/triggers/triggertemplate.yaml"
)

func withTriggers(ts ...ir.Trigger) *ir.Project {
	j := scriptJob("a", "echo ${{ branch }} ${{ sha }}")
	p := project(j)
	p.Pipelines[0].Triggers = ts
	return p
}

func listenerTriggers(t *testing.T, files map[string]string) []any {
	t.Helper()
	return list(get(docs(t, files[listenerFile])[0], "spec", "triggers"))
}

func celParams(t *testing.T, tr any) doc {
	t.Helper()
	for _, i := range list(get(tr, "interceptors")) {
		if get(i, "ref", "name") == "cel" {
			return map[string]any{"filter": named(t, get(i, "params"), "filter")["value"], "overlays": named(t, get(i, "params"), "overlays")["value"]}
		}
	}
	t.Fatalf("no cel interceptor in %v", tr)
	return nil
}

func TestPushTriggerBecomesCELFilter(t *testing.T) {
	p := withTriggers(ir.Trigger{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main", "feature/**"}, Exclude: []string{"wip-*"}}})
	files, rep := emit(t, p)
	el := docs(t, files[listenerFile])[0]
	if el["kind"] != "EventListener" || get(el, "metadata", "name") != "pipemaster" {
		t.Fatalf("listener = %v", el)
	}
	trs := list(get(el, "spec", "triggers"))
	if len(trs) != 1 {
		t.Fatalf("triggers = %v", trs)
	}
	gh := list(get(trs[0], "interceptors"))[0]
	if get(gh, "ref", "name") != "github" || list(named(t, get(gh, "params"), "eventTypes")["value"])[0] != "push" {
		t.Fatalf("github interceptor = %v", gh)
	}
	cel := celParams(t, trs[0])
	want := `body.ref.startsWith('refs/heads/') && (body.ref.replace('refs/heads/','',1).matches(r'^main$') || body.ref.replace('refs/heads/','',1).matches(r'^feature/.*$')) && !(body.ref.replace('refs/heads/','',1).matches(r'^wip-[^/]*$'))`
	if cel["filter"] != want {
		t.Fatalf("filter = %v\nwant     %v", cel["filter"], want)
	}
	ov := map[string]string{}
	for _, o := range list(cel["overlays"]) {
		ov[o.(doc)["key"].(string)] = o.(doc)["expression"].(string)
	}
	if ov["ctx_branch"] != "body.ref.replace('refs/heads/','',1)" || ov["ctx_sha"] != "body.after" {
		t.Fatalf("overlays = %v", ov)
	}
	b := docs(t, files[bindingFile])[0]
	if got := named(t, get(b, "spec", "params"), "ctx-sha")["value"]; got != "$(extensions.ctx_sha)" {
		t.Fatalf("binding = %v", b)
	}
	tpl := docs(t, files[templateFile])[0]
	run := list(get(tpl, "spec", "resourcetemplates"))[0]
	if get(run, "kind") != "PipelineRun" || get(run, "spec", "pipelineRef", "name") != "ci" {
		t.Fatalf("template = %v", tpl)
	}
	if named(t, get(run, "spec", "params"), "ctx-sha")["value"] != "$(tt.params.ctx-sha)" {
		t.Fatalf("run params = %v", get(run, "spec", "params"))
	}
	wantEntry(t, rep, caps.TriggerPush, caps.External, "pipemaster-github-webhook")
	if !strings.Contains(files[listenerFile], "# pipemaster:emulated trigger.push pipeline/ci/trigger/0") ||
		!strings.Contains(files[listenerFile], "# pipemaster:arg ") {
		t.Fatalf("marker missing:\n%s", files[listenerFile])
	}
}

func TestPullRequestAndPathsFilter(t *testing.T) {
	p := withTriggers(ir.Trigger{Kind: ir.TriggerPR, Branches: ir.Filter{Include: []string{"main"}}, Paths: ir.Filter{Include: []string{"src/**"}, Exclude: []string{"**/*.md"}}})
	files, rep := emit(t, p)
	tr := listenerTriggers(t, files)[0]
	gh := get(list(get(tr, "interceptors"))[0], "params")
	if list(named(t, gh, "eventTypes")["value"])[0] != "pull_request" || get(named(t, gh, "addChangedFiles"), "value", "enabled") != true {
		t.Fatalf("github params = %v", gh)
	}
	f := celParams(t, tr)["filter"].(string)
	for _, part := range []string{
		"body.action in ['opened','synchronize','reopened']",
		"body.pull_request.base.ref.matches(r'^main$')",
		"extensions.changed_files.added + extensions.changed_files.modified + extensions.changed_files.removed",
		"(f.matches(r'^src/.*$')) && !(f.matches(r'^(.*/)?[^/]*\\.md$'))",
	} {
		if !strings.Contains(f, part) {
			t.Fatalf("filter lacks %q:\n%s", part, f)
		}
	}
	wantEntry(t, rep, caps.TriggerFilterPaths, caps.Emulated, "")
}

func TestGlobRegexSemantics(t *testing.T) {
	p := withTriggers(ir.Trigger{Kind: ir.TriggerPR, Paths: ir.Filter{Include: []string{"**/*.md", "docs/?x"}}})
	files, _ := emit(t, p)
	f := celParams(t, listenerTriggers(t, files)[0])["filter"].(string)
	for _, want := range []string{`f.matches(r'^(.*/)?[^/]*\.md$')`, `f.matches(r'^docs/[^/]x$')`} {
		if !strings.Contains(f, want) {
			t.Fatalf("filter lacks %q:\n%s", want, f)
		}
	}
}

func TestUnexpressibleGlobMakesFilterFalse(t *testing.T) {
	p := withTriggers(ir.Trigger{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"release/[0-9]*"}}})
	files, rep := emit(t, p)
	if f := celParams(t, listenerTriggers(t, files)[0])["filter"]; f != "false" {
		t.Fatalf("filter = %v", f)
	}
	wantEntry(t, rep, caps.TriggerFilterBranch, caps.Lost, "no exact CEL regex")
	back, _ := reparse(t, files)
	tr := back.Pipelines[0].Triggers[0]
	if tr.Kind != ir.TriggerPush || len(tr.Branches.Include) != 1 || tr.Branches.Include[0] != "release/[0-9]*" {
		t.Fatalf("trigger after parse = %+v", tr)
	}
}

func TestTagTriggerAndContext(t *testing.T) {
	p := withTriggers(ir.Trigger{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}}})
	p.Pipelines[0].Jobs[0].Steps[0].Script = []string{"echo ${{ tag }} ${{ ref_type }} ${{ event }}"}
	files, _ := emit(t, p)
	tr := listenerTriggers(t, files)[0]
	c := celParams(t, tr)
	if c["filter"] != `body.ref.startsWith('refs/tags/') && (body.ref.replace('refs/tags/','',1).matches(r'^v[^/]*$'))` {
		t.Fatalf("filter = %v", c["filter"])
	}
	ov := map[string]string{}
	for _, o := range list(c["overlays"]) {
		ov[o.(doc)["key"].(string)] = o.(doc)["expression"].(string)
	}
	if ov["ctx_tag"] != "body.ref.replace('refs/tags/','',1)" || ov["ctx_ref_type"] != "'tag'" || ov["ctx_event"] != "'push'" {
		t.Fatalf("overlays = %v", ov)
	}
}

func TestScheduleBecomesCronJob(t *testing.T) {
	p := withTriggers(ir.Trigger{Kind: ir.TriggerSchedule, Cron: "0 3 * * *"})
	files, rep := emit(t, p)
	cj := docs(t, files[".tekton/cron/ci-schedule-0.yaml"])[0]
	if cj["kind"] != "CronJob" || get(cj, "spec", "schedule") != "0 3 * * *" || get(cj, "spec", "timeZone") != "Etc/UTC" {
		t.Fatalf("cronjob = %v", cj)
	}
	c := list(get(cj, "spec", "jobTemplate", "spec", "template", "spec", "containers"))[0]
	if !strings.HasPrefix(get(c, "image").(string), "curlimages/curl:") {
		t.Fatalf("container = %v", c)
	}
	args := list(get(c, "args"))
	joined := ""
	for _, a := range args {
		joined += a.(string) + "\n"
	}
	for _, s := range []string{"X-Pipemaster-Schedule: ci/0", "http://el-pipemaster-schedule.default.svc.cluster.local:8080"} {
		if !strings.Contains(joined, s) {
			t.Fatalf("args lack %q: %v", s, args)
		}
	}
	f := celParams(t, listenerTriggers(t, files)[0])["filter"]
	if f != "header.canonical('X-Pipemaster-Schedule') == 'ci/0'" {
		t.Fatalf("filter = %v", f)
	}
	wantEntry(t, rep, caps.TriggerSchedule, caps.Emulated, "default")
	back, _ := reparse(t, files)
	if len(back.Pipelines[0].Triggers) != 1 || back.Pipelines[0].Triggers[0].Kind != ir.TriggerSchedule || back.Pipelines[0].Triggers[0].Cron != "0 3 * * *" {
		t.Fatalf("triggers after parse = %+v", back.Pipelines[0].Triggers)
	}
}

func TestManualTriggerIsExternal(t *testing.T) {
	files, rep := emit(t, withTriggers(ir.Trigger{Kind: ir.TriggerManual}))
	wantEntry(t, rep, caps.TriggerManual, caps.External, "tkn pipeline start ci")
	if _, ok := files[listenerFile]; ok {
		t.Fatalf("a manual trigger needs no listener: %v", fileNames(files))
	}
}

func TestTriggersParseBack(t *testing.T) {
	p := withTriggers(
		ir.Trigger{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}}},
		ir.Trigger{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}}},
		ir.Trigger{Kind: ir.TriggerPR, Paths: ir.Filter{Include: []string{"src/**"}}},
	)
	files, _ := emit(t, p)
	back, rep := reparse(t, files)
	by := map[ir.TriggerKind]ir.Trigger{}
	for _, tr := range back.Pipelines[0].Triggers {
		by[tr.Kind] = tr
	}
	if len(by) != 3 {
		t.Fatalf("triggers after parse = %+v", back.Pipelines[0].Triggers)
	}
	if by[ir.TriggerPush].Branches.Include[0] != "main" || by[ir.TriggerTag].Tags.Include[0] != "v*" || by[ir.TriggerPR].Paths.Include[0] != "src/**" {
		t.Fatalf("filters after parse = %+v", by)
	}
	if hasEntry(rep, "marker ignored") {
		t.Fatalf("unexpected marker ignored entry: %v", rep.Entries)
	}
}

func TestEditedFilterIsNotRestored(t *testing.T) {
	files, _ := emit(t, withTriggers(ir.Trigger{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}}}))
	files[listenerFile] = strings.Replace(files[listenerFile], "'^main$'", "'^develop$'", 1)
	back, rep := reparse(t, files)
	tr := back.Pipelines[0].Triggers[0]
	if tr.Kind != ir.TriggerAPI || len(tr.Branches.Include) != 0 || ext(tr.Ext)["filter"] == nil {
		t.Fatalf("edited filter was restored: %+v", tr)
	}
	if !hasEntry(rep, "marker ignored") {
		t.Fatalf("no marker ignored entry: %v", rep.Entries)
	}
}

func TestEditedOverlayIsNotRestored(t *testing.T) {
	files, _ := emit(t, withTriggers(ir.Trigger{Kind: ir.TriggerPush}))
	files[listenerFile] = strings.Replace(files[listenerFile], "body.after", "body.head_commit.id", 1)
	back, rep := reparse(t, files)
	if ext(back.Pipelines[0].Triggers[0].Ext)["filter"] == nil || !hasEntry(rep, "marker ignored") {
		t.Fatalf("edited overlay was restored: %+v %v", back.Pipelines[0].Triggers[0], rep.Entries)
	}
}

func TestTektonOriginTemplateIsNotEmittedTwice(t *testing.T) {
	src := map[string]string{
		".tekton/pipeline.yaml": "apiVersion: tekton.dev/v1\nkind: Pipeline\nmetadata:\n  name: ci\nspec:\n  tasks:\n    - name: a\n      taskSpec:\n        steps:\n          - name: s\n            image: alpine:3.22\n            script: echo hi\n",
		".tekton/triggers.yaml": "apiVersion: triggers.tekton.dev/v1beta1\nkind: TriggerTemplate\nmetadata:\n  name: ci-run\nspec:\n  resourcetemplates:\n    - apiVersion: tekton.dev/v1\n      kind: PipelineRun\n      metadata:\n        generateName: ci-\n      spec:\n        pipelineRef:\n          name: ci\n---\napiVersion: triggers.tekton.dev/v1beta1\nkind: EventListener\nmetadata:\n  name: el\nspec:\n  triggers:\n    - name: t\n      interceptors:\n        - ref:\n            name: github\n          params:\n            - name: eventTypes\n              value: [push]\n      template:\n        ref: ci-run\n",
	}
	p, _ := parseFiles(t, src)
	files, _ := emit(t, p)
	n := 0
	for _, f := range files {
		for _, d := range docs(t, f) {
			if d["kind"] == "TriggerTemplate" && get(d, "metadata", "name") == "ci-run" {
				n++
			}
		}
	}
	if n != 1 {
		t.Fatalf("TriggerTemplate ci-run is written %d times:\n%s", n, joinFiles(files))
	}
}

func TestGeneratedDocumentsCollapseOnParse(t *testing.T) {
	p := withTriggers(ir.Trigger{Kind: ir.TriggerPush}, ir.Trigger{Kind: ir.TriggerSchedule, Cron: "0 3 * * *"})
	files, _ := emit(t, p)
	back, rep := reparse(t, files)
	pl := back.Pipelines[0]
	if pl.Ext != nil {
		t.Fatalf("pipeline Ext = %v", pl.Ext)
	}
	for _, tr := range pl.Triggers {
		if tr.Ext != nil {
			t.Fatalf("trigger %s kept Ext %v", tr.Kind, tr.Ext)
		}
	}
	if hasEntry(rep, "marker ignored") {
		t.Fatalf("unexpected marker ignored: %v", rep.Entries)
	}
}

func TestEditedGeneratedDocumentIsKept(t *testing.T) {
	cases := map[string]struct{ file, from, to, kind string }{
		"binding":  {bindingFile, "$(extensions.ctx_branch)", "main", "TriggerBinding"},
		"template": {templateFile, "generateName: ci-run-", "generateName: mine-", "TriggerTemplate"},
		"rbac":     {".tekton/rbac/triggers.yaml", "- create", "- delete", "Role"},
		"cron":     {".tekton/cron/ci-schedule-0.yaml", "timeZone: Etc/UTC", "timeZone: Europe/London", "CronJob"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := withTriggers(ir.Trigger{Kind: ir.TriggerPush}, ir.Trigger{Kind: ir.TriggerSchedule, Cron: "0 3 * * *"})
			files, _ := emit(t, p)
			if !strings.Contains(files[c.file], c.from) {
				t.Fatalf("%s lacks %q:\n%s", c.file, c.from, files[c.file])
			}
			files[c.file] = strings.Replace(files[c.file], c.from, c.to, 1)
			back, rep := reparse(t, files)
			kept := joinFiles(map[string]string{"x": fmtAny(back.Pipelines[0].Ext) + fmtAny(back.Pipelines[0].Triggers)})
			if want := c.to[strings.LastIndex(c.to, " ")+1:]; !strings.Contains(kept, want) {
				t.Fatalf("edited text %q is not kept:\n%s", want, kept)
			}
			if !hasEntry(rep, "marker ignored") {
				t.Fatalf("no marker ignored entry: %v", rep.Entries)
			}
		})
	}
}

func listenerNames(t *testing.T, files map[string]string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, d := range docs(t, files[listenerFile]) {
		if d["kind"] != "EventListener" {
			continue
		}
		var entries []string
		for _, tr := range list(get(d, "spec", "triggers")) {
			entries = append(entries, get(tr, "name").(string))
		}
		out[get(d, "metadata", "name").(string)] = strings.Join(entries, ",")
	}
	return out
}

// The schedule entry has no GitHub signature, so it sits on its own listener that the webhook Ingress does not reach.
func TestScheduleEntryHasItsOwnListener(t *testing.T) {
	files, rep := emit(t, withTriggers(ir.Trigger{Kind: ir.TriggerPush}, ir.Trigger{Kind: ir.TriggerSchedule, Cron: "0 3 * * *"}))
	got := listenerNames(t, files)
	if got["pipemaster"] != "ci-push-0" || got["pipemaster-schedule"] != "ci-schedule-0" || len(got) != 2 {
		t.Fatalf("listeners = %v", got)
	}
	if cron := joinFiles(files); !strings.Contains(cron, "el-pipemaster-schedule.default.svc") || strings.Contains(cron, "el-pipemaster.default.svc") {
		t.Fatalf("the CronJob must post to the schedule listener:\n%s", cron)
	}
	e := entry(rep, caps.TriggerSchedule, "pipemaster-schedule")
	if e == nil || !strings.Contains(e.Detail, "every ctx-* param is empty") || strings.Contains(e.Detail, "any caller in the cluster") {
		t.Fatalf("schedule caveat = %+v", e)
	}
	back, brep := reparse(t, files)
	if hasEntry(brep, "marker ignored") {
		t.Fatalf("unexpected marker ignored: %v", brep.Entries)
	}
	if same, diff := ir.Equal(withTriggers(ir.Trigger{Kind: ir.TriggerPush}, ir.Trigger{Kind: ir.TriggerSchedule, Cron: "0 3 * * *"}), back); !same {
		t.Fatalf("round trip differs:\n%s", diff)
	}
}

// A pull request from a fork would run its code with every secret of the Task, so the filter admits only the repository itself.
func TestPullRequestFilterRejectsForks(t *testing.T) {
	files, rep := emit(t, withTriggers(ir.Trigger{Kind: ir.TriggerPR}))
	if !strings.Contains(files[listenerFile], "body.pull_request.head.repo.full_name == body.repository.full_name") {
		t.Fatalf("no fork gate:\n%s", files[listenerFile])
	}
	e := entry(rep, caps.TriggerPullRequest, "fork")
	if e == nil {
		t.Fatalf("no fork entry: %v", rep.Entries)
	}
	back, brep := reparse(t, files)
	if hasEntry(brep, "marker ignored") || len(back.Pipelines[0].Triggers) != 1 || back.Pipelines[0].Triggers[0].Kind != ir.TriggerPR {
		t.Fatalf("the gated filter must read back: %+v %v", back.Pipelines[0].Triggers, brep.Entries)
	}
}

func TestTagTriggerSetsTheEventOfTheSourceDialect(t *testing.T) {
	for origin, want := range map[string]string{"gha": "'push'", "woodpecker": "'tag'", "drone": "'tag'", "gitlab": "'tag'"} {
		t.Run(origin, func(t *testing.T) {
			p := withTriggers(ir.Trigger{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}}})
			p.Origin = ir.Origin{Provider: origin, Dialect: origin}
			p.Pipelines[0].Jobs[0].Steps[0].Script = []string{"echo ${{ event }}"}
			files, _ := emit(t, p)
			var got string
			for _, o := range list(celParams(t, listenerTriggers(t, files)[0])["overlays"]) {
				if o.(doc)["key"] == "ctx_event" {
					got = o.(doc)["expression"].(string)
				}
			}
			if got != want {
				t.Fatalf("ctx_event = %s, want %s", got, want)
			}
		})
	}
}

func TestPullRequestBranchIsTheTargetForDroneAndWoodpecker(t *testing.T) {
	for origin, want := range map[string]string{"drone": "body.pull_request.base.ref", "woodpecker": "body.pull_request.base.ref", "gha": "''"} {
		t.Run(origin, func(t *testing.T) {
			p := withTriggers(ir.Trigger{Kind: ir.TriggerPR})
			p.Origin = ir.Origin{Provider: origin, Dialect: origin}
			p.Pipelines[0].Jobs[0].Steps[0].Script = []string{"echo ${{ branch }}"}
			files, _ := emit(t, p)
			var got string
			for _, o := range list(celParams(t, listenerTriggers(t, files)[0])["overlays"]) {
				if o.(doc)["key"] == "ctx_branch" {
					got = o.(doc)["expression"].(string)
				}
			}
			if got != want {
				t.Fatalf("ctx_branch = %s, want %s", got, want)
			}
		})
	}
}
