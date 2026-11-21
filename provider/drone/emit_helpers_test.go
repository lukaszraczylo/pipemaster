package drone_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/drone"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/validate"

	// The GHA dialect gives the conditions of a gha project their equality semantics.
	_ "github.com/lukaszraczylo/pipemaster/expr/gha"
)

const (
	ghaOrigin = "gha"
	ymlName   = ".drone.yml"
)

func gstep(id string, lines ...string) *ir.Step {
	return &ir.Step{ID: id, Kind: ir.StepScript, Script: lines, Image: &ir.Image{Ref: "alpine:3"}}
}

func gjob(id string, steps ...*ir.Step) *ir.Job { return &ir.Job{ID: id, Steps: steps} }

func gproject(origin string, jobs ...*ir.Job) *ir.Project {
	pl := &ir.Pipeline{ID: "drone", Jobs: jobs}
	return &ir.Project{Origin: ir.Origin{Provider: origin, Dialect: origin}, Pipelines: []*ir.Pipeline{pl}}
}

// emitText emits p and checks the file against the pinned schema and drone lint.
func emitText(t *testing.T, p *ir.Project) (string, *report.Report) {
	t.Helper()
	ir.Normalize(p)
	raw, rep, err := drone.New().Emit(p)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(raw) != 1 || raw[ymlName] == nil {
		t.Fatalf("want one %s, got %d files", ymlName, len(raw))
	}
	data := raw[ymlName]
	if err := validate.File(drone.New().ID(), ymlName, data); err != nil {
		t.Errorf("the file fails the schema: %v\n%s", err, data)
	}
	if _, ok := linttool.Locate(t, "drone"); ok {
		if out, err := linttool.DroneLint(t, ymlName, data); err != nil {
			t.Errorf("drone lint: %v\n%s\n--- file ---\n%s", err, out, data)
		}
	}
	return string(data), rep
}

// docsOf decodes every document of the file.
func docsOf(t *testing.T, text string) []map[string]any {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(text))
	var out []map[string]any
	for {
		var m map[string]any
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("decode: %v\n%s", err, text)
		}
		out = append(out, m)
	}
}

func docNamed(t *testing.T, docs []map[string]any, name string) map[string]any {
	t.Helper()
	for _, d := range docs {
		if d["name"] == name {
			return d
		}
	}
	t.Fatalf("no document named %q", name)
	return nil
}

func stepsOf(d map[string]any) []map[string]any {
	var out []map[string]any
	l, _ := d["steps"].([]any)
	for _, s := range l {
		out = append(out, s.(map[string]any))
	}
	return out
}

func levelOf(rep *report.Report, f caps.Feature, pathHas string) (caps.Level, bool) {
	for _, e := range rep.Entries {
		if e.Feature == f && strings.Contains(e.IRPath, pathHas) {
			return e.Level, true
		}
	}
	return 0, false
}

func wantLevel(t *testing.T, rep *report.Report, f caps.Feature, pathHas string, want caps.Level) {
	t.Helper()
	got, ok := levelOf(rep, f, pathHas)
	if !ok || got != want {
		t.Fatalf("%s at %q: level %v (found %v), want %v\n%v", f, pathHas, got, ok, want, rep.Entries)
	}
}

func lines(text string) []string { return strings.Split(text, "\n") }

func hasLine(text, prefix string) bool {
	for _, l := range lines(text) {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func countLines(text, prefix string) int {
	n := 0
	for _, l := range lines(text) {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// reparse parses emitted text.
func reparse(t *testing.T, text string) *ir.Project {
	t.Helper()
	p, _, err := drone.New().Parse(repo(map[string]string{ymlName: text}))
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, text)
	}
	return p
}

// emitUnchecked emits p without the schema, for a verbatim Drone form that the SchemaStore schema does not read.
func emitUnchecked(t *testing.T, p *ir.Project) string {
	t.Helper()
	ir.Normalize(p)
	raw, _, err := drone.New().Emit(p)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	return string(raw[ymlName])
}
