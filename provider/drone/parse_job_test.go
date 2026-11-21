package drone_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/drone"
)

func TestJobIDIsNameOrDefault(t *testing.T) {
	pl, _ := parseYML(t, "kind: pipeline\nname: build\nsteps:\n- name: a\n  image: x\n---\nkind: pipeline\nsteps:\n- name: a\n  image: x\n")
	if len(pl.Jobs) != 2 || pl.Jobs[0].ID != "build" || pl.Jobs[1].ID != "default" {
		t.Fatalf("job ids = %v", ids(pl))
	}
}

func ids(pl *ir.Pipeline) []string {
	var out []string
	for _, j := range pl.Jobs {
		out = append(out, j.ID)
	}
	return out
}

func TestMultiDocumentSplitAndTerminator(t *testing.T) {
	yml := "kind: pipeline\nname: a\nsteps:\n- name: s\n  image: x\n---\nkind: pipeline\nname: b\ndepends_on: [a]\nsteps:\n- name: s\n  image: x\n...\nkind: pipeline\nname: c\nsteps:\n- name: s\n  image: x\n"
	pl, _ := parseYML(t, yml)
	if got := ids(pl); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("jobs = %v, want the document after ... dropped", got)
	}
	if !reflect.DeepEqual(pl.Jobs[1].Needs, []ir.Need{{JobID: "a"}}) {
		t.Fatalf("needs = %+v", pl.Jobs[1].Needs)
	}
}

func TestOnePipelineIDDrone(t *testing.T) {
	p, _, err := drone.New().Parse(repo(map[string]string{".drone.yml": minimal}))
	if err != nil {
		t.Fatal(err)
	}
	if p.Pipelines[0].ID != "drone" || p.Origin.Provider != "drone" || !reflect.DeepEqual(p.Origin.Files, []string{".drone.yml"}) {
		t.Fatalf("project = %+v", p)
	}
}

func TestServices(t *testing.T) {
	j, rep := parseJob(t, "kind: pipeline\nname: p\nservices:\n- name: db\n  image: postgres:16\n  ports: [5432]\n  environment:\n    POSTGRES_PASSWORD:\n      from_secret: pw\n    MODE: dev\n  command: [-c, fsync=off]\nsteps:\n- name: s\n  image: x\n")
	if len(j.Services) != 1 {
		t.Fatalf("services = %+v", j.Services)
	}
	sv := j.Services[0]
	if sv.Name != "db" || sv.Image.Ref != "postgres:16" || !reflect.DeepEqual(sv.Ports, []string{"5432"}) {
		t.Fatalf("service = %+v", sv)
	}
	if len(sv.Env) != 2 || sv.Env[0].Value.Secret != "pw" || sv.Env[1].Value.Lit != "dev" {
		t.Fatalf("env = %+v", sv.Env)
	}
	if _, ok := ext(j.Ext)["services"]; !ok || !hasEntry(rep, "service db: key command") {
		t.Fatalf("command must stay in Ext with an entry: ext=%v", j.Ext)
	}
}

func TestPipelineEnvironment(t *testing.T) {
	j, _ := parseJob(t, "kind: pipeline\nname: p\nenvironment:\n  GOFLAGS: -mod=vendor\nsteps:\n- name: s\n  image: x\n")
	if len(j.Env) != 1 || j.Env[0].Name != "GOFLAGS" || j.Env[0].Value.Lit != "-mod=vendor" {
		t.Fatalf("env = %+v", j.Env)
	}
}

func TestPlatformBecomesRunnerLabel(t *testing.T) {
	j, rep := parseJob(t, "kind: pipeline\nname: p\nplatform:\n  os: linux\n  arch: arm64\n  variant: v8\nnode:\n  disk: ssd\nsteps:\n- name: s\n  image: x\n")
	if !reflect.DeepEqual(j.Runner.Labels, []string{"linux/arm64"}) {
		t.Fatalf("labels = %v", j.Runner.Labels)
	}
	if !reflect.DeepEqual(j.Runner.Selector, map[string]string{"disk": "ssd"}) {
		t.Fatalf("selector = %v", j.Runner.Selector)
	}
	if got := ext(j.Ext)["platform"]; !reflect.DeepEqual(got, map[string]any{"variant": "v8"}) || !hasEntry(rep, "platform.variant") {
		t.Fatalf("variant must stay in Ext with an entry: %v", j.Ext)
	}
}

