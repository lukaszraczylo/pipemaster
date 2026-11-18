package gitlab_test

import (
	"reflect"
	"strings"
	"testing"
)

func TestAnchorsMergeKeys(t *testing.T) {
	pl, _ := pipeline(t, `
.base: &base
  image: alpine:3
  tags: [docker]
  variables:
    A: "1"

.script: &script
  - echo one
  - echo two

build:
  <<: *base
  script: *script

test:
  <<: [*base, {tags: [other], timeout: 5 minutes}]
  image: ruby:3
  script:
    - *script
    - echo three
`)
	b := job(t, pl, "build")
	if b.Container == nil || b.Container.Ref != "alpine:3" || !reflect.DeepEqual(b.Runner.Labels, []string{"docker"}) {
		t.Errorf("build did not take the merged base: %+v %+v", b.Container, b.Runner)
	}
	if got := b.Steps[0].Script; !reflect.DeepEqual(got, []string{"echo one", "echo two"}) {
		t.Errorf("alias script = %v", got)
	}
	te := job(t, pl, "test")
	if te.Container.Ref != "ruby:3" {
		t.Errorf("explicit key must beat a merged key: image = %v", te.Container.Ref)
	}
	if !reflect.DeepEqual(te.Runner.Labels, []string{"docker"}) {
		t.Errorf("earlier merge source wins: tags = %v", te.Runner.Labels)
	}
	if te.Timeout == nil {
		t.Errorf("later merge source must contribute its own keys")
	}
	if got := te.Steps[0].Script; !reflect.DeepEqual(got, []string{"echo one", "echo two", "echo three"}) {
		t.Errorf("nested script arrays flatten: %v", got)
	}
}

func TestDuplicateKeyIsError(t *testing.T) {
	err := parseErr(t, map[string]string{rootFile: "build:\n  script: [a]\nbuild:\n  script: [b]\n"})
	if !strings.Contains(err.Error(), rootFile+":3") || !strings.Contains(err.Error(), "duplicate key") {
		t.Errorf("error = %v", err)
	}
}

func TestAliasBombIsError(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("a0: &a0 [x, x, x, x, x, x, x, x, x, x]\n")
	for i := 1; i < 12; i++ {
		sb.WriteString("a" + string(rune('0'+i%10)) + string(rune('a'+i)) + ": &b" + string(rune('a'+i)) + " [")
		prev := "*a0"
		if i > 1 {
			prev = "*b" + string(rune('a'+i-1))
		}
		sb.WriteString(strings.Repeat(prev+", ", 10) + "x]\n")
	}
	sb.WriteString("job:\n  script: *b" + string(rune('a'+11)) + "\n")
	err := parseErr(t, map[string]string{rootFile: sb.String()})
	if !strings.Contains(err.Error(), "alias") {
		t.Errorf("error = %v", err)
	}
}

func TestReferenceTag(t *testing.T) {
	pl, _ := pipeline(t, `
.setup:
  script:
    - echo setup
  before_script:
    - echo before

.rules_a:
  - if: $CI_COMMIT_BRANCH == "main"

job:
  before_script:
    - !reference [.setup, before_script]
  script:
    - !reference [.setup, script]
    - echo body
  rules:
    - !reference [.rules_a]
`)
	j := job(t, pl, "job")
	if len(j.Steps) != 2 {
		t.Fatalf("steps = %d", len(j.Steps))
	}
	if got := j.Steps[0].Script; !reflect.DeepEqual(got, []string{"echo before"}) {
		t.Errorf("before = %v", got)
	}
	if got := j.Steps[1].Script; !reflect.DeepEqual(got, []string{"echo setup", "echo body"}) {
		t.Errorf("script = %v", got)
	}
	if j.If == nil {
		t.Errorf("rules via !reference must compile")
	}
}

func TestReferenceCycleIsError(t *testing.T) {
	err := parseErr(t, map[string]string{rootFile: ".a:\n  script:\n    - !reference [.b, script]\n.b:\n  script:\n    - !reference [.a, script]\njob:\n  script:\n    - !reference [.a, script]\n"})
	if !strings.Contains(err.Error(), "reference") {
		t.Errorf("error = %v", err)
	}
}

