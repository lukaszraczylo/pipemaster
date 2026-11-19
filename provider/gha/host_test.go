package gha_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
	"github.com/lukaszraczylo/pipemaster/report"
)

func usesWorkflow(uses string) string {
	return "on: push\njobs:\n  b:\n    runs-on: r\n    steps:\n      - id: s\n        uses: " + uses + "\n"
}

func parseUses(t *testing.T, d gha.Dialect, uses string) (*ir.Project, error) {
	t.Helper()
	fsys := fstest.MapFS{d.Dirs[0] + "/ci.yml": &fstest.MapFile{Data: []byte(usesWorkflow(uses))}}
	p, _, err := gha.New(d).Parse(fsys)
	return p, err
}

func firstAction(p *ir.Project) *ir.ActionRef { return p.Pipelines[0].Jobs[0].Steps[0].Action }

func TestHostParse(t *testing.T) {
	const (
		gh = "https://github.com"
		fj = "https://data.forgejo.org"
	)
	for _, c := range []struct {
		name    string
		uses    string
		want    string
		wantErr string
		d       gha.Dialect
	}{
		{"gha bare", "actions/checkout@v4", gh, "", gha.GHA},
		{"gitea bare", "actions/checkout@v4", gh, "", gha.Gitea},
		{"forgejo bare", "actions/checkout@v4", fj, "", gha.Forgejo},
		{"gitea absolute", "https://git.example.com/o/r@v1", "https://git.example.com", "", gha.Gitea},
		{"forgejo absolute", "https://codeberg.org/o/r/sub@v1", "https://codeberg.org", "", gha.Forgejo},
		{"gha absolute", "https://git.example.com/o/r@v1", "", "ci.yml", gha.GHA},
		{"gitea self", "self:o/r@v1", ir.HostSelf, "", gha.Gitea},
		{"gitea builtin", "builtin:checkout", ir.HostBuiltin, "", gha.Gitea},
		{"gha self", "self:o/r@v1", "", "ci.yml", gha.GHA},
		{"forgejo self", "self:o/r@v1", "", "ci.yml", gha.Forgejo},
		{"docker", "docker://alpine:3", "", "", gha.Gitea},
		{"local", "./local/act", "", "", gha.Forgejo},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := parseUses(t, c.d, c.uses)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want error naming %s, got %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := firstAction(p).Host; got != c.want {
				t.Errorf("Host = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAbsoluteUsesInGhaErrors(t *testing.T) {
	_, err := parseUses(t, gha.GHA, "https://github.com/actions/checkout@v4")
	if err == nil || !strings.Contains(err.Error(), ".github/workflows/ci.yml") {
		t.Fatalf("want error naming the file, got %v", err)
	}
}

func emitText(t *testing.T, d gha.Dialect, p *ir.Project) (string, *report.Report) {
	t.Helper()
	files, rep, err := gha.New(d).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(files[d.Dirs[0]+"/ci.yml"]), rep
}

func entry(rep *report.Report, f caps.Feature) *report.Entry {
	for i := range rep.Entries {
		if rep.Entries[i].Feature == f {
			return &rep.Entries[i]
		}
	}
	return nil
}

func TestHostEmit(t *testing.T) {
	p, err := parseUses(t, gha.GHA, "actions/checkout@v4")
	if err != nil {
		t.Fatal(err)
	}
	forgejo, _ := emitText(t, gha.Forgejo, p)
	if !strings.Contains(forgejo, "uses: https://github.com/actions/checkout@v4") {
		t.Errorf("forgejo must print an absolute URL:\n%s", forgejo)
	}
	gitea, _ := emitText(t, gha.Gitea, p)
	if !strings.Contains(gitea, "uses: actions/checkout@v4") || strings.Contains(gitea, "https://") {
		t.Errorf("gitea must print the bare form:\n%s", gitea)
	}
	ghaOut, rep := emitText(t, gha.GHA, p)
	if !strings.Contains(ghaOut, "uses: actions/checkout@v4") || strings.Contains(ghaOut, "pipemaster:emulated step.action") {
		t.Errorf("gha with a github.com host needs no marker:\n%s", ghaOut)
	}
	if entry(rep, caps.StepAction) != nil {
		t.Errorf("github.com host on gha must be native: %+v", rep.Entries)
	}
}

func TestForgejoBareToGha(t *testing.T) {
	src, err := parseUses(t, gha.Forgejo, "actions/checkout@v4")
	if err != nil {
		t.Fatal(err)
	}
	text, rep := emitText(t, gha.GHA, src)
	if !strings.Contains(text, "uses: actions/checkout@v4") || !strings.Contains(text, "# pipemaster:emulated step.action pipeline/ci/job/b/step/s") {
		t.Fatalf("want bare ref and marker:\n%s", text)
	}
	e := entry(rep, caps.StepAction)
	if e == nil || e.Level != caps.Emulated || !strings.Contains(e.Detail, "resolved against https://data.forgejo.org in the source") {
		t.Fatalf("want emulated step.action with caveat, got %+v", rep.Entries)
	}
	mid, _, err := gha.New(gha.GHA).Parse(fstest.MapFS{".github/workflows/ci.yml": &fstest.MapFile{Data: []byte(text)}})
	if err != nil {
		t.Fatal(err)
	}
	if h := firstAction(mid).Host; h != "https://data.forgejo.org" {
		t.Fatalf("collapse must restore the host without metadata, got %q", h)
	}
	back, _ := emitText(t, gha.Forgejo, mid)
	if !strings.Contains(back, "uses: actions/checkout@v4") || strings.Contains(back, "https://") {
		t.Errorf("forgejo must print the bare form again:\n%s", back)
	}
}

func TestGiteaSelfToGha(t *testing.T) {
	src, err := parseUses(t, gha.Gitea, "self:o/r@v1")
	if err != nil {
		t.Fatal(err)
	}
	text, rep := emitText(t, gha.GHA, src)
	if !strings.Contains(text, "if: false") || strings.Contains(text, "uses:") {
		t.Errorf("want a disabled step:\n%s", text)
	}
	if e := entry(rep, caps.StepAction); e == nil || e.Level != caps.Lost {
		t.Errorf("want lost step.action, got %+v", rep.Entries)
	}
	gitea, grep := emitText(t, gha.Gitea, src)
	if !strings.Contains(gitea, "uses: self:o/r@v1") || entry(grep, caps.StepAction) != nil {
		t.Errorf("gitea keeps self: natively:\n%s %+v", gitea, grep.Entries)
	}
}
