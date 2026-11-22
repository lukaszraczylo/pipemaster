package validate_test

import (
	"os"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"github.com/lukaszraczylo/pipemaster/validate"
	"github.com/lukaszraczylo/pipemaster/validate/schemas"
)

const concourseTestID = "concourse-schema-test"

func init() {
	validate.Register(concourseTestID, schemas.Concourse, []string{".concourse/*.yml", ".concourse/*.yaml"})
}

const (
	concourseCorpusFile = "../testdata/concourse/capi-ci-cve-pipeline.yml"

	// concourseAllSteps holds every step type, modifier and hook, and an opaque source with an unknown key.
	concourseAllSteps = `resource_types:
- name: pr
  type: registry-image
  source: {repository: example/pr-resource}
  privileged: true
  check_every: 24h
  defaults: {v3_endpoint: "https://api.example.com"}
resources:
- name: repo
  type: git
  icon: git
  check_every: 5m
  webhook_token: ((hook))
  source:
    uri: https://example.com/r.git
    branch: main
    some_future_key: [1, 2]
- name: nightly
  type: time
  source: {interval: 24h}
- name: pull
  type: pr
  source: {repository: a/b}
jobs:
- name: build
  serial: true
  max_in_flight: 1
  build_log_retention: {builds: 10, days: 3, minimum_succeeded_builds: 1}
  on_failure:
    task: notify
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run: {path: echo, args: [failed]}
  ensure:
    do:
    - task: cleanup
      file: repo/ci/cleanup.yml
  plan:
  - in_parallel:
      limit: 2
      fail_fast: true
      steps:
      - get: repo
        trigger: true
        version: every
        passed: [other]
        tags: [t]
      - get: nightly
        trigger: true
  - get: pull
  - load_var: sha
    file: repo/.git/ref
    format: trim
    reveal: true
  - try:
      task: flaky
      timeout: 5m
      attempts: 3
      privileged: true
      file: repo/ci/flaky.yml
      input_mapping: {src: repo}
      output_mapping: {out: result}
      params: {A: b, N: 3}
      vars: {k: v}
      container_limits: {cpu: 512, memory: 1GB}
      image: img
      on_success:
        put: repo
        params: {repository: out}
        inputs: [out]
        get_params: {depth: 1}
        no_get: true
  - across:
    - var: v
      values: [a, b]
      max_in_flight: all
    fail_fast: true
    do:
    - set_pipeline: child
      file: repo/ci/child.yml
      vars: {x: ((.:v))}
      instance_vars: {y: ((.:v))}
      var_files: [repo/vars.yml]
      team: main
  - do:
    - put: repo
      inputs: detect
  - task: inline
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      inputs: [{name: repo, path: src, optional: true}]
      outputs: [{name: out}]
      caches: [{path: cache, ttl: 1h}]
      params: {X: y}
      run: {path: sh, args: [-c, "true"], dir: src, user: nobody}
      container_limits: {cpu: 1}
- name: other
  plan:
  - get: repo
  - get: nightly
    trigger: true
groups:
- name: all
  jobs: ["*"]
var_sources:
- name: vault
  type: vault
  config: {url: "https://vault.example.com", client_token: t}
display: {background_image: "https://example.com/i.png"}
user_data: {anything: [1]}
`
	concourseBogusStep = `resources:
- name: repo
  type: git
  source: {uri: https://example.com/r.git}
jobs:
- name: build
  plan:
  - get: repo
    bogus: 1
`
	concourseUnusedResource = `resources:
- name: repo
  type: git
  source: {uri: https://example.com/r.git}
- name: x
  type: git
  source: {uri: https://example.com/x.git}
jobs:
- name: build
  plan:
  - get: repo
    trigger: true
`
)

