package woodpecker_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestStepsKeepOrderInBothForms(t *testing.T) {
	_, j, _ := single(t, "steps:\n  zeta:\n    image: a\n    commands: [one]\n  alpha:\n    image: b\n    commands: [two]\n")
	if j.Steps[0].ID != "zeta" || j.Steps[1].ID != "alpha" {
		t.Fatalf("map-form order lost: %s, %s", j.Steps[0].ID, j.Steps[1].ID)
	}
	_, j, _ = single(t, "steps:\n  - name: b\n    image: a\n    commands: [one]\n  - image: c\n    commands: [two]\n  - name: a\n    image: b\n    commands: [three]\n")
	if len(j.Steps) != 3 || j.Steps[0].ID != "b" || j.Steps[1].ID != "step-1" || j.Steps[2].ID != "a" {
		t.Fatalf("list-form order or unnamed step wrong: %+v", j.Steps)
	}
	if ext(j.Steps[1].Ext)["_derived_id"] != true || ext(j.Steps[0].Ext)["_derived_id"] != nil {
		t.Fatalf("only the unnamed step is marked derived: %v %v", j.Steps[1].Ext, j.Steps[0].Ext)
	}
}

// Woodpecker names an unnamed list step step-<index>, so a depends_on that says so must find it.
func TestDerivedStepIDMatchesDependsOn(t *testing.T) {
	_, j, _ := single(t, "steps:\n  - image: a\n    commands: [x]\n  - name: b\n    image: a\n    commands: [x]\n    depends_on: [step-0]\n")
	dep, _ := ext(step(t, j, "b").Ext)["depends_on"].([]any)
	if step(t, j, "step-0") == nil || !reflect.DeepEqual(dep, []any{"step-0"}) {
		t.Fatalf("depends_on %v does not resolve against %+v", dep, j.Steps)
	}
}

// An explicit name wins over the map key, as in Woodpecker; the key stays recoverable.
func TestMapFormExplicitNameWins(t *testing.T) {
	_, j, rep := single(t, "steps:\n  key1:\n    name: real\n    image: a\n    commands: [x]\n")
	st := step(t, j, "real")
	e := ext(st.Ext)
	if e["_map_key"] != "key1" || e["name"] != nil || len(rep.Entries) != 0 {
		t.Fatalf("ext %v entries %+v", e, rep.Entries)
	}
}

func TestCommandStep(t *testing.T) {
	_, j, _ := single(t, "steps:\n  - name: build\n    image: golang:1.22\n    directory: sub\n    entrypoint: [/bin/sh]\n    commands:\n      - go build\n      - go test\n")
	s := step(t, j, "build")
	if s.Kind != ir.StepScript || !reflect.DeepEqual(s.Script, []string{"go build", "go test"}) {
		t.Fatalf("script = %+v", s)
	}
	if s.Image == nil || s.Image.Ref != "golang:1.22" || !reflect.DeepEqual(s.Image.Entrypoint, []string{"/bin/sh"}) {
		t.Fatalf("image = %+v", s.Image)
	}
	if s.WorkDir != "sub" {
		t.Fatalf("workdir = %q", s.WorkDir)
	}
}

func TestCommandsAsString(t *testing.T) {
	_, j, _ := single(t, "steps:\n  - name: s\n    image: a\n    commands: echo hi\n")
	if !reflect.DeepEqual(step(t, j, "s").Script, []string{"echo hi"}) {
		t.Fatalf("script = %v", step(t, j, "s").Script)
	}
}

func TestPluginStep(t *testing.T) {
	_, j, rep := single(t, `steps:
  - name: publish
    image: woodpeckerci/plugin-docker-buildx
    settings:
      repo: org/app
      tags: [a, b]
      push: true
      password:
        from_secret: registry_pw
`)
	s := step(t, j, "publish")
	if s.Kind != ir.StepPlugin || s.Image == nil || s.Image.Ref != "woodpeckerci/plugin-docker-buildx" {
		t.Fatalf("plugin step = %+v", s)
	}
	if v := s.With["repo"]; v.Kind != ir.ValueLit || v.Lit != "org/app" {
		t.Fatalf("repo = %+v", v)
	}
	if v := s.With["push"]; v.Lit != "true" {
		t.Fatalf("push = %+v", v)
	}
	if v := s.With["password"]; v.Kind != ir.ValueSecret || v.Secret != "registry_pw" {
		t.Fatalf("password = %+v", v)
	}
	if _, ok := s.With["tags"]; ok {
		t.Fatal("a list setting has no Value form and must not be in With")
	}
	settings, _ := ext(s.Ext)["settings"].(map[string]any)
	if !reflect.DeepEqual(settings["tags"], []any{"a", "b"}) {
		t.Fatalf("structured setting not kept in Ext: %v", ext(s.Ext))
	}
	if !hasEntry(rep, "settings.tags") {
		t.Fatalf("no report entry for the structured setting: %+v", rep.Entries)
	}
}

