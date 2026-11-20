package woodpecker_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func combo(kv ...string) ir.Combo {
	var c ir.Combo
	for i := 0; i+1 < len(kv); i += 2 {
		c = append(c, ir.KV{Key: kv[i], Value: kv[i+1]})
	}
	return c
}

func TestMatrixAxes(t *testing.T) {
	_, j, _ := single(t, `matrix:
  GO: [1.21, 1.22]
  OS: [linux, "mac"]
steps:
  - name: s
    image: golang:${GO}
    commands: ['echo ${OS}']
    environment:
      V: ${GO}-x
`)
	want := &ir.Matrix{Axes: []ir.Axis{{Name: "GO", Values: []string{"1.21", "1.22"}}, {Name: "OS", Values: []string{"linux", "mac"}}}}
	if !reflect.DeepEqual(j.Matrix, want) {
		t.Fatalf("matrix = %+v", j.Matrix)
	}
	s := step(t, j, "s")
	if s.Image.Ref != "golang:${{ matrix.GO }}" || s.Script[0] != "echo ${{ matrix.OS }}" {
		t.Fatalf("matrix refs not rewritten: %+v %v", s.Image, s.Script)
	}
	if s.Env[0].Value.Kind != ir.ValueExpr {
		t.Fatalf("env value with a matrix ref must be an expression: %+v", s.Env[0])
	}
}

func TestMatrixInclude(t *testing.T) {
	_, j, rep := single(t, "matrix:\n  include:\n    - {GO: 1.21, DB: pg}\n    - {GO: 1.22, DB: my}\nsteps:\n  - name: s\n    image: x\n    commands: [y]\n")
	if len(j.Matrix.Axes) != 0 || !reflect.DeepEqual(j.Matrix.Include, []ir.Combo{combo("DB", "pg", "GO", "1.21"), combo("DB", "my", "GO", "1.22")}) {
		t.Fatalf("matrix = %+v", j.Matrix)
	}
	if len(rep.Entries) != 0 {
		t.Fatalf("unexpected report entries: %+v", rep.Entries)
	}
}

func TestMatrixAxesNextToIncludeAreKept(t *testing.T) {
	_, j, rep := single(t, "matrix:\n  GO: [1.21]\n  include:\n    - {GO: 1.22}\nsteps:\n  - name: s\n    image: x\n    commands: [y]\n")
	if len(j.Matrix.Axes) != 0 || len(j.Matrix.Include) != 1 {
		t.Fatalf("matrix = %+v", j.Matrix)
	}
	if _, ok := ext(j.Ext)["matrix"]; !ok || !hasEntry(rep, "matrix") {
		t.Fatalf("ignored axes must stay in Ext with a report entry: %v %+v", j.Ext, rep.Entries)
	}
}

func TestLabelsPlatform(t *testing.T) {
	_, j, rep := single(t, "labels:\n  platform: linux/arm64\n  backend: docker\nsteps:\n  - name: s\n    image: x\n    commands: [y]\n")
	if !reflect.DeepEqual(j.Runner.Labels, []string{"linux/arm64"}) {
		t.Fatalf("runner = %+v", j.Runner)
	}
	labels, _ := ext(j.Ext)["labels"].(map[string]any)
	if !reflect.DeepEqual(labels, map[string]any{"backend": "docker"}) || !hasEntry(rep, "labels.backend") {
		t.Fatalf("other labels must stay in Ext: %v %+v", j.Ext, rep.Entries)
	}
}

func TestWorkflowKeysWithoutIRFormStayInExt(t *testing.T) {
	_, j, rep := single(t, `skip_clone: true
workspace:
  base: /w
  path: src
concurrency:
  limit: 1
  group: g
clone:
  git:
    settings:
      depth: 1
steps:
  - name: s
    image: x
    commands: [y]
`)
	e := ext(j.Ext)
	for _, k := range []string{"skip_clone", "workspace", "concurrency", "clone"} {
		if _, ok := e[k]; !ok {
			t.Errorf("key %s not kept in Ext: %v", k, e)
		}
		if !hasEntry(rep, "key "+k) {
			t.Errorf("no report entry for %s", k)
		}
	}
}

func TestRunsOnReadsAsStatus(t *testing.T) {
	_, j, _ := single(t, "runs_on: [success, failure]\nsteps:\n  - name: s\n    image: x\n    commands: [y]\n")
	if text(j.If) != "always()" {
		t.Fatalf("If = %q", text(j.If))
	}
}

func TestMarshalRoundTripKeepsExt(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{".woodpecker.yaml": `labels:
  platform: linux/arm64
  backend: docker
matrix:
  GO: [1.21]
when:
  - event: push
    platform: linux/*
services:
  - name: db
    image: postgres
    commands: [x]
steps:
  - name: a
    image: plugin
    settings:
      tags: [a, b]
      n: 3
    depends_on: []
    detach: true
`})
	b, err := ir.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ir.Unmarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if ok, diff := ir.Equal(p, back); !ok {
		t.Fatalf("round trip differs: %s", diff)
	}
}
