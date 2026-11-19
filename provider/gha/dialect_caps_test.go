package gha_test

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
	_ "github.com/lukaszraczylo/pipemaster/provider/gitlab"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	envSrc   = "on: push\njobs:\n  deploy:\n    runs-on: r\n    environment: production\n    steps:\n      - run: echo\n"
	permSrc  = "on: push\npermissions:\n  contents: read\n  checks: write\n  id-token: write\njobs:\n  b:\n    runs-on: r\n    steps:\n      - run: echo\n"
	prSrc    = "on: pull_request\njobs:\n  b:\n    runs-on: r\n    steps:\n      - run: echo\n"
	eventSrc = "on:\n  push:\n  gollum:\n  check_run:\njobs:\n  b:\n    runs-on: r\n    steps:\n      - run: echo\n"
	pushJob  = "jobs:\n  b:\n    runs-on: r\n    steps:\n      - run: echo\n"
)

func parseWF(t *testing.T, d gha.Dialect, src string) *ir.Project {
	t.Helper()
	fsys := fstest.MapFS{d.Dirs[0] + "/ci.yml": &fstest.MapFile{Data: []byte(src)}}
	p, _, err := gha.New(d).Parse(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type wantEntry struct {
	path  string
	text  string
	f     caps.Feature
	level caps.Level
}

// checkEntries requires the report to hold exactly the wanted entries among the features in scope.
func checkEntries(t *testing.T, rep *report.Report, scope []caps.Feature, want []wantEntry) {
	t.Helper()
	used := map[int]bool{}
	for _, e := range rep.Entries {
		if !containsFeature(scope, e.Feature) {
			continue
		}
		found := false
		for i, w := range want {
			if !used[i] && w.path == e.IRPath && w.f == e.Feature && w.level == e.Level && strings.Contains(e.Detail+" "+e.Strategy, w.text) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			t.Errorf("unexpected entry %+v", e)
		}
	}
	for i, w := range want {
		if !used[i] {
			t.Errorf("missing entry %+v in %+v", w, rep.Entries)
		}
	}
}

func containsFeature(fs []caps.Feature, f caps.Feature) bool {
	for _, x := range fs {
		if x == f {
			return true
		}
	}
	return false
}

func TestDialectCapsTable(t *testing.T) {
	const (
		prCaveat      = "the ref is refs/pull/N/head, not the merge ref"
		forgejoPerms  = "Forgejo does not document permissions; the token has write access except on fork pull requests"
		giteaOIDCText = "id-token is unsupported in Gitea v28"
	)
	jobEnv, perms, trig, ext := []caps.Feature{caps.JobEnvironment}, []caps.Feature{caps.PipelinePermissions},
		[]caps.Feature{caps.TriggerPullRequest}, []caps.Feature{caps.ExtPassthrough}
	for _, c := range []struct {
		name   string
		src    string
		d      string
		scope  []caps.Feature
		out    []string
		absent []string
		want   []wantEntry
	}{
		{"job.environment/gha", envSrc, gha.GHA.ID, jobEnv, []string{"environment: production"}, nil, nil},
		{"job.environment/gitea", envSrc, gha.Gitea.ID, jobEnv, []string{"environment: production"}, nil,
			[]wantEntry{{"pipeline/ci/job/deploy", "Gitea ignores jobs.<id>.environment", caps.JobEnvironment, caps.Lost}}},
		{"job.environment/forgejo", envSrc, gha.Forgejo.ID, jobEnv, []string{"environment: production"}, nil,
			[]wantEntry{{"pipeline/ci/job/deploy", "Forgejo does not document environment", caps.JobEnvironment, caps.Lost}}},

		{"pipeline.permissions/gha", permSrc, gha.GHA.ID, perms, []string{"contents: read", "checks: write", "id-token: write"}, nil, nil},
		{"pipeline.permissions/gitea", permSrc, gha.Gitea.ID, perms, []string{"contents: read"}, []string{"checks", "id-token"},
			[]wantEntry{
				{"pipeline/ci/scope/checks", "checks", caps.PipelinePermissions, caps.Lost},
				{"pipeline/ci/scope/id-token", giteaOIDCText, caps.PipelinePermissions, caps.Lost},
			}},
		{"pipeline.permissions/forgejo", permSrc, gha.Forgejo.ID, perms,
			[]string{"contents: read", "checks: write", "enable-openid-connect: true"}, []string{"id-token"},
			[]wantEntry{{"pipeline/ci", forgejoPerms, caps.PipelinePermissions, caps.Lost}}},

		{"trigger.pull_request/gha", prSrc, gha.GHA.ID, trig, []string{"pull_request"}, nil, nil},
		{"trigger.pull_request/gitea", prSrc, gha.Gitea.ID, trig, []string{"pull_request"}, nil,
			[]wantEntry{{"pipeline/ci/trigger/0", prCaveat, caps.TriggerPullRequest, caps.Native}}},
		{"trigger.pull_request/forgejo", prSrc, gha.Forgejo.ID, trig, []string{"pull_request"}, nil, nil},

		{"events/gha", eventSrc, gha.GHA.ID, ext, []string{"gollum", "check_run"}, nil, nil},
		{"events/gitea", eventSrc, gha.Gitea.ID, ext, []string{"gollum"}, []string{"check_run"},
			[]wantEntry{{"pipeline/ci/event/check_run", "check_run", caps.ExtPassthrough, caps.Lost}}},
		{"events/forgejo", eventSrc, gha.Forgejo.ID, ext, nil, []string{"gollum", "check_run"},
			[]wantEntry{
				{"pipeline/ci/event/check_run", "check_run", caps.ExtPassthrough, caps.Lost},
				{"pipeline/ci/event/gollum", "gollum", caps.ExtPassthrough, caps.Lost},
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			text, rep := emitText(t, gha.Dialects[c.d], parseWF(t, gha.GHA, c.src))
			for _, s := range c.out {
				if !strings.Contains(text, s) {
					t.Errorf("output lacks %q:\n%s", s, text)
				}
			}
			for _, s := range c.absent {
				if strings.Contains(text, s) {
					t.Errorf("output holds %q:\n%s", s, text)
				}
			}
			checkEntries(t, rep, c.scope, c.want)
		})
	}
}

func TestForgejoConcurrencyDefault(t *testing.T) {
	const marker = "# pipemaster:emulated pipeline.concurrency pipeline/ci"
	src := parseWF(t, gha.GHA, "on: push\n"+pushJob)
	text, rep := emitText(t, gha.Forgejo, src)
	for _, s := range []string{"group: ${{ github.run_id }}", "cancel-in-progress: false", marker} {
		if !strings.Contains(text, s) {
			t.Errorf("forgejo output lacks %q:\n%s", s, text)
		}
	}
	checkEntries(t, rep, []caps.Feature{caps.PipelineConcurrency},
		[]wantEntry{{"pipeline/ci", "Forgejo cancels", caps.PipelineConcurrency, caps.Emulated}})

	mid, _, err := gha.New(gha.Forgejo).Parse(fstest.MapFS{".forgejo/workflows/ci.yml": &fstest.MapFile{Data: []byte(text)}})
	if err != nil {
		t.Fatal(err)
	}
	if pl := mid.Pipelines[0]; pl.Concurrency != nil || len(pl.Ext) != 0 {
		t.Fatalf("the marker must collapse to no concurrency, got %+v ext %v", pl.Concurrency, pl.Ext)
	}
	back, brep := emitText(t, gha.GHA, mid)
	if strings.Contains(back, "concurrency") || entry(brep, caps.PipelineConcurrency) != nil {
		t.Errorf("forgejo to gha must have no concurrency:\n%s %+v", back, brep.Entries)
	}

	for name, c := range map[string]struct {
		src string
		d   gha.Dialect
	}{
		"gha":               {"on: push\n" + pushJob, gha.GHA},
		"gitea":             {"on: push\n" + pushJob, gha.Gitea},
		"explicit":          {"on: push\nconcurrency: mine\n" + pushJob, gha.Forgejo},
		"schedule only":     {"on:\n  schedule:\n    - cron: '0 0 * * *'\n" + pushJob, gha.Forgejo},
		"workflow_dispatch": {"on: workflow_dispatch\n" + pushJob, gha.Forgejo},
	} {
		t.Run(name, func(t *testing.T) {
			out, r := emitText(t, c.d, parseWF(t, gha.GHA, c.src))
			if strings.Contains(out, "github.run_id") || strings.Contains(out, marker) {
				t.Errorf("no default entry expected:\n%s", out)
			}
			if e := entry(r, caps.PipelineConcurrency); e != nil && c.src != "on: push\nconcurrency: mine\n"+pushJob {
				t.Errorf("unexpected entry %+v", e)
			}
		})
	}
	explicit, _ := emitText(t, gha.Forgejo, parseWF(t, gha.GHA, "on: push\nconcurrency: mine\n"+pushJob))
	if !strings.Contains(explicit, "concurrency: mine") && !strings.Contains(explicit, "group: mine") {
		t.Errorf("explicit concurrency must stay:\n%s", explicit)
	}
}

func TestOIDCForgejo(t *testing.T) {
	src := parseWF(t, gha.GHA, permSrc)
	text, _ := emitText(t, gha.Forgejo, src)
	mid := parseWF(t, gha.Forgejo, text)
	want := &ir.Permissions{Scopes: map[string]string{"contents": "read", "checks": "write"}, OIDC: true}
	if got := mid.Pipelines[0].Permissions; !reflect.DeepEqual(got, want) {
		t.Fatalf("permissions after the forgejo trip = %+v, want %+v", got, want)
	}
	back, _ := emitText(t, gha.GHA, mid)
	if !strings.Contains(back, "id-token: write") || strings.Contains(back, "enable-openid-connect") {
		t.Errorf("gha must write id-token: write:\n%s", back)
	}

	only := parseWF(t, gha.Forgejo, "on: push\nenable-openid-connect: true\n"+pushJob)
	pl := only.Pipelines[0]
	if !reflect.DeepEqual(pl.Permissions, &ir.Permissions{OIDC: true}) || pl.Ext["forgejo"] != nil {
		t.Fatalf("workflow-level key must become OIDC, got %+v ext %v", pl.Permissions, pl.Ext)
	}
	out, _ := emitText(t, gha.Forgejo, only)
	if !strings.Contains(out, "enable-openid-connect: true") || strings.Contains(out, "permissions") {
		t.Errorf("forgejo must write the key and no permissions mapping:\n%s", out)
	}

	off := parseWF(t, gha.Forgejo, "on: push\nenable-openid-connect: false\n"+pushJob)
	if off.Pipelines[0].Permissions != nil {
		t.Errorf("enable-openid-connect: false grants nothing, got %+v", off.Pipelines[0].Permissions)
	}
	offOut, _ := emitText(t, gha.Forgejo, off)
	if !strings.Contains(offOut, "enable-openid-connect: false") {
		t.Errorf("the explicit false must survive:\n%s", offOut)
	}

	jobLevel := parseWF(t, gha.Forgejo, "on: push\njobs:\n  b:\n    runs-on: r\n    enable-openid-connect: true\n    steps:\n      - run: echo\n")
	if jobLevel.Pipelines[0].Permissions != nil || jobLevel.Pipelines[0].Jobs[0].Ext["forgejo"] == nil {
		t.Errorf("the job-level form stays in Ext[forgejo]: %+v", jobLevel.Pipelines[0])
	}
}

func TestScopesFilteredToGha(t *testing.T) {
	d := gha.Gitea
	src := parseWF(t, d, "on: push\npermissions:\n  code: read\n  contents: read\n"+pushJob)
	target := gha.New(gha.GHA)
	files, rep, err := target.Emit(src)
	if err != nil {
		t.Fatal(err)
	}
	text := string(files[".github/workflows/ci.yml"])
	if strings.Contains(text, "code") || !strings.Contains(text, "contents: read") {
		t.Fatalf("a Gitea-only scope must not reach GHA output:\n%s", text)
	}
	checkEntries(t, rep, []caps.Feature{caps.PipelinePermissions},
		[]wantEntry{{"pipeline/ci/scope/code", "code", caps.PipelinePermissions, caps.Lost}})

	m := roundtrip.NewMeta(d.ID, src, target, rep)
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "not kept in round-trip metadata") {
			t.Errorf("the scope must be recorded: %+v", e)
		}
	}
	mid, _, err := target.Parse(fstest.MapFS{".github/workflows/ci.yml": &fstest.MapFile{Data: []byte(text)}})
	if err != nil {
		t.Fatal(err)
	}
	roundtrip.Restore(mid, m)
	want := map[string]string{"code": "read", "contents": "read"}
	if got := mid.Pipelines[0].Permissions.Scopes; !reflect.DeepEqual(got, want) {
		t.Fatalf("restored scopes = %v, want %v", got, want)
	}
}

