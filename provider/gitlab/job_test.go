package gitlab_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
	"github.com/lukaszraczylo/pipemaster/report"
)

func TestScriptsAreCanonicalTemplates(t *testing.T) {
	pl, _ := pipeline(t, "j:\n  before_script:\n    - echo ${CI_COMMIT_BRANCH}\n  script:\n    - echo $CI_COMMIT_SHA $HOME ${HOME} $$CI_COMMIT_SHA\n    - echo '${{ x }}' $CI_COMMIT_SHA_SHORT\n    - |\n      if [ -n \"$CI_COMMIT_TAG\" ]; then\n        echo tagged\n      fi\n  after_script:\n    - echo done $GITLAB_USER_LOGIN\n")
	j := job(t, pl, "j")
	if len(j.Steps) != 2 {
		t.Fatalf("steps = %d, want before_script and script in one step plus after_script", len(j.Steps))
	}
	want := []string{
		"echo ${{ branch }}",
		"echo ${{ sha }} $HOME ${HOME} $$CI_COMMIT_SHA",
		"echo '${{ '${{' }} x }}' $CI_COMMIT_SHA_SHORT",
		`if [ -n "${{ tag }}" ]; then`,
		"  echo tagged",
		"fi",
	}
	s0 := j.Steps[0]
	if s0.ID != "script" || !reflect.DeepEqual(s0.Script, want) {
		t.Errorf("script step = %+v\nwant lines %q", s0, want)
	}
	if ext(s0.Ext)["before_lines"] != 1 {
		t.Errorf("split index = %v, want 1", ext(s0.Ext)["before_lines"])
	}
	a := j.Steps[1]
	if a.ID != "after_script" || !reflect.DeepEqual(a.Script, []string{"echo done ${{ actor }}"}) {
		t.Errorf("after = %+v", a)
	}
	if got := expr.String(a.If); got != "always()" || !a.ContinueOnError {
		t.Errorf("after_script runs on failure and ignores its status: If=%s continue=%v", got, a.ContinueOnError)
	}
	if ext(a.Ext)["section"] != "after_script" {
		t.Errorf("after_script section = %v", a.Ext)
	}
	if s0.If != nil || s0.ContinueOnError {
		t.Errorf("the script step is unconditional and failing")
	}
}

func TestBeforeScriptOnlyAndNone(t *testing.T) {
	pl, _ := pipeline(t, "a:\n  before_script: [echo b]\nb:\n  script: [echo s]\n")
	a := job(t, pl, "a")
	if len(a.Steps) != 1 || ext(a.Steps[0].Ext)["before_lines"] != 1 {
		t.Errorf("before_script only: %+v", a.Steps)
	}
	if b := job(t, pl, "b"); ext(b.Steps[0].Ext)["before_lines"] != nil {
		t.Errorf("no before_script, no split index: %v", b.Steps[0].Ext)
	}
}

func TestScriptEntryMustBeString(t *testing.T) {
	err := parseErr(t, map[string]string{rootFile: "j:\n  script:\n    - echo: hi\n"})
	if !strings.Contains(err.Error(), rootFile+":3") {
		t.Errorf("error = %v", err)
	}
}

func TestDefaultApplied(t *testing.T) {
	pl, _ := pipeline(t, `
default:
  image: ruby:3
  retry: 2
  tags: [shared]
  before_script: [echo default]
  interruptible: true
a:
  script: [x]
b:
  script: [x]
  image: alpine
  retry: 0
c:
  script: [x]
  inherit:
    default: false
d:
  script: [x]
  inherit:
    default: [image]
`)
	a := job(t, pl, "a")
	if a.Container.Ref != "ruby:3" || a.Retry == nil || a.Retry.Attempts != 3 || !reflect.DeepEqual(a.Runner.Labels, []string{"shared"}) || len(a.Steps) != 1 || len(a.Steps[0].Script) != 2 {
		t.Errorf("a did not inherit: %+v retry=%+v", a, a.Retry)
	}
	if ext(a.Ext)["interruptible"] != true {
		t.Errorf("inherited interruptible must reach Ext: %v", a.Ext)
	}
	b := job(t, pl, "b")
	if b.Container.Ref != "alpine" || b.Retry != nil {
		t.Errorf("job values replace defaults: %+v retry=%+v", b.Container, b.Retry)
	}
	c := job(t, pl, "c")
	if c.Container != nil || c.Retry != nil || len(c.Runner.Labels) != 0 {
		t.Errorf("inherit false: %+v", c)
	}
	if ext(c.Ext)["inherit"] != nil {
		t.Errorf("inherit:default is applied, so it is not kept: %v", c.Ext)
	}
	d := job(t, pl, "d")
	if d.Container == nil || d.Retry != nil {
		t.Errorf("inherit list: %+v", d)
	}
	if ext(pl.Ext) != nil {
		t.Errorf("default is expanded into the jobs, so Ext keeps nothing: %v", pl.Ext)
	}
}