func TestPassthroughKeysKeepExtAndEntry(t *testing.T) {
	j, rep := parseJob(t, "kind: pipeline\nname: p\nversion: 1\nclone:\n  depth: 1\nworkspace:\n  path: /src\nconcurrency:\n  limit: 1\nvolumes:\n- name: tmp\n  temp: {}\nimage_pull_secrets: [reg]\nx-anchors:\n  a: 1\nsteps:\n- name: s\n  image: x\n")
	e := ext(j.Ext)
	for _, k := range []string{"version", "clone", "workspace", "concurrency", "volumes", "image_pull_secrets", "x-anchors"} {
		if _, ok := e[k]; !ok {
			t.Errorf("key %s missing from Ext: %v", k, e)
		}
		if !hasEntry(rep, "key "+k+" has no IR field") {
			t.Errorf("no report entry for %s", k)
		}
	}
}

func TestAnchorsAndMergeKeysResolve(t *testing.T) {
	j, _ := parseJob(t, "x-base: &base\n  image: golang\n  pull: always\nkind: pipeline\nname: p\nsteps:\n- name: s\n  <<: *base\n  commands: [go test]\n")
	s := step(t, j, "s")
	if s.Image == nil || s.Image.Ref != "golang" || ext(s.Ext)["pull"] != "always" {
		t.Fatalf("step = %+v ext=%v", s, s.Ext)
	}
	if _, ok := ext(j.Ext)["x-base"]; !ok {
		t.Fatalf("x-base must stay in Ext: %v", j.Ext)
	}
}

func TestSubstitutionEscape(t *testing.T) {
	j, _ := parseJob(t, "kind: pipeline\nname: p\nsteps:\n- name: s\n  image: x\n  commands:\n  - echo $$HOME $${PATH} cost $$5\n")
	got := step(t, j, "s").Script
	if !reflect.DeepEqual(got, []string{"echo $HOME ${PATH} cost $5"}) {
		t.Fatalf("script = %q, want $$ unescaped to $", got)
	}
}

func TestSubstitutionBuiltinBecomesRef(t *testing.T) {
	j, rep := parseJob(t, "kind: pipeline\nname: p\nsteps:\n- name: s\n  image: x\n  commands:\n  - echo ${DRONE_BRANCH} ${X##v}\n  environment:\n    TAG: ${DRONE_TAG}\n")
	s := step(t, j, "s")
	if len(s.Script) != 1 || !strings.Contains(s.Script[0], "${{ ") || !strings.Contains(s.Script[0], "${X##v}") {
		t.Fatalf("script = %q", s.Script)
	}
	if s.Env[0].Value.Kind != ir.ValueExpr {
		t.Fatalf("env = %+v", s.Env)
	}
	if !hasEntry(rep, "${X##v}") {
		t.Fatal("a kept modifier form must have a report entry")
	}
}

func TestBadShapesAreErrors(t *testing.T) {
	for name, yml := range map[string]string{
		"no kind":         "name: x\nsteps: []\n",
		"no steps":        "kind: pipeline\nname: x\n",
		"steps map":       "kind: pipeline\nname: x\nsteps: {a: {image: x}}\n",
		"unnamed step":    "kind: pipeline\nname: x\nsteps:\n- image: x\n",
		"no image":        "kind: pipeline\nname: x\nsteps:\n- name: a\n",
		"dup step":        "kind: pipeline\nname: x\nsteps:\n- {name: a, image: x}\n- {name: a, image: x}\n",
		"unknown needs":   "kind: pipeline\nname: x\ndepends_on: [nope]\nsteps:\n- {name: a, image: x}\n",
		"scalar document": "hello\n",
	} {
		t.Run(name, func(t *testing.T) { parseErr(t, yml) })
	}
}

func TestOpaqueUsesDroneDialect(t *testing.T) {
	j, _ := parseJob(t, "kind: pipeline\nname: p\nsteps:\n- name: s\n  image: x\n  when:\n    branch:\n      include: main\n")
	o, ok := step(t, j, "s").If.(expr.Opaque)
	if !ok || o.Dialect != "drone" {
		t.Fatalf("if = %#v", step(t, j, "s").If)
	}
}

func TestNonScalarNodeStaysInExt(t *testing.T) {
	j, rep := parseJob(t, "kind: pipeline\nname: p\nnode:\n  disk: [ssd, nvme]\nsteps:\n- name: s\n  image: x\n")
	if j.Runner.Selector != nil {
		t.Fatalf("selector = %v, want none", j.Runner.Selector)
	}
	if _, ok := ext(j.Ext)["node"]; !ok || !hasEntry(rep, "key node has no IR field") {
		t.Fatalf("node must stay in Ext with an entry: %v", j.Ext)
	}
}
