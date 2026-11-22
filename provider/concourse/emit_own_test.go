package concourse_test

import (
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/concourse"
)

const ownPipeline = `resource_types:
  - name: slack
    type: registry-image
    source: {repository: cfcommunity/slack-notification-resource}
resources:
  - name: repo
    type: git
    source:
      uri: https://example.com/r.git
      branch: main
      private_key: ((deploy-key))
      depth: 5
  - name: nightly
    type: time
    source:
      start: "03:00"
      stop: "03:30"
      location: UTC
  - name: image
    type: registry-image
    source:
      repository: acme/app
      tag: latest
jobs:
  - name: build
    serial: true
    plan:
      - get: repo
        trigger: true
      - task: unit
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: golang, tag: "1.22"}
          inputs: [{name: repo}]
          run:
            path: sh
            args: [-ec, "cd repo && go test ./..."]
      - put: image
        params: {image: repo/image.tar}
  - name: nightly-scan
    plan:
      - get: nightly
        trigger: true
      - get: repo
      - task: scan
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: alpine, tag: "3.20"}
          inputs: [{name: repo}]
          run:
            path: sh
            args: [-ec, "echo scan"]
`

func ownEmit(t *testing.T) (map[string]string, map[string]any) {
	t.Helper()
	p, _ := parseFiles(t, map[string]string{"ci.yml": ownPipeline})
	files, _ := emit(t, p)
	out := map[string]string{}
	for n, b := range files {
		out[n] = b
	}
	var d map[string]any
	if err := yaml.Unmarshal([]byte(out[".concourse/ci.yml"]), &d); err != nil {
		t.Fatal(err)
	}
	return out, d
}

func TestOwnReEmitRestoresEveryResourceSource(t *testing.T) {
	var want map[string]any
	if err := yaml.Unmarshal([]byte(ownPipeline), &want); err != nil {
		t.Fatal(err)
	}
	_, got := ownEmit(t)
	for _, name := range []string{"repo", "nightly", "image"} {
		w, g := resourceDoc(want, name), resourceDoc(got, name)
		if g == nil || !reflect.DeepEqual(w, g) {
			t.Fatalf("resource %s\nwant %v\ngot  %v", name, w, g)
		}
	}
	if !reflect.DeepEqual(want["resource_types"], got["resource_types"]) {
		t.Fatalf("resource_types\nwant %v\ngot  %v", want["resource_types"], got["resource_types"])
	}
	if jobDoc(t, got, "build")["serial"] != true {
		t.Fatalf("serial was lost: %v", jobDoc(t, got, "build"))
	}
}

func TestOwnReEmitParsesBackToTheSameIR(t *testing.T) {
	files, _ := ownEmit(t)
	first, _ := parseFiles(t, map[string]string{"ci.yml": ownPipeline})
	back, _ := reparse(t, files)
	if fragment(first) != fragment(back) {
		t.Fatalf("IR changed\nfirst:\n%s\nback:\n%s", fragment(first), fragment(back))
	}
}

func TestOwnReEmitKeepsThePutStep(t *testing.T) {
	_, got := ownEmit(t)
	var put map[string]any
	for _, s := range list(jobDoc(t, got, "build")["plan"]) {
		if m := dict(s); m["put"] != nil {
			put = m
		}
	}
	if put == nil || put["put"] != "image" {
		t.Fatalf("put step lost: %v", jobDoc(t, got, "build")["plan"])
	}
}

func TestPerJobTriggerSetKeepsTheOtherJobsOffTheResource(t *testing.T) {
	_, got := ownEmit(t)
	for _, s := range list(jobDoc(t, got, "nightly-scan")["plan"]) {
		if m := dict(s); m["get"] == "repo" && m["trigger"] == true {
			t.Fatalf("the nightly job triggers on the repo: %v", m)
		}
	}
	var hit bool
	for _, s := range list(jobDoc(t, got, "nightly-scan")["plan"]) {
		if m := dict(s); m["get"] == "nightly" && m["trigger"] == true {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("the nightly job lost its time trigger: %v", jobDoc(t, got, "nightly-scan")["plan"])
	}
}

func TestForeignExtIsReported(t *testing.T) {
	j := scriptJob("build", "make")
	j.Ext = ir.Ext{"gitlab": map[string]any{"cache": "x"}}
	_, rep := emit(t, project(j))
	if !hasFeatureEntry(rep, string(caps.ExtPassthrough), "gitlab") {
		t.Fatalf("no entry for the foreign Ext: %v", rep.Entries)
	}
}

func TestFirstStepCheckoutIsTheSourceGetAndComesBack(t *testing.T) {
	j := scriptJob("build", "make")
	co := &ir.Step{ID: "checkout", Kind: ir.StepAction, Action: &ir.ActionRef{Kind: ir.ActionRepo, Owner: "actions", Repo: "checkout", Ref: "v4"},
		With: map[string]ir.Value{"fetch-depth": {Lit: "1"}}}
	j.Steps = append([]*ir.Step{co}, j.Steps...)
	files, _ := emit(t, project(j))
	d := pipelineDoc(t, files)
	plan := list(jobDoc(t, d, "build")["plan"])
	if g := dict(plan[0]); g["get"] != "source" || dict(g["params"])["depth"] != 1 {
		t.Fatalf("the source get has no depth: %v", plan[0])
	}
	if strings.Contains(files[".concourse/ci.yml"], "task: checkout") {
		t.Fatalf("a task stands for the checkout:\n%s", files[".concourse/ci.yml"])
	}
	back, _ := reparse(t, files)
	if s := back.Pipelines[0].Jobs[0].Steps[0]; s.Kind != ir.StepAction || s.Action == nil || s.Action.Repo != "checkout" {
		t.Fatalf("checkout came back as %+v", s)
	}
	edited := strings.Replace(files[".concourse/ci.yml"], "depth: 1", "depth: 9", 1)
	if edited == files[".concourse/ci.yml"] {
		t.Fatalf("no depth in the file:\n%s", files[".concourse/ci.yml"])
	}
	again, rep := parseFiles(t, map[string]string{"ci.yml": edited})
	for _, s := range again.Pipelines[0].Jobs[0].Steps {
		if s.Kind == ir.StepAction {
			t.Fatalf("an edited get params restored the checkout: %+v", s)
		}
	}
	if !hasFeatureEntry(rep, string(caps.StepAction), "marker ignored") {
		t.Fatalf("no marker-ignored entry: %v", rep.Entries)
	}
}

func TestEmitDoesNotChangeTheInput(t *testing.T) {
	p := project(scriptJob("build", "make"))
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main")}
	ir.Normalize(p)
	before := fragment(p)
	if _, _, err := concourse.New().Emit(p); err != nil {
		t.Fatal(err)
	}
	if fragment(p) != before {
		t.Fatal("Emit changed the project")
	}
}
