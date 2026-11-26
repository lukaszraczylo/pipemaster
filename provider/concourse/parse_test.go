package concourse_test

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/concourse"
)

const echoTask = "  - task: unit\n    config:\n      platform: linux\n      run: {path: sh, args: [-exc, \"echo hi\\nmake test\"]}\n"

func TestDetect(t *testing.T) {
	for _, c := range []struct {
		files map[string]string
		name  string
		want  bool
	}{
		{name: "pipeline", files: map[string]string{".concourse/ci.yml": oneJob(echoTask)}, want: true},
		{name: "yaml ext", files: map[string]string{".concourse/ci.yaml": oneJob(echoTask)}, want: true},
		{name: "no jobs", files: map[string]string{".concourse/t.yml": "platform: linux\nrun: {path: ls}\n"}, want: false},
		{name: "jobs without plan", files: map[string]string{".concourse/t.yml": "jobs:\n- name: a\n"}, want: false},
		{name: "outside dir", files: map[string]string{"ci/p.yml": oneJob(echoTask)}, want: false},
		{name: "broken yaml", files: map[string]string{".concourse/p.yml": "jobs: [\n"}, want: false},
		{name: "duplicate key still detected", files: map[string]string{".concourse/p.yml": "jobs:\n- name: a\n  plan: []\n  plan: []\n"}, want: true},
		{name: "empty", files: map[string]string{}, want: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := concourse.New().Detect(repo(c.files)); got != c.want {
				t.Fatalf("Detect = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSourceFilesAndIDRules(t *testing.T) {
	l, ok := concourse.New().(provider.Layout)
	if !ok {
		t.Fatal("the provider must implement Layout")
	}
	got, err := l.SourceFiles(repo(map[string]string{".concourse/b.yaml": "a: 1", ".concourse/sub/a.yml": "a: 1", ".concourse/x.md": "x", "o.yml": "a: 1"}))
	if err != nil || !reflect.DeepEqual(got, []string{".concourse/b.yaml", ".concourse/sub/a.yml"}) {
		t.Fatalf("SourceFiles = %v, %v", got, err)
	}
	r, ok := concourse.New().(provider.IDRules)
	if !ok {
		t.Fatal("the provider must implement IDRules")
	}
	for in, want := range map[string]string{"build": "build", "Build Test": "build-test", "a--b": "a--b", "-x": "x", "": "default", "9lives": "9lives", "a/b!": "a-b-", "_x": "x"} {
		if got := r.SanitizeID(in); got != want {
			t.Errorf("SanitizeID(%q) = %q, want %q", in, got, want)
		}
		if again := r.SanitizeID(want); again != want {
			t.Errorf("SanitizeID is not idempotent on %q: %q", want, again)
		}
	}
}

func TestGitTrigger(t *testing.T) {
	pl, _ := parseYAML(t, "resources:\n- name: repo\n  type: git\n  source:\n    uri: https://example.com/r.git\n    branch: main\n    paths: [src/**]\n    ignore_paths: [docs/**]\njobs:\n- name: build\n  plan:\n  - {get: repo, trigger: true}\n"+echoTask)
	if len(pl.Triggers) != 1 {
		t.Fatalf("triggers = %+v", pl.Triggers)
	}
	tr := pl.Triggers[0]
	if tr.Kind != ir.TriggerPush || !reflect.DeepEqual(tr.Branches.Include, []string{"main"}) ||
		!reflect.DeepEqual(tr.Paths.Include, []string{"src/**"}) || !reflect.DeepEqual(tr.Paths.Exclude, []string{"docs/**"}) {
		t.Fatalf("trigger = %+v", tr)
	}
	if job(t, pl, "build").Manual {
		t.Error("a job with a triggering get is not manual")
	}
	if got := ext(tr.Ext)["resources"]; !reflect.DeepEqual(got, []any{"repo"}) {
		t.Errorf("trigger Ext resources = %v", got)
	}
}

func TestTagFilterTrigger(t *testing.T) {
	pl, _ := parseYAML(t, "resources:\n- name: repo\n  type: git\n  source: {uri: u, tag_filter: 'v*'}\njobs:\n- name: rel\n  plan:\n  - {get: repo, trigger: true}\n"+echoTask)
	tr := pl.Triggers[0]
	if tr.Kind != ir.TriggerTag || !reflect.DeepEqual(tr.Tags.Include, []string{"v*"}) || len(tr.Branches.Include) != 0 {
		t.Fatalf("trigger = %+v", tr)
	}
}

func TestTagFilterWithBranchFailsSafe(t *testing.T) {
	pl, rep := parseYAML(t, "resources:\n- name: repo\n  type: git\n  source: {uri: u, branch: main, tag_filter: 'v*'}\njobs:\n- name: rel\n  plan:\n  - {get: repo, trigger: true}\n"+echoTask)
	if len(pl.Triggers) != 1 || pl.Triggers[0].Kind != ir.TriggerAPI || !hasEntry(rep, "tag_filter") {
		t.Fatalf("triggers = %+v", pl.Triggers)
	}
}

func TestManualJobs(t *testing.T) {
	pl, _ := parseYAML(t, gitRepo+"jobs:\n- name: a\n  plan:\n  - {get: repo, trigger: true}\n"+echoTask+"- name: b\n  plan:\n  - {get: repo}\n"+echoTask+"- name: c\n  plan:\n"+echoTask)
	if job(t, pl, "a").Manual || !job(t, pl, "b").Manual || !job(t, pl, "c").Manual {
		t.Fatalf("manual flags: a=%v b=%v c=%v", job(t, pl, "a").Manual, job(t, pl, "b").Manual, job(t, pl, "c").Manual)
	}
}

func TestTimeResource(t *testing.T) {
	for _, c := range []struct{ src, cron string }{
		{"{interval: 1h}", "0 */1 * * *"},
		{"{interval: 6h}", "0 */6 * * *"},
		{"{interval: 24h}", "0 0 * * *"},
		{"{interval: 15m}", "*/15 * * * *"},
		{"{days: [Monday, Friday]}", "0 0 * * 1,5"},
	} {
		pl, rep := parseYAML(t, "resources:\n- {name: tick, type: time, source: "+c.src+"}\njobs:\n- name: nightly\n  plan:\n  - {get: tick, trigger: true}\n"+echoTask)
		if len(pl.Triggers) != 1 || pl.Triggers[0].Kind != ir.TriggerSchedule || pl.Triggers[0].Cron != c.cron {
			t.Errorf("%s: triggers = %+v, want cron %q", c.src, pl.Triggers, c.cron)
		}
		if !hasEntry(rep, "approximate cron") {
			t.Errorf("%s: no approximate-cron entry", c.src)
		}
	}
}

func TestUnreadableTriggersFailSafe(t *testing.T) {
	for name, src := range map[string]string{
		"time window":        "type: time\n  source: {start: 9AM, stop: 5PM, interval: 1h}",
		"time zone":          "type: time\n  source: {interval: 1h, location: Europe/London}",
		"time odd interval":  "type: time\n  source: {interval: 7m}",
		"git commit_filter":  "type: git\n  source: {uri: u, branch: main, commit_filter: {include: [x]}}",
		"git tag_regex":      "type: git\n  source: {uri: u, branch: main, tag_regex: 'v.*'}",
		"git version_type":   "type: git\n  source: {uri: u, branch: main, version_type: branches}",
		"git unknown key":    "type: git\n  source: {uri: u, branch: main, future_filter: x}",
		"git var branch":     "type: git\n  source: {uri: u, branch: ((branch))}",
		"git var paths":      "type: git\n  source: {uri: u, branch: main, paths: [((p))]}",
		"git paths not list": "type: git\n  source: {uri: u, branch: main, paths: src}",
		"git no source":      "type: git",
	} {
		t.Run(name, func(t *testing.T) {
			pl, rep := parseYAML(t, "resources:\n- name: r\n  "+src+"\njobs:\n- name: j\n  plan:\n  - {get: r, trigger: true}\n"+echoTask)
			if len(pl.Triggers) != 1 || pl.Triggers[0].Kind != ir.TriggerAPI {
				t.Fatalf("triggers = %+v, want one api trigger", pl.Triggers)
			}
			if _, ok := ext(pl.Triggers[0].Ext)["unreadable"]; !ok {
				t.Error("the api trigger does not say why it is unreadable")
			}
			if !hasEntry(rep, "unreadable") {
				t.Error("no report entry")
			}
		})
	}
}

func TestShadowedBuiltinTypeIsNotGit(t *testing.T) {
	pl, _ := parseYAML(t, "resource_types:\n- {name: git, type: registry-image, source: {repository: x/git}}\nresources:\n- {name: r, type: git, source: {uri: u, branch: main}}\njobs:\n- name: j\n  plan:\n  - {get: r, trigger: true}\n"+echoTask)
	if tr := pl.Triggers[0]; tr.Kind != ir.TriggerResource || tr.Resource == nil || tr.Resource.Type != "git" {
		t.Fatalf("trigger = %+v", tr)
	}
}

func TestTwoGitResourcesOneJobMergeBranches(t *testing.T) {
	pl, rep := parseYAML(t, "resources:\n- {name: a, type: git, source: {uri: u, branch: main}}\n- {name: b, type: git, source: {uri: u, branch: release}}\njobs:\n- name: j\n  plan:\n  - {get: a, trigger: true}\n  - {get: b, trigger: true}\n"+echoTask)
	if len(pl.Triggers) != 1 || !reflect.DeepEqual(pl.Triggers[0].Branches.Include, []string{"main", "release"}) {
		t.Fatalf("triggers = %+v", pl.Triggers)
	}
	if !hasEntry(rep, "once per branch event") {
		t.Error("no entry that the job runs per branch event")
	}
}

func TestGitResourcesThatDifferStayApart(t *testing.T) {
	for name, res := range map[string]string{
		"paths": "- {name: a, type: git, source: {uri: u, branch: main, paths: [x]}}\n- {name: b, type: git, source: {uri: u, branch: dev}}\n",
		"uri":   "- {name: a, type: git, source: {uri: u1, branch: main}}\n- {name: b, type: git, source: {uri: u2, branch: dev}}\n",
	} {
		pl, _ := parseYAML(t, "resources:\n"+res+"jobs:\n- name: j\n  plan:\n  - {get: a, trigger: true}\n  - {get: b, trigger: true}\n"+echoTask)
		if len(pl.Triggers) != 2 {
			t.Errorf("%s: triggers = %+v, want two", name, pl.Triggers)
		}
	}
}

func TestNonGitResourceBecomesResourceSteps(t *testing.T) {
	pl, rep := parseYAML(t, "resource_types:\n- {name: s3, type: registry-image, source: {repository: x/s3}}\nresources:\n- {name: bucket, type: s3, icon: aws, check_every: 5m, webhook_token: ((tok)), source: {bucket: b}}\n- {name: unused, type: s3, source: {bucket: u}}\njobs:\n- name: pub\n  plan:\n  - {get: bucket, trigger: true, params: {unpack: true}}\n"+echoTask+"  - {put: bucket, params: {file: out/x.tgz}, timeout: 5m}\n")
	j := job(t, pl, "pub")
	g, p := step(t, j, "bucket"), j.Steps[len(j.Steps)-1]
	if g.Kind != ir.StepResource || p.Kind != ir.StepResource {
		t.Fatalf("kinds: %v %v", g.Kind, p.Kind)
	}
	if ext(g.Ext)["get"] != "bucket" || ext(p.Ext)["put"] != "bucket" {
		t.Errorf("Ext get/put missing: %v / %v", g.Ext, p.Ext)
	}
	if p.Timeout == nil || *p.Timeout != ir.Duration(5*60*1e9) {
		t.Errorf("put timeout = %v", p.Timeout)
	}
	if _, kept := ext(p.Ext)["timeout"]; kept {
		t.Error("a consumed timeout must not stay in Ext")
	}
	tr := pl.Triggers[0]
	if tr.Kind != ir.TriggerResource || tr.Resource == nil || tr.Resource.Name != "bucket" || tr.Resource.Type != "s3" {
		t.Fatalf("trigger = %+v", tr)
	}
	res, _ := ext(pl.Ext)["resources"].([]any)
	if len(res) != 2 {
		t.Fatalf("pipeline Ext resources = %v", res)
	}
	first := res[0].(map[string]any)
	if first["icon"] != "aws" || first["check_every"] != "5m" || first["webhook_token"] != "((tok))" {
		t.Errorf("resource metadata not kept verbatim: %v", first)
	}
	for _, want := range []string{"icon", "check_every", "webhook_token", "unused"} {
		if !hasEntry(rep, want) {
			t.Errorf("no report entry mentions %q", want)
		}
	}
}

func TestPassedMapsToNeeds(t *testing.T) {
	pl, _ := parseYAML(t, "resources:\n- {name: a, type: git, source: {uri: u, branch: main}}\n- {name: b, type: git, source: {uri: u, branch: main}}\njobs:\n- name: one\n  plan:\n  - {get: a, trigger: true}\n  - {get: b}\n"+echoTask+"- name: two\n  plan:\n  - {get: a, trigger: true}\n"+echoTask+
		"- name: three\n  plan:\n  - in_parallel:\n    - {get: a, trigger: true, passed: [one, two]}\n    - {get: b, passed: [one]}\n"+echoTask)
	j := job(t, pl, "three")
	var ids []string
	for _, n := range j.Needs {
		ids = append(ids, n.JobID)
	}
	if !reflect.DeepEqual(ids, []string{"one", "two"}) {
		t.Fatalf("needs = %v", ids)
	}
	if j.Manual {
		t.Error("three has a triggering get")
	}
	if len(pl.Triggers) != 1 || len(pl.Triggers[0].Branches.Include) != 1 {
		t.Errorf("a get with passed must not add a trigger: %+v", pl.Triggers)
	}
}

func TestPassedOrdersJobsWithoutTrigger(t *testing.T) {
	pl, _ := parseYAML(t, gitRepo+"jobs:\n- name: one\n  plan:\n  - {get: repo, trigger: true}\n"+echoTask+"- name: two\n  plan:\n  - {get: repo, passed: [one]}\n"+echoTask)
	j := job(t, pl, "two")
	if len(j.Needs) != 1 || j.Needs[0].JobID != "one" || !j.Manual {
		t.Fatalf("two = needs %v manual %v", j.Needs, j.Manual)
	}
}

func TestPassedGlobAndUnresolvable(t *testing.T) {
	head := gitRepo + "jobs:\n- name: unit-a\n  plan:\n  - {get: repo, trigger: true}\n" + echoTask + "- name: unit-b\n  plan:\n  - {get: repo, trigger: true}\n" + echoTask
	pl, rep := parseYAML(t, head+"- name: ship\n  plan:\n  - {get: repo, trigger: true, passed: ['unit-*']}\n"+echoTask)
	if got := job(t, pl, "ship").Needs; len(got) != 2 || got[0].JobID != "unit-a" || got[1].JobID != "unit-b" {
		t.Fatalf("glob needs = %v", got)
	}
	if !hasEntry(rep, "glob") {
		t.Error("no glob entry")
	}
	for name, passed := range map[string]string{"brace glob": "['unit-{a,b}']", "unknown job": "[nope]", "no match": "['zzz*']", "self": "[ship]"} {
		pl, rep := parseYAML(t, head+"- name: ship\n  plan:\n  - {get: repo, trigger: true, passed: "+passed+"}\n"+echoTask)
		if j := job(t, pl, "ship"); !j.Manual {
			t.Errorf("%s: an unresolvable passed must fail safe to manual", name)
		}
		if !hasEntry(rep, "passed") {
			t.Errorf("%s: no entry", name)
		}
	}
}

func TestSerialAndConcurrency(t *testing.T) {
	pl, rep := parseYAML(t, oneJob(echoTask)+"  serial: true\n  max_in_flight: 3\n")
	if pl.Concurrency == nil || pl.Concurrency.Group != "build" {
		t.Fatalf("concurrency = %+v", pl.Concurrency)
	}
	e := ext(job(t, pl, "build").Ext)
	if e["max_in_flight"] != 3 || e["serial"] != true {
		t.Errorf("job Ext = %v", e)
	}
	if !hasEntry(rep, "max_in_flight") {
		t.Error("no entry for max_in_flight")
	}
	pl, _ = parseYAML(t, gitRepo+"jobs:\n- {name: a, serial_groups: [deploy], plan: [{get: repo}]}\n- {name: b, serial_groups: [deploy], plan: [{get: repo}]}\n")
	if pl.Concurrency == nil || pl.Concurrency.Group != "deploy" {
		t.Fatalf("shared group concurrency = %+v", pl.Concurrency)
	}
	pl, rep = parseYAML(t, gitRepo+"jobs:\n- {name: a, serial: true, plan: [{get: repo}]}\n- {name: b, serial_groups: [x], plan: [{get: repo}]}\n")
	if pl.Concurrency != nil || !hasEntry(rep, "serial") {
		t.Fatalf("several groups: concurrency = %+v", pl.Concurrency)
	}
}

func TestHooks(t *testing.T) {
	pl, _ := parseYAML(t, oneJob(echoTask)+"  on_failure:\n    task: alert\n    config: {platform: linux, run: {path: echo, args: [failed]}}\n  ensure:\n    do:\n    - {task: clean, config: {platform: linux, run: {path: rm, args: [-rf, tmp]}}}\n    - {task: log, config: {platform: linux, run: {path: echo}}}\n  on_success: {put: repo}\n  on_error: {put: repo, params: {x: 1}}\n  on_abort: {put: repo}\n")
	j := job(t, pl, "build")
	want := map[string]string{"alert": "failure()", "clean": "always()", "log": "always()", "repo": "success()"}
	for _, s := range j.Steps[1:] {
		if w, ok := want[s.ID]; ok && cond(s.If) != w {
			t.Errorf("step %s if = %q, want %q", s.ID, cond(s.If), w)
		}
	}
	e := ext(j.Ext)
	if e["on_error"] == nil || e["on_abort"] == nil {
		t.Errorf("on_error/on_abort not kept: %v", e)
	}
	if cond(step(t, j, "unit").If) != "" {
		t.Error("main step must be unconditional")
	}
}

func TestInlineTaskMapping(t *testing.T) {
	pl, rep := parseYAML(t, oneJob("  - task: unit\n    timeout: 30m\n    params: {LEVEL: debug, TOKEN: ((deploy-token)), REGION: ((region)), LOCAL: ((.:x)), MIXED: \"a-((b))\"}\n    config:\n      platform: linux\n      image_resource: {type: registry-image, source: {repository: golang, tag: \"1.22\"}}\n      params: {LEVEL: info, BASE: x}\n      inputs: [{name: repo}]\n      run: {path: bash, dir: repo, args: [-ec, \"set -x\\ngo test ./...\"]}\n"))
	s := step(t, job(t, pl, "build"), "unit")
	if s.Kind != ir.StepScript || s.Shell != "bash" || s.WorkDir != "repo" || !reflect.DeepEqual(s.Script, []string{"set -x", "go test ./..."}) {
		t.Fatalf("step = %+v", s)
	}
	if s.Image == nil || s.Image.Ref != "golang:1.22" {
		t.Errorf("image = %+v", s.Image)
	}
	if s.Timeout == nil || *s.Timeout != ir.Duration(30*60*1e9) {
		t.Errorf("timeout = %v", s.Timeout)
	}
	env := map[string]ir.Value{}
	var names []string
	for _, v := range s.Env {
		env[v.Name] = v.Value
		names = append(names, v.Name)
	}
	if !reflect.DeepEqual(names, []string{"LEVEL", "BASE", "TOKEN", "REGION"}) {
		t.Errorf("env order = %v", names)
	}
	if env["LEVEL"].Lit != "debug" || env["BASE"].Lit != "x" {
		t.Errorf("step params must override config params: %+v", env)
	}
	if env["TOKEN"].Kind != ir.ValueSecret || env["TOKEN"].Secret != "deploy-token" {
		t.Errorf("TOKEN = %+v", env["TOKEN"])
	}
	if r := env["REGION"]; r.Kind != ir.ValueSecret || r.Secret != "region" {
		t.Errorf("REGION = %+v", r)
	}
	e := ext(s.Ext)
	unread, _ := e["params"].(map[string]any)
	if unread["LOCAL"] != "((.:x))" || unread["MIXED"] != "a-((b))" {
		t.Errorf("unreadable params not kept: %v", e)
	}
	if !reflect.DeepEqual(e["shell_flags"], []any{"-ec"}) {
		t.Errorf("shell flags = %v", e["shell_flags"])
	}
	if cfg, _ := e["config"].(map[string]any); cfg["platform"] != "linux" || cfg["inputs"] == nil {
		t.Errorf("residual config = %v", e["config"])
	}
	for _, want := range []string{"LOCAL", "task config keys inputs"} {
		if !hasEntry(rep, want) {
			t.Errorf("no entry mentions %q", want)
		}
	}
}

func TestInlineTaskCommandForm(t *testing.T) {
	pl, _ := parseYAML(t, oneJob("  - task: build\n    config:\n      platform: linux\n      run: {path: ./repo/ci/build.sh, args: [--fast, \"two words\"]}\n  - task: bare\n    config: {platform: linux}\n"))
	s := step(t, job(t, pl, "build"), "build")
	if !reflect.DeepEqual(s.Script, []string{"./repo/ci/build.sh --fast 'two words'"}) || s.Shell != "" {
		t.Fatalf("script = %q shell %q", s.Script, s.Shell)
	}
	e := ext(s.Ext)
	if e["command"] != "./repo/ci/build.sh" || e["script_line"] != s.Script[0] || !reflect.DeepEqual(e["args"], []any{"--fast", "two words"}) {
		t.Errorf("Ext = %v", e)
	}
	if bare := step(t, job(t, pl, "build"), "bare"); len(bare.Script) != 0 || ext(bare.Ext)["entrypoint"] != true {
		t.Errorf("bare = %+v", bare)
	}
}

func TestImageResourceThatTheIRCannotHold(t *testing.T) {
	for name, ir := range map[string]string{
		"other type": "{type: docker-image, source: {repository: x}}",
		"var":        "{type: registry-image, source: {repository: ((reg))/x}}",
		"creds":      "{type: registry-image, source: {repository: x, username: u, password: ((p))}}",
	} {
		pl, _ := parseYAML(t, oneJob("  - task: t\n    config: {platform: linux, image_resource: "+ir+", run: {path: ls}}\n"))
		s := step(t, job(t, pl, "build"), "t")
		if s.Image != nil && name != "creds" {
			t.Errorf("%s: image = %+v", name, s.Image)
		}
		if ext(s.Ext)["config"] == nil && ext(s.Ext)["image_resource"] == nil {
			t.Errorf("%s: image_resource not kept: %v", name, s.Ext)
		}
	}
}

func TestTaskFile(t *testing.T) {
	pl, rep := parseYAML(t, oneJob("  - task: lint\n    file: repo/ci/lint.yml\n    params: {A: b}\n    input_mapping: {src: repo}\n"))
	s := step(t, job(t, pl, "build"), "lint")
	e := ext(s.Ext)
	if s.Kind != ir.StepTask || e["file"] != "repo/ci/lint.yml" || e["input_mapping"] == nil || len(s.Env) != 1 {
		t.Fatalf("step = %+v ext %v", s, s.Ext)
	}
	if !hasEntry(rep, "repo/ci/lint.yml") {
		t.Error("no entry for the task file")
	}
}

func TestTryTimeoutAttempts(t *testing.T) {
	pl, _ := parseYAML(t, oneJob("  - try: {task: flaky, config: {platform: linux, run: {path: ls}}}\n  - task: once\n    attempts: 3\n    config: {platform: linux, run: {path: ls}}\n"))
	j := job(t, pl, "build")
	if !step(t, j, "flaky").ContinueOnError || step(t, j, "once").ContinueOnError {
		t.Error("try must set ContinueOnError on its step only")
	}
	if ext(step(t, j, "once").Ext)["attempts"] != 3 {
		t.Errorf("step attempts must stay in Ext: %v", step(t, j, "once").Ext)
	}
	pl, _ = parseYAML(t, gitRepo+"jobs:\n- name: j\n  plan:\n  - attempts: 3\n    timeout: 1h\n    do:\n    - {get: repo, trigger: true}\n    - {task: t, config: {platform: linux, run: {path: ls}}}\n")
	j = job(t, pl, "j")
	if j.Retry == nil || j.Retry.Attempts != 3 || j.Timeout == nil || *j.Timeout != ir.Duration(3600*1e9) || len(j.Steps) != 1 {
		t.Fatalf("job = retry %+v timeout %v steps %d", j.Retry, j.Timeout, len(j.Steps))
	}
	if j.Manual || len(pl.Triggers) != 1 {
		t.Errorf("a trigger inside the do wrapper must count: manual=%v triggers=%v", j.Manual, pl.Triggers)
	}
}

func TestOpaqueStepsStayInExt(t *testing.T) {
	pl, rep := parseYAML(t, oneJob("  - in_parallel: [{task: a, config: {platform: linux, run: {path: ls}}}]\n  - do: [{task: b, config: {platform: linux, run: {path: ls}}}]\n    on_failure: {put: repo}\n  - {load_var: v, file: repo/v.json}\n  - {set_pipeline: self, file: repo/p.yml}\n  - {task: m, across: [{var: x, values: [1, 2]}], config: {platform: linux, run: {path: ls}}}\n"))
	j := job(t, pl, "build")
	var plugins []string
	for _, s := range j.Steps {
		if s.Kind == ir.StepPlugin {
			plugins = append(plugins, ext(s.Ext)["kind"].(string))
			if ext(s.Ext)["step"] == nil {
				t.Errorf("%s: step not kept", s.ID)
			}
		}
	}
	if !reflect.DeepEqual(plugins, []string{"in_parallel", "do", "load_var", "set_pipeline", "across"}) {
		t.Fatalf("opaque kinds = %v", plugins)
	}
	for _, want := range []string{"in_parallel", "across", "load_var", "set_pipeline"} {
		if !hasEntry(rep, want) {
			t.Errorf("no entry for %s", want)
		}
	}
}

func TestStepIDsAreUnique(t *testing.T) {
	pl, _ := parseYAML(t, oneJob(echoTask+"  on_failure: {task: unit, config: {platform: linux, run: {path: ls}}}\n"))
	seen := map[string]bool{}
	for _, s := range job(t, pl, "build").Steps {
		if seen[s.ID] {
			t.Fatalf("duplicate step id %s", s.ID)
		}
		seen[s.ID] = true
	}
}

func TestMetadataIsKept(t *testing.T) {
	pl, rep := parseYAML(t, "x-anchors: &a {k: v}\nresource_types:\n- {name: rt, type: registry-image, source: {repository: r}}\nvar_sources:\n- {name: v, type: dummy, config: {vars: {a: b}}}\ngroups:\n- {name: g, jobs: [build]}\ndisplay: {background_image: x.png}\nuser_data: {a: 1}\n"+oneJob(echoTask)+"  old_name: legacy\n  public: true\n  interruptible: true\n  disable_manual_trigger: true\n  build_log_retention: {builds: 5}\n")
	pe := ext(pl.Ext)
	for _, k := range []string{"resource_types", "var_sources", "groups", "display", "user_data", "resources"} {
		if pe[k] == nil {
			t.Errorf("pipeline Ext %s missing", k)
		}
	}
	if extra, _ := pe["extra"].(map[string]any); extra["x-anchors"] == nil {
		t.Errorf("unknown top-level key not kept: %v", pe)
	}
	je := ext(job(t, pl, "build").Ext)
	for _, k := range []string{"old_name", "public", "interruptible", "disable_manual_trigger", "build_log_retention"} {
		if je[k] == nil {
			t.Errorf("job Ext %s missing", k)
		}
		if !hasEntry(rep, k) {
			t.Errorf("no entry for %s", k)
		}
	}
	for _, k := range []string{"x-anchors", "groups", "display", "var_sources", "resource_types", "user_data"} {
		if !hasEntry(rep, k) {
			t.Errorf("no entry for %s", k)
		}
	}
}

func TestLeadingGetsAreJobExt(t *testing.T) {
	pl, _ := parseYAML(t, gitRepo+"jobs:\n- name: j\n  plan:\n  - in_parallel:\n    - {get: repo, trigger: true, params: {depth: 1}, version: every}\n"+echoTask)
	j := job(t, pl, "j")
	e := ext(j.Ext)
	gets, _ := e["gets"].([]any)
	if len(gets) != 1 || gets[0].(map[string]any)["params"] == nil || e["gets_in_parallel"] != true {
		t.Fatalf("job Ext = %v", e)
	}
	if len(j.Steps) != 1 {
		t.Errorf("a source get must not be a step: %d steps", len(j.Steps))
	}
}

func TestDuplicateJobNamesAcrossFilesAreSeparatePipelines(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"a.yml": oneJob(echoTask), "b.yml": oneJob(echoTask)})
	if len(p.Pipelines) != 2 || p.Pipelines[0].ID != "a" || p.Pipelines[1].ID != "b" || len(p.Origin.Files) != 2 {
		t.Fatalf("pipelines = %+v origin %+v", p.Pipelines, p.Origin)
	}
	for _, pl := range p.Pipelines {
		job(t, pl, "build")
	}
}

func TestInvalidSources(t *testing.T) {
	for name, y := range map[string]string{
		"duplicate key":      oneJob(echoTask) + "  serial: true\n  serial: false\n",
		"job without name":   "jobs:\n- plan: []\n",
		"duplicate job name": gitRepo + "jobs:\n- {name: a, plan: []}\n- {name: a, plan: []}\n",
		"needs cycle":        gitRepo + "jobs:\n- {name: a, plan: [{get: repo, passed: [b]}]}\n- {name: b, plan: [{get: repo, passed: [a]}]}\n",
	} {
		t.Run(name, func(t *testing.T) { parseErr(t, y) })
	}
	_, _, err := concourse.New().Parse(repo(map[string]string{".concourse/a.yml": oneJob(echoTask), ".concourse/a.yaml": oneJob(echoTask)}))
	if err == nil || !strings.Contains(err.Error(), "a.yaml") {
		t.Errorf("same pipeline id twice: %v", err)
	}
	if _, _, err := concourse.New().Parse(repo(map[string]string{})); err == nil {
		t.Error("no files must be an error")
	}
}

func TestAnchorsAndMergeKeys(t *testing.T) {
	pl, _ := parseYAML(t, "x: &cfg {platform: linux}\nx2: &cfg {platform: darwin}\n"+gitRepo+"jobs:\n- name: j\n  plan:\n  - {get: repo, trigger: true}\n  - task: t\n    config:\n      <<: *cfg\n      run: {path: ls}\n")
	if cfg, _ := ext(step(t, job(t, pl, "j"), "t").Ext)["config"].(map[string]any); cfg["platform"] != "darwin" {
		t.Fatalf("the later anchor must win: %v", cfg)
	}
}

func TestOtherYAMLFilesAreKept(t *testing.T) {
	p, rep := parseFiles(t, map[string]string{"ci.yml": oneJob(echoTask), "tasks/lint.yml": "platform: linux\nrun: {path: ls}\n"})
	files, _ := ext(p.Pipelines[0].Ext)["files"].(map[string]any)
	if files[".concourse/tasks/lint.yml"] == nil || !hasEntry(rep, "tasks/lint.yml") {
		t.Fatalf("task file not kept: %v", ext(p.Pipelines[0].Ext))
	}
}

func TestOutputValidates(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"ci.yml": oneJob(echoTask + "  on_failure: {put: repo}\n")})
	if err := ir.Validate(p); err != nil {
		t.Fatal(err)
	}
	if _, err := ir.Marshal(p); err != nil {
		t.Fatal(err)
	}
}