func readCorpus(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(concourseCorpusFile)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConcourseCorpusPassesSchemaAndFly(t *testing.T) {
	data := readCorpus(t)
	if err := validate.File(concourseTestID, "cve-pipeline.yml", data); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if out, err := linttool.FlyLint(t, "cve-pipeline.yml", data); err != nil {
		t.Fatalf("fly rejected the corpus file: %v\n%s", err, out)
	}
}

func TestConcourseEveryStepTypePassesSchemaAndFly(t *testing.T) {
	if err := validate.File(concourseTestID, "all.yml", []byte(concourseAllSteps)); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if out, err := linttool.FlyLint(t, "all.yml", []byte(concourseAllSteps)); err != nil {
		t.Fatalf("fly: %v\n%s", err, out)
	}
}

func TestConcourseBogusStepKeyFailsSchemaAndFly(t *testing.T) {
	if err := validate.File(concourseTestID, "p.yml", []byte(concourseBogusStep)); err == nil {
		t.Fatal("schema accepted an unknown step key")
	}
	out, err := linttool.FlyLint(t, "p.yml", []byte(concourseBogusStep))
	if err == nil {
		t.Fatalf("fly accepted an unknown step key:\n%s", out)
	}
	if !strings.Contains(out, "bogus") {
		t.Errorf("fly output does not name the key:\n%s", out)
	}
}

func TestConcourseUnusedResourceFailsFlyOnly(t *testing.T) {
	out, err := linttool.FlyLint(t, "p.yml", []byte(concourseUnusedResource))
	if err == nil {
		t.Fatalf("fly accepted an unused resource:\n%s", out)
	}
	if !strings.Contains(out, "resource 'x' is not used") {
		t.Errorf("fly output lacks the unused resource error:\n%s", out)
	}
	if err := validate.File(concourseTestID, "p.yml", []byte(concourseUnusedResource)); err != nil {
		t.Fatalf("a reference between objects is not a schema rule: %v", err)
	}
}

func TestConcourseSchemaRejects(t *testing.T) {
	base := func(plan, extra string) string {
		return "resources:\n- {name: r, type: git, source: {uri: u}}\n" + extra + "jobs:\n- name: j\n  plan:\n" + plan
	}
	cases := map[string]string{
		"step without a kind":         base("  - trigger: true\n", ""),
		"two step kinds":              base("  - get: r\n    put: r\n", ""),
		"get with a task key":         base("  - get: r\n    privileged: true\n", ""),
		"task without file or config": base("  - task: t\n", ""),
		"task with both":              base("  - task: t\n    file: r/t.yml\n    config: {platform: linux}\n", ""),
		"task config typo":            base("  - task: t\n    config: {platform: linux, ouputs: []}\n", ""),
		"task config no platform":     base("  - task: t\n    config: {run: {path: x}}\n", ""),
		"fail_fast without across":    base("  - get: r\n    fail_fast: true\n", ""),
		"privileged as a string":      base("  - task: t\n    file: r/t.yml\n    privileged: fuse-only\n", ""),
		"bad get version":             base("  - get: r\n    version: newest\n", ""),
		"bad load_var format":         base("  - load_var: v\n    file: r/f\n    format: toml\n", ""),
		"unknown hook key":            base("  - get: r\n    on_finish: {get: r}\n", ""),
		"nested bad step":             base("  - do:\n    - get: r\n      nope: 1\n", ""),
		"in_parallel bad key":         base("  - in_parallel: {steps: [], nope: 1}\n", ""),
		"across without var":          base("  - get: r\n    across: [{values: [a]}]\n", ""),
		"unknown top-level key":       base("  - get: r\n", "") + "bogus: 1\n",
		"unknown job key":             "jobs:\n- name: j\n  bogus: 1\n  plan: []\n",
		"unknown resource key":        "resources:\n- {name: r, type: git, source: {}, bogus: 1}\njobs:\n- {name: j, plan: []}\n",
		"no jobs":                     "resources: []\n",
		"upper-case job name":         "jobs:\n- name: Build\n  plan: []\n",
		"upper-case resource name":    "resources:\n- {name: Repo, type: git, source: {}}\njobs:\n- {name: j, plan: []}\n",
		"job name with a space":       "jobs:\n- name: a b\n  plan: []\n",
		"var source type":             base("  - get: r\n", "") + "var_sources:\n- {name: v, type: nope, config: {}}\n",
	}
	for name, y := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validate.File(concourseTestID, "p.yml", []byte(y)); err == nil {
				t.Fatalf("schema accepted:\n%s", y)
			}
		})
	}
}

func TestConcourseSourceKeysAreOpaque(t *testing.T) {
	y := "resources:\n- {name: r, type: git, source: {uri: u, bogus_key: [1, {a: b}]}}\njobs:\n- {name: j, plan: [{get: r}]}\n"
	if err := validate.File(concourseTestID, "p.yml", []byte(y)); err != nil {
		t.Fatalf("unknown source keys must pass: %v", err)
	}
}

func TestConcourseFlyStrictRejectsUnknownResourceKey(t *testing.T) {
	y := "resources:\n- {name: r, type: git, source: {uri: u}, bogus_key: 1}\njobs:\n- {name: j, plan: [{get: r}]}\n"
	if out, err := linttool.FlyLint(t, "p.yml", []byte(y)); err == nil {
		t.Fatalf("fly --strict accepted an unknown resource key:\n%s", out)
	}
}

func TestConcourseFlyRejectsDuplicateKeyAndAcceptsAnchors(t *testing.T) {
	dup := "resources:\n- {name: r, type: git, source: {uri: u}}\njobs:\n- name: j\n  plan:\n  - get: r\n    passed: [a]\n    passed: [b]\n"
	if out, err := linttool.FlyLint(t, "p.yml", []byte(dup)); err == nil || !strings.Contains(out, "already defined") {
		t.Fatalf("fly must reject a duplicate key: %v\n%s", err, out)
	}
	anchors := "resources:\n- {name: r1, type: git, source: &s {uri: u}}\n- {name: r2, type: git, source: &s {uri: v}}\n" +
		"jobs:\n- name: j\n  plan:\n  - get: r1\n  - get: r2\n"
	if out, err := linttool.FlyLint(t, "p.yml", []byte(anchors)); err != nil {
		t.Fatalf("fly must accept a repeated anchor: %v\n%s", err, out)
	}
}