func TestDeprecatedGlobalsAreDefaults(t *testing.T) {
	pl, _ := pipeline(t, "image: node:20\nbefore_script: [echo hi]\nj:\n  script: [x]\n")
	j := job(t, pl, "j")
	if j.Container == nil || j.Container.Ref != "node:20" || len(j.Steps) != 1 || len(j.Steps[0].Script) != 2 {
		t.Errorf("job = %+v", j)
	}
	if ext(pl.Ext) != nil {
		t.Errorf("a global image is a default and is expanded into the jobs: %v", pl.Ext)
	}
}

func TestFieldMapping(t *testing.T) {
	pl, _ := pipeline(t, `
variables:
  GLOBAL: g
  RICH:
    value: v
    description: shown
j:
  script: [x]
  image:
    name: alpine:3
    entrypoint: [""]
    pull_policy: always
  services:
    - postgres:15
    - name: redis:7
      alias: cache
      command: [serve]
  tags: [docker, linux]
  timeout: 1h 30m
  retry:
    max: 2
    when: [runner_system_failure, api_failure]
    exit_codes: 137
  allow_failure: true
  environment:
    name: prod
    url: https://x.example
    action: start
  resource_group: deploy
  variables:
    JOB: $CI_COMMIT_SHA
  cache:
    key: k-$CI_COMMIT_REF_SLUG
    paths: [vendor/]
    policy: pull
  artifacts:
    name: out
    paths: [dist/]
    expire_in: 1 week
    reports:
      junit: r.xml
`)
	if len(pl.Env) != 2 || pl.Env[1].Value.Lit != "v" {
		t.Errorf("pipeline env = %+v", pl.Env)
	}
	if ext(pl.Ext)["variables"].(map[string]any)["RICH"] == nil {
		t.Errorf("variable extras: %v", pl.Ext)
	}
	j := job(t, pl, "j")
	if j.Container.Ref != "alpine:3" || !reflect.DeepEqual(j.Container.Entrypoint, []string{""}) {
		t.Errorf("container = %+v", j.Container)
	}
	if ext(j.Ext)["image"].(map[string]any)["pull_policy"] != "always" {
		t.Errorf("image extras: %v", j.Ext)
	}
	if len(j.Services) != 2 || j.Services[0].Name != "postgres" || j.Services[1].Name != "cache" || j.Services[1].Image.Ref != "redis:7" {
		t.Errorf("services = %+v", j.Services)
	}
	if got := gitlab.DerivedServiceName("registry.example.com/grp/img:1.2"); got != "img" {
		t.Errorf("DerivedServiceName = %q", got)
	}
	if !reflect.DeepEqual(j.Runner.Labels, []string{"docker", "linux"}) {
		t.Errorf("labels = %v", j.Runner.Labels)
	}
	if j.Timeout == nil || time.Duration(*j.Timeout) != 90*time.Minute {
		t.Errorf("timeout = %v", j.Timeout)
	}
	if j.Retry == nil || j.Retry.Attempts != 3 || !reflect.DeepEqual(j.Retry.On, []string{"runner_system_failure", "api_failure"}) {
		t.Errorf("retry = %+v", j.Retry)
	}
	if !reflect.DeepEqual(ext(j.Ext)["retry"], map[string]any{"exit_codes": 137}) {
		t.Errorf("retry extras = %v", ext(j.Ext)["retry"])
	}
	if !j.AllowFailure {
		t.Errorf("allow_failure")
	}
	if j.Environment == nil || j.Environment.Name != "prod" || j.Environment.URL != "https://x.example" || ext(j.Ext)["environment"].(map[string]any)["action"] != "start" {
		t.Errorf("environment = %+v ext=%v", j.Environment, j.Ext)
	}
	if ext(j.Ext)["resource_group"] != "deploy" {
		t.Errorf("resource_group must stay in Ext")
	}
	if len(j.Env) != 1 || j.Env[0].Value.Kind != ir.ValueExpr || expr.String(j.Env[0].Value.Expr) != "sha" {
		t.Errorf("a predefined variable in job env is a canonical ref: %+v", j.Env)
	}
	if len(j.Cache) != 1 || j.Cache[0].Key != "k-$CI_COMMIT_REF_SLUG" || j.Cache[0].Policy != "pull" || !reflect.DeepEqual(j.Cache[0].Paths, []string{"vendor/"}) {
		t.Errorf("cache = %+v", j.Cache)
	}
	if len(j.Artifacts) != 1 || j.Artifacts[0].Name != "out" || j.Artifacts[0].Expire != "1 week" {
		t.Errorf("artifacts = %+v", j.Artifacts)
	}
	if ext(j.Ext)["artifacts"].(map[string]any)["reports"] == nil {
		t.Errorf("artifact reports stay in Ext")
	}
}