func TestConfigNamePrefix(t *testing.T) {
	wf := func(ref string) string {
		return "on: push\njobs:\n  b:\n    runs-on: r\n    steps:\n      - run: echo '${{ " + ref + " }}'\n"
	}
	for _, c := range []struct {
		name string
		ref  string
		want string
		d    gha.Dialect
	}{
		{"gitea secret GITEA_", "secrets.GITEA_X", "create secret GITEA_X: Gitea rejects names that start with GITEA_; rename it and update the reference", gha.Gitea},
		{"gitea secret GITHUB_", "secrets.GITHUB_X", "create secret GITHUB_X: Gitea rejects names that start with GITHUB_; rename it and update the reference", gha.Gitea},
		{"gitea var CI", "vars.CI_MODE", "create variable CI_MODE: Gitea rejects names that start with CI; rename it and update the reference", gha.Gitea},
		{"gitea var GITEA_", "vars.GITEA_Y", "create variable GITEA_Y: Gitea rejects names that start with GITEA_; rename it and update the reference", gha.Gitea},
		{"gitea builtin token", "secrets.GITHUB_TOKEN", "", gha.Gitea},
		{"gitea secret CI", "secrets.CI_KEY", "", gha.Gitea},
		{"gitea plain", "secrets.DEPLOY_KEY", "", gha.Gitea},
		{"forgejo var FORGEJO_", "vars.FORGEJO_A", "create variable FORGEJO_A: Forgejo rejects names that start with FORGEJO_; rename it and update the reference", gha.Forgejo},
		{"forgejo var GITHUB_", "vars.GITHUB_A", "create variable GITHUB_A: Forgejo rejects names that start with GITHUB_; rename it and update the reference", gha.Forgejo},
		{"forgejo var CI", "vars.CI", "create variable CI: Forgejo rejects the name CI; rename it and update the reference", gha.Forgejo},
		{"forgejo var CIX", "vars.CIX", "", gha.Forgejo},
		{"forgejo token", "secrets.GITHUB_TOKEN", "", gha.Forgejo},
		{"gha anything", "secrets.GITHUB_X", "", gha.GHA},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, rep := emitText(t, c.d, parseWF(t, gha.GHA, wf(c.ref)))
			path := "pipeline/ci/ref/" + c.ref
			if c.want == "" {
				checkEntries(t, rep, []caps.Feature{caps.ConfigName}, nil)
				return
			}
			checkEntries(t, rep, []caps.Feature{caps.ConfigName}, []wantEntry{{path, c.want, caps.ConfigName, caps.External}})
		})
	}
}

