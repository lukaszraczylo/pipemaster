package drone_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const stepHead = "kind: pipeline\nname: p\nsteps:\n"

func TestScriptStep(t *testing.T) {
	j, _ := parseJob(t, stepHead+"- name: t\n  image: golang:1.22\n  entrypoint: [sh]\n  working_dir: /src\n  commands:\n  - go build\n  - go test\n  environment:\n    TOKEN:\n      from_secret: tok\n    MODE: ci\n")
	s := step(t, j, "t")
	if s.Kind != ir.StepScript || s.Image.Ref != "golang:1.22" || !reflect.DeepEqual(s.Image.Entrypoint, []string{"sh"}) || s.WorkDir != "/src" {
		t.Fatalf("step = %+v", s)
	}
	if !reflect.DeepEqual(s.Script, []string{"go build", "go test"}) {
		t.Fatalf("script = %q", s.Script)
	}
	if s.Env[0].Value.Kind != ir.ValueSecret || s.Env[0].Value.Secret != "tok" || s.Env[1].Value.Lit != "ci" {
		t.Fatalf("env = %+v", s.Env)
	}
	if s.Ext != nil {
		t.Fatalf("ext = %v, want none", s.Ext)
	}
}

func TestPluginStep(t *testing.T) {
	j, rep := parseJob(t, stepHead+"- name: pub\n  image: plugins/docker\n  settings:\n    repo: acme/app\n    password:\n      from_secret: pw\n    tags: [a, b]\n")
	s := step(t, j, "pub")
	if s.Kind != ir.StepPlugin || s.Image.Ref != "plugins/docker" || len(s.Script) != 0 {
		t.Fatalf("step = %+v", s)
	}
	if s.With["repo"].Lit != "acme/app" || s.With["password"].Secret != "pw" {
		t.Fatalf("with = %+v", s.With)
	}
	if _, ok := ext(s.Ext)["settings"]; !ok || !hasEntry(rep, "settings.tags") {
		t.Fatalf("a list setting must stay in Ext with an entry: %v", s.Ext)
	}
}

func TestFailureIgnore(t *testing.T) {
	j, rep := parseJob(t, stepHead+"- {name: a, image: x, failure: ignore}\n- {name: b, image: x, failure: fast}\n")
	if !step(t, j, "a").ContinueOnError {
		t.Fatal("failure: ignore must set ContinueOnError")
	}
	b := step(t, j, "b")
	if b.ContinueOnError || ext(b.Ext)["failure"] != "fast" || !hasEntry(rep, "failure: fast") {
		t.Fatalf("failure: fast must stay in Ext: %+v", b)
	}
}

func TestStepKeysWithoutIRFieldStayInExt(t *testing.T) {
	j, rep := parseJob(t, stepHead+"- name: a\n  image: x\n  depends_on: [b]\n  detach: true\n  privileged: true\n  pull: always\n  user: root\n  network_mode: host\n  command: [serve]\n  volumes:\n  - {name: tmp, path: /tmp}\n- {name: b, image: x}\n")
	e := ext(step(t, j, "a").Ext)
	for _, k := range []string{"depends_on", "detach", "privileged", "pull", "user", "network_mode", "command", "volumes"} {
		if _, ok := e[k]; !ok {
			t.Errorf("%s missing from Ext: %v", k, e)
		}
		if !hasEntry(rep, "key "+k+" has no IR field") {
			t.Errorf("no entry for %s", k)
		}
	}
	if !hasEntry(rep, "step DAG is not applied") {
		t.Error("depends_on entry must say the step DAG is not applied")
	}
}

func TestMultiLineCommandShape(t *testing.T) {
	j, _ := parseJob(t, stepHead+"- name: a\n  image: x\n  commands:\n  - |\n    if true; then\n      echo hi\n    fi\n  - echo done\n")
	s := step(t, j, "a")
	if len(s.Script) != 4 {
		t.Fatalf("script = %q", s.Script)
	}
	if _, ok := ext(s.Ext)["_commands"]; !ok {
		t.Fatalf("command shapes missing: %v", s.Ext)
	}
}

func TestStepWithoutCommandsOrSettingsIsPlugin(t *testing.T) {
	j, _ := parseJob(t, stepHead+"- {name: a, image: x}\n")
	if step(t, j, "a").Kind != ir.StepPlugin {
		t.Fatal("a step with no commands is a plugin step")
	}
}

func TestNonDockerTypeKeepsTypeAndParsesSteps(t *testing.T) {
	j, rep := parseJob(t, "kind: pipeline\ntype: exec\nname: p\nsteps:\n- name: a\n  commands: [make]\n")
	if ext(j.Ext)["type"] != "exec" || !hasEntry(rep, "pipeline type exec") {
		t.Fatalf("type must stay in Ext with an entry: %v", j.Ext)
	}
	if s := step(t, j, "a"); s.Image != nil || !reflect.DeepEqual(s.Script, []string{"make"}) {
		t.Fatalf("step = %+v", s)
	}
}

func TestMissingAndDockerTypeAreDocker(t *testing.T) {
	for _, head := range []string{"kind: pipeline\nname: p\n", "kind: pipeline\ntype: docker\nname: p\n"} {
		j, _ := parseJob(t, head+"steps:\n- {name: a, image: x}\n")
		if j.Ext != nil {
			t.Errorf("ext = %v, want none", j.Ext)
		}
	}
	parseErr(t, "kind: pipeline\nname: p\nsteps:\n- {name: a}\n")
}