func TestCacheKeyFiles(t *testing.T) {
	pl, _ := pipeline(t, "j:\n  script: [x]\n  cache:\n    - key:\n        files: [Gemfile.lock, package.json]\n        prefix: deps\n      paths: [vendor]\n    - key: plain\n      paths: [x]\n")
	j := job(t, pl, "j")
	if got := j.Cache[0].Key; got != "deps-${{ hashFiles('Gemfile.lock', 'package.json') }}" {
		t.Errorf("derived key = %q", got)
	}
	raw := ext(j.Ext)["cache"].([]any)
	if len(raw) != 2 || raw[0].(map[string]any)["key"] == nil {
		t.Errorf("raw key form must stay in Ext: %v", raw)
	}
	if j.Cache[1].Key != "plain" {
		t.Errorf("cache 1 = %+v", j.Cache[1])
	}
}

func TestParallelMatrix(t *testing.T) {
	pl, _ := pipeline(t, `
one:
  script: [x]
  parallel:
    matrix:
      - OS: [linux, mac]
        ARCH: [amd64, arm64]
many:
  script: [x]
  parallel:
    matrix:
      - PROVIDER: aws
        STACK: [monitoring, app1]
      - PROVIDER: [gcp, vultr]
        STACK: [data]
count:
  script: [x]
  parallel: 5
`)
	one := job(t, pl, "one")
	if one.Matrix == nil || !reflect.DeepEqual(one.Matrix.Axes, []ir.Axis{{Name: "OS", Values: []string{"linux", "mac"}}, {Name: "ARCH", Values: []string{"amd64", "arm64"}}}) || len(one.Matrix.Include) != 0 {
		t.Errorf("single entry = %+v", one.Matrix)
	}
	many := job(t, pl, "many")
	if len(many.Matrix.Axes) != 0 || len(many.Matrix.Include) != 4 {
		t.Fatalf("union entry = %+v", many.Matrix)
	}
	if got := ir.Expand(many.Matrix); len(got) != 4 {
		t.Errorf("Expand = %v", got)
	}
	c := job(t, pl, "count")
	if c.Matrix != nil || ext(c.Ext)["parallel"] != 5 {
		t.Errorf("parallel: N stays in Ext: %+v %v", c.Matrix, c.Ext)
	}
}