func TestSynthesisedActionsCarryTheGithubHost(t *testing.T) {
	gl, err := provider.Get(provider.GitLab)
	if err != nil {
		t.Fatal(err)
	}
	src := fstest.MapFS{".gitlab-ci.yml": &fstest.MapFile{Data: []byte("build:\n  cache:\n    key: k\n    paths: [.cache]\n  script: [make]\n")}}
	proj, _, err := gl.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		want string
		d    gha.Dialect
	}{
		{"uses: https://github.com/actions/cache@", gha.Forgejo},
		{"uses: actions/cache@", gha.Gitea},
		{"uses: actions/cache@", gha.GHA},
	} {
		files, _, err := gha.New(c.d).Emit(proj)
		if err != nil {
			t.Fatal(err)
		}
		var text string
		for _, f := range files {
			text += string(f)
		}
		if !strings.Contains(text, c.want) {
			t.Errorf("%s: want %q in\n%s", c.d.ID, c.want, text)
		}
		if c.d.ID == provider.Forgejo && strings.Contains(strings.ReplaceAll(text, "https://github.com/actions/", ""), " actions/") {
			t.Errorf("forgejo must not print a bare synthesised action:\n%s", text)
		}
	}
}

func TestNativeCaveatsAreReported(t *testing.T) {
	for _, c := range []struct {
		want     string
		from, to gha.Dialect
	}{
		{"DEFAULT_ACTIONS_URL; default github", gha.Gitea, gha.Gitea},
		{"printed as an absolute URL", gha.Gitea, gha.Forgejo},
		{"DEFAULT_ACTIONS_URL; default https://data.forgejo.org", gha.Forgejo, gha.Forgejo},
	} {
		t.Run(c.from.ID+"-to-"+c.to.ID, func(t *testing.T) {
			p, err := parseUses(t, c.from, "actions/checkout@v4")
			if err != nil {
				t.Fatal(err)
			}
			_, rep := emitText(t, c.to, p)
			var sb strings.Builder
			if err := rep.Render(&sb, "text"); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(sb.String(), "[native]") || !strings.Contains(sb.String(), c.want) {
				t.Errorf("rendered report lacks the native caveat %q:\n%s", c.want, sb.String())
			}
			for _, e := range rep.Entries {
				if e.Feature == caps.StepAction && e.Level != caps.Native {
					t.Errorf("a native caveat must not fail --strict: %+v", e)
				}
			}
		})
	}
}