func TestPlanEntryThatIsNoMapping(t *testing.T) {
	pl, rep := parseYAML(t, "jobs:\n- name: j\n  plan:\n  -\n  - just text\n")
	if got := job(t, pl, "j").Steps; len(got) != 2 || got[0].Kind != ir.StepPlugin || !hasEntry(rep, "not a mapping") {
		t.Fatalf("steps = %+v", got)
	}
}

func TestResourceEntryThatIsNoMappingOrNoName(t *testing.T) {
	pl, rep := parseYAML(t, "resources:\n- {}\n- text\njobs:\n- name: j\n  plan: []\n")
	if res, _ := ext(pl.Ext)["resources"].([]any); len(res) != 2 || !hasEntry(rep, "not a mapping") {
		t.Fatalf("Ext = %v", pl.Ext)
	}
}

func TestParsesTheCorpusFixture(t *testing.T) {
	data, err := os.ReadFile("../../testdata/concourse/capi-ci-cve-pipeline.yml")
	if err != nil {
		t.Fatal(err)
	}
	pl, rep := parseYAML(t, string(data))
	if len(pl.Jobs) == 0 || len(rep.Entries) == 0 {
		t.Fatalf("jobs=%d entries=%d", len(pl.Jobs), len(rep.Entries))
	}
	t.Logf("%d jobs, %d triggers, %d entries", len(pl.Jobs), len(pl.Triggers), len(rep.Entries))
}