func TestTimeoutFormats(t *testing.T) {
	cases := map[string]time.Duration{
		"3600 seconds": time.Hour, "60 minutes": time.Hour, "one hour": time.Hour,
		"3 hours 30 minutes": 210 * time.Minute, "3h 30m": 210 * time.Minute, "1h30m": 90 * time.Minute,
		"30": 30 * time.Second, "2 days": 48 * time.Hour,
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			pl, _ := pipeline(t, "j:\n  script: [x]\n  timeout: "+in+"\n")
			if j := job(t, pl, "j"); j.Timeout == nil || time.Duration(*j.Timeout) != want {
				t.Errorf("timeout = %v, want %v", j.Timeout, want)
			}
		})
	}
	pl, rep := pipeline(t, "j:\n  script: [x]\n  timeout: soon\n")
	if j := job(t, pl, "j"); j.Timeout != nil || ext(j.Ext)["timeout"] != "soon" {
		t.Errorf("unparsable timeout stays in Ext: %+v", j)
	}
	if !hasEntry(rep, "timeout soon") {
		t.Errorf("report = %+v", rep.Entries)
	}
}

func TestRetryForms(t *testing.T) {
	pl, _ := pipeline(t, "a:\n  script: [x]\n  retry: 2\nb:\n  script: [x]\n  retry: 0\nc:\n  script: [x]\n  retry:\n    max: 1\n")
	if r := job(t, pl, "a").Retry; r == nil || r.Attempts != 3 {
		t.Errorf("retry: 2 is three runs: %+v", r)
	}
	if job(t, pl, "b").Retry != nil {
		t.Errorf("retry: 0 is no retry")
	}
	if r := job(t, pl, "c").Retry; r == nil || r.Attempts != 2 {
		t.Errorf("max 1 is two runs: %+v", r)
	}
}

func TestAllowFailureExitCodes(t *testing.T) {
	pl, rep := pipeline(t, "j:\n  script: [x]\n  allow_failure:\n    exit_codes: [137]\n")
	j := job(t, pl, "j")
	if j.AllowFailure || ext(j.Ext)["allow_failure"] == nil {
		t.Errorf("exit_codes form: %+v", j)
	}
	if !hasEntry(rep, "allow_failure:exit_codes") {
		t.Errorf("report = %+v", rep.Entries)
	}
}

func TestUnknownKeysEveryLevel(t *testing.T) {
	pl, _ := pipeline(t, `
variables:
  A: b
j:
  script: [x]
  weird_job_key: [1, 2]
  trigger:
    include: child.yml
  image:
    name: a
    docker:
      platform: linux/arm64
  environment:
    name: e
    deployment_tier: production
  cache:
    key: k
    paths: [p]
    untracked: true
  services:
    - name: pg
      weird: yes
  artifacts:
    paths: [a]
    untracked: true
`)
	x := ext(job(t, pl, "j").Ext)
	for _, k := range []string{"weird_job_key", "trigger", "image", "environment", "cache", "services", "artifacts"} {
		if x[k] == nil {
			t.Errorf("Ext[gitlab][%s] missing: %v", k, x)
		}
	}
	for _, k := range []string{"script", "variables", "tags"} {
		if _, bad := x[k]; bad {
			t.Errorf("consumed key %s must not be in Ext", k)
		}
	}
	if ext(pl.Ext) != nil {
		t.Errorf("a clean pipeline has no pipeline Ext: %v", pl.Ext)
	}
}

func TestPipelineIdentity(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{rootFile: "\"build linux\":\n  script: [x]\n"})
	if p.Pipelines[0].ID != "gitlab-ci" || p.Origin.Provider != "gitlab" || job(t, p.Pipelines[0], "build linux") == nil {
		t.Errorf("project = %+v", p)
	}
}

func TestSyntaxErrorHasLine(t *testing.T) {
	err := parseErr(t, map[string]string{rootFile: "a:\n  script: [x\n"})
	if !strings.Contains(err.Error(), rootFile+":") {
		t.Errorf("error = %v", err)
	}
}

func TestNonMappingJobIsError(t *testing.T) {
	err := parseErr(t, map[string]string{rootFile: "a: 1\n"})
	if !strings.Contains(err.Error(), rootFile+":1") {
		t.Errorf("error = %v", err)
	}
}

func TestDetectAndRegistered(t *testing.T) {
	p := gitlab.New()
	if !p.Detect(repo(map[string]string{rootFile: "a:\n  script: [x]\n"})) || p.Detect(repo(map[string]string{"x": "y"})) {
		t.Errorf("Detect")
	}
	if got, err := provider.Get("gitlab"); err != nil || got.ID() != "gitlab" {
		t.Errorf("registry: %v %v", got, err)
	}
}