// viaMetadata emits src through d, reads the file back, restores the metadata and returns the project.
func viaMetadata(t *testing.T, src *ir.Project, d gha.Dialect) (string, *report.Report, *ir.Project) {
	t.Helper()
	target := gha.New(d)
	files, rep, err := target.Emit(src)
	if err != nil {
		t.Fatal(err)
	}
	m := roundtrip.NewMeta(provider.GHA, src, target, rep)
	mid, _, err := target.Parse(toMapFS(files))
	if err != nil {
		t.Fatal(err)
	}
	roundtrip.Restore(mid, m)
	return string(files[d.Dirs[0]+"/ci.yml"]), rep, mid
}

func toMapFS(files map[string][]byte) fstest.MapFS {
	out := fstest.MapFS{}
	for n, b := range files {
		out[n] = &fstest.MapFile{Data: b}
	}
	return out
}

func TestModelledTriggersOutsideDialectEvents(t *testing.T) {
	const (
		runSrc  = "on:\n  workflow_run:\n    workflows: [ci]\n    types: [completed]\n" + pushJob
		dispSrc = "on: repository_dispatch\n" + pushJob
	)
	for _, c := range []struct {
		name    string
		src     string
		event   string
		dropped string
		feature caps.Feature
		d       gha.Dialect
		kept    bool
	}{
		{"workflow_run to forgejo", runSrc, "workflow_run", "Forgejo", caps.TriggerPipelineDone, gha.Forgejo, false},
		{"repository_dispatch to forgejo", dispSrc, "repository_dispatch", "Forgejo", caps.TriggerAPI, gha.Forgejo, false},
		{"repository_dispatch to gitea", dispSrc, "repository_dispatch", "Gitea", caps.TriggerAPI, gha.Gitea, false},
		{"workflow_run to gitea", runSrc, "workflow_run", "", caps.TriggerPipelineDone, gha.Gitea, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := parseWF(t, gha.GHA, c.src)
			text, rep, back := viaMetadata(t, src, c.d)
			if c.kept {
				if !strings.Contains(text, c.event+":") {
					t.Errorf("%s must stay:\n%s", c.event, text)
				}
				checkEntries(t, rep, []caps.Feature{c.feature}, nil)
				return
			}
			if strings.Contains(text, c.event+":") {
				t.Errorf("%s must be dropped:\n%s", c.event, text)
			}
			if !strings.Contains(text, "workflow_dispatch") || strings.Contains(text, "push") {
				t.Errorf("a pipeline with no runnable trigger must be manual only:\n%s", text)
			}
			checkEntries(t, rep, []caps.Feature{c.feature},
				[]wantEntry{{"pipeline/ci/trigger/0", c.dropped, c.feature, caps.Lost}})
			if !reflect.DeepEqual(back.Pipelines[0].Triggers, src.Pipelines[0].Triggers) {
				t.Errorf("triggers after the metadata return = %+v, want %+v", back.Pipelines[0].Triggers, src.Pipelines[0].Triggers)
			}
		})
	}
}