func TestVarInRunIsKeptNotScripted(t *testing.T) {
	for name, run := range map[string]string{
		"args": "{path: sh, args: [-ec, \"curl -H 'X: ((api_token))' u\"]}",
		"path": "{path: ((tool)), args: [a]}",
		"dir":  "{path: ls, dir: ((where))}",
		"word": "{path: ./x.sh, args: [--token, ((api_token))]}",
	} {
		t.Run(name, func(t *testing.T) {
			pl, rep := parseYAML(t, oneJob("  - task: t\n    config: {platform: linux, run: "+run+"}\n"))
			s := step(t, job(t, pl, "build"), "t")
			if len(s.Script) != 0 || s.WorkDir != "" || s.Shell != "" {
				t.Fatalf("a var must not reach the script: %+v", s)
			}
			if kept, _ := ext(s.Ext)["run"].(map[string]any); kept["path"] == nil {
				t.Errorf("run not kept whole: %v", s.Ext)
			}
			if !hasEntry(rep, "((") {
				t.Error("no entry for the var in run")
			}
		})
	}
}

func TestEveryVarIsASecretByDefault(t *testing.T) {
	names := []string{"deploy-token", "region", "gcp_service_account_json", "slack_webhook_url", "cluster"}
	body := ""
	for i, n := range names {
		body += fmt.Sprintf("      P%d: ((%s))\n", i, n)
	}
	pl, rep := parseYAML(t, oneJob("  - task: t\n    params:\n"+body+"    config: {platform: linux, run: {path: ls}}\n"))
	s := step(t, job(t, pl, "build"), "t")
	for i, n := range names {
		var got ir.Value
		for _, v := range s.Env {
			if v.Name == fmt.Sprintf("P%d", i) {
				got = v.Value
			}
		}
		if got.Kind != ir.ValueSecret || got.Secret != n {
			t.Errorf("%s read as %+v, want a secret", n, got)
		}
	}
	notes := 0
	for _, e := range rep.Entries {
		if e.Feature == caps.ConfigName && strings.Contains(e.Detail, "treated as secrets") {
			notes++
			for _, n := range names {
				if !strings.Contains(e.Detail, n) {
					t.Errorf("the note does not name %s: %s", n, e.Detail)
				}
			}
		}
	}
	if notes != 1 {
		t.Fatalf("want one note per pipeline, got %d: %v", notes, rep.Entries)
	}
}