func TestCapsDeclared(t *testing.T) {
	m := gitlab.New().Caps()
	if m["trigger.schedule"].Level.String() != "external" || m["job.needs"].Level.String() != "native" {
		t.Errorf("caps = %+v", m)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"a:\n  script: [x]\n",
		"stages: [a, b]\na:\n  stage: a\n  script: x\nb:\n  stage: b\n  needs: [a]\n",
		".h: &h\n  image: x\nj:\n  <<: *h\n  extends: .h\n  rules:\n    - if: $CI_COMMIT_BRANCH == \"x\"\n",
		"workflow:\n  rules:\n    - if: $CI_COMMIT_TAG =~ /^v/\ninclude: [a.yml, {remote: http://x}]\n",
		"j:\n  parallel:\n    matrix:\n      - A: [1, 2]\n",
		"j:\n  script:\n    - !reference [.a, script]\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(_ *testing.T, src string) {
		_, _, _ = gitlab.New().Parse(repo(map[string]string{rootFile: src, "a.yml": src}))
	})
}

func TestVariablesCanonical(t *testing.T) {
	pl, rep := pipeline(t, `
variables:
  PLAIN: text
  ONE: $CI_COMMIT_SHA
  BRACED: ${CI_COMMIT_BRANCH}
  MIXED: "img:$CI_COMMIT_SHA-${CI_COMMIT_BRANCH} {x}"
  UNKNOWN: $OTHER_VAR
  ESC: $$CI_COMMIT_SHA
j:
  script: [x]
`)
	by := map[string]ir.Value{}
	for _, v := range pl.Env {
		by[v.Name] = v.Value
	}
	if by["PLAIN"].Kind != ir.ValueLit || by["PLAIN"].Lit != "text" {
		t.Errorf("PLAIN = %+v", by["PLAIN"])
	}
	if by["ONE"].Kind != ir.ValueExpr || expr.String(by["ONE"].Expr) != "sha" || by["BRACED"].Kind != ir.ValueExpr || expr.String(by["BRACED"].Expr) != "branch" {
		t.Errorf("single refs = %+v %+v", by["ONE"], by["BRACED"])
	}
	m := by["MIXED"]
	if m.Kind != ir.ValueExpr || expr.String(m.Expr) != "format('img:{0}-{1} {{x}}', sha, branch)" {
		t.Errorf("MIXED = %+v (%s)", m, expr.String(m.Expr))
	}
	if by["UNKNOWN"].Kind != ir.ValueLit || by["UNKNOWN"].Lit != "$OTHER_VAR" {
		t.Errorf("UNKNOWN = %+v", by["UNKNOWN"])
	}
	if by["ESC"].Kind != ir.ValueLit || by["ESC"].Lit != "$$CI_COMMIT_SHA" {
		t.Errorf("ESC = %+v", by["ESC"])
	}
	if !hasEntry(rep, "OTHER_VAR") {
		t.Errorf("an unknown variable in a value needs a report note: %+v", rep.Entries)
	}
	if hasEntry(rep, "CI_COMMIT_SHA") {
		t.Errorf("known variables are not reported: %+v", rep.Entries)
	}
}

func TestConsumedKeysNotInExt(t *testing.T) {
	pl, _ := pipeline(t, `
stages: [a, b]
image: x
services: [pg]
cache: {paths: [p]}
before_script: [echo]
after_script: [echo]
default:
  retry: 1
j:
  stage: a
  script: [x]
`)
	x := ext(pl.Ext)
	for _, k := range []string{"default", "image", "services", "cache", "before_script", "after_script"} {
		if _, bad := x[k]; bad {
			t.Errorf("%s is expanded into the jobs and must not be in pipeline Ext: %v", k, x)
		}
	}
	if x["stages"] == nil {
		t.Errorf("stages has no IR field and stays in Ext for the emitter")
	}
}

func TestInheritVariablesKept(t *testing.T) {
	pl, _ := pipeline(t, "j:\n  script: [x]\n  inherit:\n    default: false\n    variables: false\n")
	in, _ := ext(job(t, pl, "j").Ext)["inherit"].(map[string]any)
	if in["variables"] != false || in["default"] != nil {
		t.Errorf("only the unapplied part of inherit stays: %v", in)
	}
}