func TestExtendsHiddenJob(t *testing.T) {
	pl, _ := pipeline(t, `
.tests:
  stage: test
  image: ruby:3.0
  variables:
    URL: "http://my-url.internal"
    IMPORTANT: details
  script: [echo base]
  tags: [production]

rspec:
  extends: .tests
  script: [rake rspec]
  tags: [docker]
  variables:
    URL: "http://docker-url.internal"
`)
	if len(pl.Jobs) != 1 {
		t.Fatalf("hidden jobs are not IR jobs: %d jobs", len(pl.Jobs))
	}
	j := job(t, pl, "rspec")
	if j.Container == nil || j.Container.Ref != "ruby:3.0" {
		t.Errorf("image from parent: %+v", j.Container)
	}
	vals := map[string]string{}
	for _, v := range j.Env {
		vals[v.Name] = v.Value.Lit
	}
	if vals["URL"] != "http://docker-url.internal" || vals["IMPORTANT"] != "details" {
		t.Errorf("hashes merge, child wins: %v", vals)
	}
	if !reflect.DeepEqual(j.Runner.Labels, []string{"docker"}) {
		t.Errorf("arrays replace: tags = %v", j.Runner.Labels)
	}
	if got := j.Steps[0].Script; !reflect.DeepEqual(got, []string{"rake rspec"}) {
		t.Errorf("arrays replace: script = %v", got)
	}
	jx := ext(j.Ext)
	if got, _ := jx["extends"].([]any); !reflect.DeepEqual(got, []any{".tests"}) {
		t.Errorf("job Ext extends = %v", jx["extends"])
	}
	if jx["stage"] != "test" {
		t.Errorf("stage via extends must be visible in Ext: %v", jx["stage"])
	}
	hidden, _ := ext(pl.Ext)["hidden"].(map[string]any)
	if _, ok := hidden[".tests"]; !ok {
		t.Errorf("hidden jobs must be kept in pipeline Ext: %v", ext(pl.Ext))
	}
}

func TestExtendsMultiParentAndChain(t *testing.T) {
	pl, _ := pipeline(t, `
.a:
  image: a
  variables: {V: a, W: a}
.b:
  extends: .a
  variables: {V: b}
.c:
  image: c
job:
  extends: [.b, .c]
  script: [x]
`)
	j := job(t, pl, "job")
	if j.Container.Ref != "c" {
		t.Errorf("later parent wins: %v", j.Container.Ref)
	}
	vals := map[string]string{}
	for _, v := range j.Env {
		vals[v.Name] = v.Value.Lit
	}
	if vals["V"] != "b" || vals["W"] != "a" {
		t.Errorf("env = %v", vals)
	}
}

func TestExtendsNullExcludesKey(t *testing.T) {
	pl, _ := pipeline(t, `
.base:
  script: [x]
  variables:
    V: 1
job:
  extends: .base
  variables: null
`)
	if j := job(t, pl, "job"); len(j.Env) != 0 {
		t.Errorf("null excludes the key: env = %v", j.Env)
	}
}

func TestExtendsRealJob(t *testing.T) {
	pl, _ := pipeline(t, "base:\n  image: x\n  script: [a]\nchild:\n  extends: base\n  script: [b]\n")
	if job(t, pl, "child").Container.Ref != "x" {
		t.Errorf("extending a non-hidden job must work")
	}
}

func TestExtendsErrors(t *testing.T) {
	for name, yml := range map[string]string{
		"missing": "job:\n  extends: .nope\n  script: [x]\n",
		"cycle":   ".a:\n  extends: .b\n.b:\n  extends: .a\njob:\n  extends: .a\n  script: [x]\n",
		"self":    "job:\n  extends: job\n  script: [x]\n",
	} {
		t.Run(name, func(t *testing.T) {
			err := parseErr(t, map[string]string{rootFile: yml})
			if !strings.Contains(err.Error(), rootFile+":") || !strings.Contains(err.Error(), "extends") {
				t.Errorf("error = %v", err)
			}
		})
	}
}

func TestExtendsTooDeep(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 13; i++ {
		sb.WriteString(".h" + string(rune('a'+i)) + ":\n")
		if i > 0 {
			sb.WriteString("  extends: .h" + string(rune('a'+i-1)) + "\n")
		}
		sb.WriteString("  image: x\n")
	}
	sb.WriteString("job:\n  extends: .hm\n  script: [x]\n")
	err := parseErr(t, map[string]string{rootFile: sb.String()})
	if !strings.Contains(err.Error(), "levels") {
		t.Errorf("error = %v", err)
	}
}