func TestConfigNameInExtAndImages(t *testing.T) {
	const wf = `on: push
jobs:
  b:
    runs-on: r
    container:
      image: ghcr.io/o/${{ secrets.GITHUB_IMG }}:1
      credentials:
        username: u
        password: ${{ secrets.GITHUB_PAT }}
    services:
      db:
        image: postgres:${{ vars.GITHUB_PG }}
    steps:
      - run: echo '${{ secrets.GITHUB_OTHER }}'
`
	_, rep := emitText(t, gha.Gitea, parseWF(t, gha.GHA, wf))
	const reason = ": Gitea rejects names that start with GITHUB_; rename it and update the reference"
	checkEntries(t, rep, []caps.Feature{caps.ConfigName}, []wantEntry{
		{"pipeline/ci/ref/secrets.GITHUB_IMG", "create secret GITHUB_IMG" + reason, caps.ConfigName, caps.External},
		{"pipeline/ci/ref/secrets.GITHUB_OTHER", "create secret GITHUB_OTHER" + reason, caps.ConfigName, caps.External},
		{"pipeline/ci/ref/secrets.GITHUB_PAT", "create secret GITHUB_PAT" + reason, caps.ConfigName, caps.External},
		{"pipeline/ci/ref/vars.GITHUB_PG", "create variable GITHUB_PG" + reason, caps.ConfigName, caps.External},
	})
}