// A list built from an anchor nests one list in another, which GitLab flattens.
func TestCacheNestedListIsFlattened(t *testing.T) {
	src := ".cached: &cached\n  - key: go\n    paths: [.mod/]\nbuild:\n  cache:\n    - *cached\n    - key: lint\n      paths: [.lint/]\n  script: [make]\n"
	pl, _ := pipeline(t, src)
	got := job(t, pl, "build").Cache
	want := []ir.Cache{{Key: "go", Paths: []string{".mod/"}}, {Key: "lint", Paths: []string{".lint/"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cache = %+v, want %+v", got, want)
	}
}

// A job that overrides a job from a remote template has no script, and GitLab merges the template's in.
func TestScriptlessJobStaysScriptless(t *testing.T) {
	src := "include:\n  - template: Security/Container-Scanning.gitlab-ci.yml\ncontainer_scanning:\n  stage: test\n  variables:\n    TAG: edge\n"
	proj, _ := parseFiles(t, map[string]string{".gitlab-ci.yml": src})
	files, _, err := gitlab.New().Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	out := string(files[".gitlab-ci.yml"])
	if strings.Contains(out, "script:") {
		t.Errorf("emitted a script for a job that has none:\n%s", out)
	}
	again, _ := parseFiles(t, map[string]string{".gitlab-ci.yml": out})
	if eq, diff := ir.Equal(proj, again); !eq {
		t.Errorf("second parse differs: %s", diff)
	}
}

// An empty before_script overrides a template's; it is no script, and Emit keeps it and invents none.
func TestEmptyScriptSectionOverrideRoundTrips(t *testing.T) {
	src := "include:\n  - template: Security/SAST.gitlab-ci.yml\nsast:\n  needs: []\n  before_script: []\n"
	proj, _ := parseFiles(t, map[string]string{".gitlab-ci.yml": src})
	files, _, err := gitlab.New().Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	out := string(files[".gitlab-ci.yml"])
	if !strings.Contains(out, "before_script: []") || strings.Contains(out, "script:\n") && strings.Contains(out, "true") {
		t.Errorf("before_script override not kept as is:\n%s", out)
	}
	again, _ := parseFiles(t, map[string]string{".gitlab-ci.yml": out})
	if eq, diff := ir.Equal(proj, again); !eq {
		t.Errorf("second parse differs: %s", diff)
	}
}

// A job that adds a before_script to a template job has no script key, and Emit must not add one.
func TestBeforeScriptWithoutScriptRoundTrips(t *testing.T) {
	src := "include:\n  - template: Security/SAST.gitlab-ci.yml\nsast:\n  before_script:\n    - bundle install\n"
	proj, _ := parseFiles(t, map[string]string{".gitlab-ci.yml": src})
	files, _, err := gitlab.New().Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	out := string(files[".gitlab-ci.yml"])
	if strings.Contains(out, "\n  script:") {
		t.Errorf("emitted a script key:\n%s", out)
	}
	again, _ := parseFiles(t, map[string]string{".gitlab-ci.yml": out})
	if eq, diff := ir.Equal(proj, again); !eq {
		t.Errorf("second parse differs: %s", diff)
	}
}

// cache: [] removes the cache an extended job sets.
func TestEmptyCacheListOverrideRoundTrips(t *testing.T) {
	src := ".base:\n  cache:\n    paths: [.mod/]\nbuild:\n  extends: .base\n  cache: []\n  script: [make]\n"
	proj, _ := parseFiles(t, map[string]string{".gitlab-ci.yml": src})
	files, _, err := gitlab.New().Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	out := string(files[".gitlab-ci.yml"])
	again, _ := parseFiles(t, map[string]string{".gitlab-ci.yml": out})
	if eq, diff := ir.Equal(proj, again); !eq {
		t.Errorf("second parse differs: %s\n%s", diff, out)
	}
}

// A null that unsets a key of an extended job must survive Emit, or the parent's value comes back. The CI schema
// rejects parallel: null, so this cannot be a fixture.
func TestNullOverrideOfInheritedKeyRoundTrips(t *testing.T) {
	src := ".unit:\n  parallel: 2\n  script: [make]\ncompile:\n  extends: .unit\n  parallel: null\n  script: [make compile]\n"
	proj, _ := parseFiles(t, map[string]string{".gitlab-ci.yml": src})
	files, _, err := gitlab.New().Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	out := string(files[".gitlab-ci.yml"])
	if !strings.Contains(out, "parallel: null") {
		t.Fatalf("null override dropped:\n%s", out)
	}
	again, _ := parseFiles(t, map[string]string{".gitlab-ci.yml": out})
	if eq, diff := ir.Equal(proj, again); !eq {
		t.Errorf("second parse differs: %s", diff)
	}
}

func needsText(t *testing.T, src string) string {
	t.Helper()
	proj, _ := parseFiles(t, map[string]string{".gitlab-ci.yml": src})
	files, _, err := gitlab.New().Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	return string(files[".gitlab-ci.yml"])
}

// An instance need with explicit keys keeps the instance name and the keys.
func TestMatrixInstanceNeedKeepsExplicitKeys(t *testing.T) {
	src := "stages: [a, b]\nimages:\n  stage: a\n  script: [x]\n  artifacts:\n    paths: [o]\n  parallel:\n    matrix:\n      - T: [x, y]\npkg:\n  stage: b\n  needs:\n    - job: \"images: [x]\"\n      artifacts: false\n    - job: images\n      optional: true\n  script: [y]\n"
	out := needsText(t, src)
	if !strings.Contains(out, `job: 'images: [x]'`) && !strings.Contains(out, `job: "images: [x]"`) {
		t.Errorf("instance name lost:\n%s", out)
	}
	if !strings.Contains(out, "artifacts: false") {
		t.Errorf("artifacts key lost:\n%s", out)
	}
}

// A need on a job the emitter renames keeps its instance selector under the new name.
func TestMatrixInstanceNeedFollowsRename(t *testing.T) {
	prod := glJob("on", glStep("x"))
	prod.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "T", Values: []string{"x", "y"}}}}
	cons := glJob("pkg", glStep("y"))
	cons.Needs = []ir.Need{{JobID: "on"}}
	cons.Ext = ir.Ext{"gitlab": map[string]any{"needs_extra": map[string]any{"on": map[string]any{"_instance": "on: [x]"}}}}
	files, _, err := gitlab.New().Emit(glProject("gitlab", prod, cons))
	if err != nil {
		t.Fatal(err)
	}
	out := string(files[".gitlab-ci.yml"])
	if strings.Contains(out, "- on\n") || !strings.Contains(out, ": [x]") || strings.Contains(out, "- 'on: [x]'") {
		t.Errorf("selector wrong after rename:\n%s", out)
	}
}