func TestEnvironmentAndSecret(t *testing.T) {
	_, j, _ := single(t, "steps:\n  - name: s\n    image: a\n    commands: [x]\n    environment:\n      PLAIN: v\n      NUM: 3\n      TOKEN:\n        from_secret: tok\n")
	got := step(t, j, "s").Env
	want := []ir.Var{
		{Name: "PLAIN", Value: ir.Value{Lit: "v"}},
		{Name: "NUM", Value: ir.Value{Lit: "3"}},
		{Name: "TOKEN", Value: ir.Value{Kind: ir.ValueSecret, Secret: "tok"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env = %+v", got)
	}
}

func TestFailureIgnore(t *testing.T) {
	_, j, rep := single(t, "steps:\n  - name: a\n    image: x\n    commands: [y]\n    failure: ignore\n  - name: b\n    image: x\n    commands: [y]\n    failure: cancel\n")
	if !step(t, j, "a").ContinueOnError {
		t.Fatal("failure: ignore must set ContinueOnError")
	}
	b := step(t, j, "b")
	if b.ContinueOnError || ext(b.Ext)["failure"] != "cancel" || !hasEntry(rep, "failure") {
		t.Fatalf("failure: cancel must stay in Ext with a report entry: %+v / %+v", b, rep.Entries)
	}
}

func TestStepFieldsWithoutIRFormStayInExt(t *testing.T) {
	_, j, rep := single(t, `steps:
  - name: a
    image: x
    commands: [y]
    pull: true
    detach: true
    privileged: true
    volumes: ["/a:/b"]
    depends_on: []
    backend_options:
      docker:
        user: "1000"
  - name: b
    image: x
    commands: [y]
    depends_on:
      - a
      - name: c
        optional: true
`)
	a := ext(step(t, j, "a").Ext)
	for _, k := range []string{"pull", "detach", "privileged", "volumes", "depends_on", "backend_options"} {
		if _, ok := a[k]; !ok {
			t.Errorf("key %s not kept in Ext: %v", k, a)
		}
	}
	if !reflect.DeepEqual(a["depends_on"], []any{}) {
		t.Errorf("an empty depends_on means start at once and must stay present: %#v", a["depends_on"])
	}
	b := ext(step(t, j, "b").Ext)
	want := []any{"a", map[string]any{"name": "c", "optional": true}}
	if !reflect.DeepEqual(b["depends_on"], want) {
		t.Errorf("depends_on = %#v", b["depends_on"])
	}
	for _, k := range []string{"pull", "detach", "privileged", "volumes", "depends_on", "backend_options"} {
		if !hasEntry(rep, "key "+k) {
			t.Errorf("no report entry for step key %s", k)
		}
	}
}

func TestDollarHandling(t *testing.T) {
	_, j, rep := single(t, `matrix:
  GO: [one]
steps:
  - name: s
    image: x
    commands:
      - echo $$HOME $${FOO}
      - echo ${CI_COMMIT_BRANCH} ${GO} ${CUSTOM} ${CI_REPO_URL}
      - echo ${CI_COMMIT_TAG##v}
      - echo $PLAIN
`)
	got := step(t, j, "s").Script
	want := []string{
		"echo $HOME ${FOO}",
		"echo ${{ branch }} ${{ matrix.GO }} ${{ env.CUSTOM }} ${{ woodpecker.CI_REPO_URL }}",
		"echo ${CI_COMMIT_TAG##v}",
		"echo $PLAIN",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("script = %q", got)
	}
	if !hasEntry(rep, "${CI_COMMIT_TAG##v}") {
		t.Fatalf("substitution modifier is not reported: %+v", rep.Entries)
	}
}

func TestLiteralTemplateOpenIsEscaped(t *testing.T) {
	_, j, _ := single(t, "steps:\n  - name: s\n    image: x\n    commands:\n      - 'echo $${{ not an expression }}'\n")
	got := step(t, j, "s").Script[0]
	if got != "echo ${{ '${{' }} not an expression }}" {
		t.Fatalf("script = %q", got)
	}
}

func TestServices(t *testing.T) {
	_, j, rep := single(t, `services:
  - name: db
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: pw
    ports: [5432]
    entrypoint: [docker-entrypoint.sh]
    commands: [postgres]
steps:
  - name: s
    image: x
    commands: [y]
`)
	if len(j.Services) != 1 {
		t.Fatalf("services = %+v", j.Services)
	}
	sv := j.Services[0]
	if sv.Name != "db" || sv.Image.Ref != "postgres:16" || !reflect.DeepEqual(sv.Ports, []string{"5432"}) ||
		!reflect.DeepEqual(sv.Image.Entrypoint, []string{"docker-entrypoint.sh"}) ||
		!reflect.DeepEqual(sv.Env, []ir.Var{{Name: "POSTGRES_PASSWORD", Value: ir.Value{Lit: "pw"}}}) {
		t.Fatalf("service = %+v", sv)
	}
	extra, _ := ext(j.Ext)["services"].(map[string]any)
	db, _ := extra["db"].(map[string]any)
	if !reflect.DeepEqual(db["commands"], []any{"postgres"}) || !hasEntry(rep, "service db") {
		t.Fatalf("service commands not kept in Ext with a report entry: %v / %+v", extra, rep.Entries)
	}
}

func TestServicesMapForm(t *testing.T) {
	_, j, _ := single(t, "services:\n  cache:\n    image: redis\nsteps:\n  - name: s\n    image: x\n    commands: [y]\n")
	if len(j.Services) != 1 || j.Services[0].Name != "cache" {
		t.Fatalf("services = %+v", j.Services)
	}
}