func TestSetPipelineVarsStayPlain(t *testing.T) {
	y := oneJob("  - set_pipeline: child\n    file: repo/ci.yml\n    vars: {region: eu}\n  - task: t\n    params: {R: ((region)), T: ((deploy-token))}\n    config: {platform: linux, run: {path: ls}}\n")
	pl, _ := parseYAML(t, y)
	s := step(t, job(t, pl, "build"), "t")
	got := map[string]ir.Value{}
	for _, v := range s.Env {
		got[v.Name] = v.Value
	}
	if got["R"].Kind != ir.ValueExpr || got["T"].Kind != ir.ValueSecret {
		t.Fatalf("env = %+v", got)
	}
}

func TestMergedBranchesAreDeduplicated(t *testing.T) {
	pl, _ := parseYAML(t, "resources:\n- {name: a, type: git, source: {uri: u, branch: main}}\n- {name: b, type: git, source: {uri: u, branch: main}}\n- {name: c, type: git, source: {uri: u, branch: dev}}\njobs:\n- name: j\n  plan:\n  - {get: a, trigger: true}\n  - {get: b, trigger: true}\n  - {get: c, trigger: true}\n"+echoTask)
	if len(pl.Triggers) != 1 || !reflect.DeepEqual(pl.Triggers[0].Branches.Include, []string{"main", "dev"}) {
		t.Fatalf("triggers = %+v", pl.Triggers)
	}
	if refs := ext(pl.Triggers[0].Ext)["resources"]; !reflect.DeepEqual(refs, []any{"a", "b", "c"}) {
		t.Errorf("resources = %v", refs)
	}
}