func instanceNeedEmit(t *testing.T, inst string) (string, *report.Report) {
	t.Helper()
	prod := glJob("on", glStep("x"))
	cons := glJob("pkg", glStep("y"))
	cons.Needs = []ir.Need{{JobID: "on"}}
	cons.Ext = ir.Ext{"gitlab": map[string]any{"needs_extra": map[string]any{"on": map[string]any{"_instance": inst}}}}
	files, rep, err := gitlab.New().Emit(glProject("gitlab", prod, cons))
	if err != nil {
		t.Fatal(err)
	}
	return string(files[".gitlab-ci.yml"]), rep
}

// An instance value without a bracket selector is written as the need name when GitLab can take it.
func TestInstanceNeedWithoutSelectorIsEmittedVerbatim(t *testing.T) {
	out, _ := instanceNeedEmit(t, "on-custom")
	if !strings.Contains(out, "- on-custom") {
		t.Errorf("instance dropped:\n%s", out)
	}
}

// A value GitLab cannot take as a need name is reported with its text.
func TestInstanceNeedInvalidIsReportedLost(t *testing.T) {
	out, rep := instanceNeedEmit(t, "bad\nname")
	if strings.Contains(out, "bad") {
		t.Errorf("invalid instance written:\n%s", out)
	}
	for _, e := range rep.Entries {
		if e.Level == caps.Lost && strings.Contains(e.Detail, "bad") {
			return
		}
	}
	t.Error("no lost entry naming the instance")
}
