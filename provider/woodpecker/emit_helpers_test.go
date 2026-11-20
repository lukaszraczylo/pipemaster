package woodpecker_test

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/woodpecker"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/validate"
)

const (
	dirPrefix = ".woodpecker/"
	ghaOrigin = "gha"
)

func wpStep(id string, lines ...string) *ir.Step {
	return &ir.Step{ID: id, Kind: ir.StepScript, Script: lines, Image: &ir.Image{Ref: "alpine:3"}}
}

func wpJob(id string, steps ...*ir.Step) *ir.Job { return &ir.Job{ID: id, Steps: steps} }

func wpProject(origin string, jobs ...*ir.Job) *ir.Project {
	pl := &ir.Pipeline{ID: "woodpecker", Jobs: jobs}
	return &ir.Project{Origin: ir.Origin{Provider: origin, Dialect: origin}, Pipelines: []*ir.Pipeline{pl}}
}

// emitFiles emits p, validates every file against the pinned schema and lints it with woodpecker-cli
// when the binary exists.
func emitFiles(t *testing.T, p *ir.Project) (map[string]string, *report.Report) {
	t.Helper()
	ir.Normalize(p)
	raw, rep, err := woodpecker.New().Emit(p)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	out := map[string]string{}
	for name, data := range raw {
		if err := validate.File(woodpecker.New().ID(), name, data); err != nil {
			t.Errorf("%s fails the schema: %v\n%s", name, err, data)
		}
		lint(t, name, data)
		out[name] = string(data)
	}
	return out, rep
}

func lint(t *testing.T, name string, data []byte) {
	t.Helper()
	if _, ok := linttool.Find("woodpecker-cli"); !ok {
		if os.Getenv(linttool.RequireEnv) == "1" {
			t.Fatalf("woodpecker-cli is not installed and %s=1; run scripts/install-linters.sh", linttool.RequireEnv)
		}
		return
	}
	if out, err := linttool.WoodpeckerLint(t, name, data); err != nil {
		t.Errorf("woodpecker-cli lint %s: %v\n%s\n--- file ---\n%s", name, err, out, data)
	}
}

// one emits a project that holds a single workflow and returns its text.
func one(t *testing.T, p *ir.Project) (string, *report.Report) {
	t.Helper()
	files, rep := emitFiles(t, p)
	if len(files) != 1 {
		t.Fatalf("want one file, got %v", keys(files))
	}
	for _, v := range files {
		return v, rep
	}
	return "", rep
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func reparse(t *testing.T, files map[string]string) *ir.Project {
	t.Helper()
	p, _ := parseRaw(t, files)
	return p
}

func reportHas(rep *report.Report, f caps.Feature, lvl caps.Level) bool {
	for _, e := range rep.Entries {
		if e.Feature == f && e.Level == lvl {
			return true
		}
	}
	return false
}

func reportDetail(rep *report.Report, f caps.Feature, substr string) bool {
	for _, e := range rep.Entries {
		if e.Feature == f && strings.Contains(e.Detail, substr) {
			return true
		}
	}
	return false
}

func ref(path ...string) expr.Expr { return expr.Ref{Path: path} }

func inc(vs ...string) ir.Filter { return ir.Filter{Include: vs} }
