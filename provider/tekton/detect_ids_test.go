package tekton_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
)

func TestDetect(t *testing.T) {
	for _, c := range []struct {
		files map[string]string
		name  string
		want  bool
	}{
		{name: "pipeline", files: map[string]string{".tekton/p.yaml": triggerFiles}, want: true},
		{name: "pipelinerun", files: map[string]string{".tekton/r.yml": "apiVersion: tekton.dev/v1beta1\nkind: PipelineRun\nmetadata: {name: r}\nspec: {pipelineSpec: {tasks: []}}\n"}, want: true},
		{name: "configmap only", files: map[string]string{".tekton/c.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: c}\n"}, want: false},
		{name: "task only", files: map[string]string{".tekton/t.yaml": "apiVersion: tekton.dev/v1\nkind: Task\nmetadata: {name: t}\nspec: {steps: []}\n"}, want: false},
		{name: "other apiVersion", files: map[string]string{".tekton/p.yaml": "apiVersion: example.dev/v1\nkind: Pipeline\nmetadata: {name: p}\n"}, want: false},
		{name: "outside .tekton", files: map[string]string{"ci/p.yaml": triggerFiles}, want: false},
		{name: "nested", files: map[string]string{".tekton/sub/p.yaml": triggerFiles}, want: true},
		{name: "broken yaml", files: map[string]string{".tekton/p.yaml": "kind: [\n"}, want: false},
		{name: "empty", files: map[string]string{}, want: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := tekton.New().Detect(repo(c.files)); got != c.want {
				t.Fatalf("Detect = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSourceFiles(t *testing.T) {
	l, ok := tekton.New().(provider.Layout)
	if !ok {
		t.Fatal("the provider must implement Layout")
	}
	got, err := l.SourceFiles(repo(map[string]string{
		".tekton/b.yaml": "a: 1", ".tekton/sub/a.yml": "a: 1", ".tekton/readme.md": "x", "other.yaml": "a: 1",
	}))
	if err != nil || !reflect.DeepEqual(got, []string{".tekton/b.yaml", ".tekton/sub/a.yml"}) {
		t.Fatalf("SourceFiles = %v, %v", got, err)
	}
}

func TestSanitizeID(t *testing.T) {
	r, ok := tekton.New().(provider.IDRules)
	if !ok {
		t.Fatal("the provider must implement IDRules")
	}
	for in, want := range map[string]string{
		"build":          "build",
		"Build_Test.one": "build-test-one",
		"-x-":            "x",
		"":               "default",
	} {
		if got := r.SanitizeID(in); got != want {
			t.Errorf("SanitizeID(%q) = %q, want %q", in, got, want)
		}
	}
	a, b := strings.Repeat("a", 80)+"-one", strings.Repeat("a", 80)+"-two"
	ga, gb := r.SanitizeID(a), r.SanitizeID(b)
	if len(ga) > 63 || len(gb) > 63 || ga == gb || r.SanitizeID(a) != ga {
		t.Fatalf("long names must truncate to 63, stay distinct and stable: %q %q", ga, gb)
	}
}

// A Task named <pipeline>-<job> that exceeds 63 characters is truncated by the emitter. The parser reads the
// job ID from the PipelineTask name or from the pipemaster:id marker beside it, so two jobs whose Task names
// share a 63-character prefix keep their own IDs.
func TestLongNamesRestoreFromMarker(t *testing.T) {
	long1 := strings.Repeat("j", 70) + "-first"
	long2 := strings.Repeat("j", 70) + "-second"
	r := tekton.New().(provider.IDRules)
	n1, n2 := r.SanitizeID(long1), r.SanitizeID(long2)
	pipe := "ci"
	taskName := func(n string) string { return r.SanitizeID(pipe + "-" + n) }
	yml := "apiVersion: tekton.dev/v1\nkind: Pipeline\nmetadata:\n  # pipemaster:id " + strings.Repeat("p", 80) + "\n  name: " + pipe + "\nspec:\n  tasks:\n"
	for _, c := range []struct{ id, name string }{{long1, n1}, {long2, n2}} {
		yml += "  # pipemaster:id " + c.id + "\n  - name: " + c.name + "\n    taskRef: {name: " + taskName(c.name) + "}\n"
	}
	for _, c := range []string{n1, n2} {
		yml += "---\napiVersion: tekton.dev/v1\nkind: Task\nmetadata: {name: " + taskName(c) + "}\nspec:\n  steps: [{name: s, image: x, script: \"true\"}]\n"
	}
	if taskName(n1) == taskName(n2) || len(taskName(n1)) > 63 {
		t.Fatalf("task names must be unique and short: %q %q", taskName(n1), taskName(n2))
	}
	pl, _ := parseYAML(t, yml)
	if pl.ID != strings.Repeat("p", 80) {
		t.Fatalf("pipeline id = %q", pl.ID)
	}
	if len(pl.Jobs) != 2 || pl.Jobs[0].ID != long1 || pl.Jobs[1].ID != long2 {
		var got []string
		for _, j := range pl.Jobs {
			got = append(got, j.ID)
		}
		t.Fatalf("jobs = %v", got)
	}
}
