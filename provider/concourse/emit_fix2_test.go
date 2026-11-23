package concourse_test

import (
	"slices"

	"go.yaml.in/yaml/v3"

	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestUnspellableConditionOnAnEnvironmentJobReportsNoFalseEdit(t *testing.T) {
	j := scriptJob("deploy", "make deploy")
	j.If = eqRef("event", "push")
	j.Environment = &ir.Environment{Name: "prod"}
	p := project(j)
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main")}
	files, _ := emit(t, p)
	_, rep := reparse(t, files)
	if hasFeatureEntry(rep, string(caps.JobManual), "marker ignored") {
		t.Fatalf("an unedited file reported an edit: %v", rep.Entries)
	}
}

// editedFiles emits p, replaces old with replacement and returns the edited file.
func editedFile(t *testing.T, p *ir.Project, old, replacement string) map[string]string {
	t.Helper()
	files, _ := emit(t, p)
	src := files[".concourse/ci.yml"]
	if !strings.Contains(src, old) {
		t.Fatalf("%q is not in the file:\n%s", old, src)
	}
	return map[string]string{"ci.yml": strings.Replace(src, old, replacement, 1)}
}

// mutate decodes the file, applies fn to the root node and encodes it again; the marker lines stay as they were.
func mutate(t *testing.T, src string, fn func(root *yaml.Node)) string {
	t.Helper()
	var head []string
	for _, l := range strings.Split(src, "\n") {
		if strings.HasPrefix(l, "# pipemaster:") {
			head = append(head, l)
		}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	fn(doc.Content[0])
	doc.Content[0].HeadComment = ""
	b, err := yaml.Marshal(doc.Content[0])
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(head, "\n") + "\n" + string(b)
}

// unwrap replaces every mapping that has the key and no sibling but extra by the value of the key.
func unwrap(n *yaml.Node, key string, extra ...string) {
	for i, c := range n.Content {
		unwrap(c, key, extra...)
		if c.Kind != yaml.MappingNode || len(c.Content) < 2 || c.Content[0].Value != key {
			continue
		}
		ok := true
		for j := 2; j < len(c.Content); j += 2 {
			ok = ok && slices.Contains(extra, c.Content[j].Value)
		}
		if !ok {
			continue
		}
		if key == "do" {
			n.Content = slices.Concat(n.Content[:i], c.Content[1].Content, n.Content[i+1:])
			return
		}
		n.Content[i] = c.Content[1]
	}
}

func wrapperProject() *ir.Project {
	j := scriptJob("build", "make")
	j.Steps[0].ContinueOnError = true
	return project(j)
}

func TestRemovingTheTryWrapperWins(t *testing.T) {
	files, _ := emit(t, wrapperProject())
	src := mutate(t, files[".concourse/ci.yml"], func(r *yaml.Node) { unwrap(r, "try") })
	if strings.Contains(bodyOf(src), "try:") {
		t.Fatalf("try stays:\n%s", src)
	}
	back, rep := parseFiles(t, map[string]string{"ci.yml": src})
	if back.Pipelines[0].Jobs[0].Steps[0].ContinueOnError {
		t.Fatal("the carry restored continue_on_error after the wrapper was removed")
	}
	if !hasFeatureEntry(rep, string(caps.StepContinueOnError), "continue_on_error") {
		t.Fatalf("no entry for the dropped field: %v", rep.Entries)
	}
}

func TestAddingTheTryWrapperWins(t *testing.T) {
	j := scriptJob("build", "make")
	files, _ := emit(t, project(j))
	src := mutate(t, files[".concourse/ci.yml"], func(r *yaml.Node) {
		var plan *yaml.Node
		var find func(n *yaml.Node)
		find = func(n *yaml.Node) {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == "plan" {
					plan = n.Content[i+1]
				}
			}
			for _, c := range n.Content {
				find(c)
			}
		}
		find(r)
		last := len(plan.Content) - 1
		plan.Content[last] = &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "try"}, plan.Content[last]}}
	})
	back, _ := parseFiles(t, map[string]string{"ci.yml": src})
	if !back.Pipelines[0].Jobs[0].Steps[0].ContinueOnError {
		t.Fatal("an added try wrapper was not read")
	}
}

func jobWrapperProject() *ir.Project {
	j := scriptJob("build", "make")
	d := ir.Duration(3 * 60e9)
	j.Timeout, j.Retry, j.AllowFailure = &d, &ir.Retry{Attempts: 2, On: []string{"x"}}, true
	return project(j)
}

func TestRemovingTheJobWrappersWins(t *testing.T) {
	files, _ := emit(t, jobWrapperProject())
	src := mutate(t, files[".concourse/ci.yml"], func(r *yaml.Node) {
		unwrap(r, "try")
		unwrap(r, "do", "attempts", "timeout")
	})
	if body := bodyOf(src); strings.Contains(body, "attempts") || strings.Contains(body, "try:") {
		t.Fatalf("wrappers stay:\n%s", src)
	}
	back, rep := parseFiles(t, map[string]string{"ci.yml": src})
	j := back.Pipelines[0].Jobs[0]
	if j.Timeout != nil || j.Retry != nil || j.AllowFailure {
		t.Fatalf("the carry restored removed wrappers: %+v", j)
	}
	if !hasFeatureEntry(rep, string(caps.ExtPassthrough), "marker ignored") {
		t.Fatalf("no entry: %v", rep.Entries)
	}
}

func TestRemovingTheAcrossWins(t *testing.T) {
	j := scriptJob("build", "echo ${{ matrix.os }}")
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "os", Values: []string{"a", "b"}}}}
	files, _ := emit(t, project(j))
	src := mutate(t, files[".concourse/ci.yml"], func(r *yaml.Node) {
		var strip func(n *yaml.Node)
		strip = func(n *yaml.Node) {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == "across" {
					n.Content = slices.Delete(n.Content, i, i+2)
					i -= 2
				}
			}
			for _, c := range n.Content {
				strip(c)
			}
		}
		strip(r)
	})
	if strings.Contains(bodyOf(src), "across") {
		t.Fatalf("across stays:\n%s", src)
	}
	back, _ := parseFiles(t, map[string]string{"ci.yml": src})
	if back.Pipelines[0].Jobs[0].Matrix != nil {
		t.Fatal("the carry restored the matrix after across was removed")
	}
}

func TestMovingAHookTaskIntoThePlanWins(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps = append(j.Steps, &ir.Step{ID: "cleanup", Kind: ir.StepScript, Script: []string{"rm -rf x"}, If: callOf("failure")})
	files, _ := emit(t, project(j))
	src := mutate(t, files[".concourse/ci.yml"], func(r *yaml.Node) {
		var move func(n *yaml.Node)
		move = func(n *yaml.Node) {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == "on_failure" {
					hook := n.Content[i+1]
					n.Content = slices.Delete(n.Content, i, i+2)
					for k := 0; k+1 < len(n.Content); k += 2 {
						if n.Content[k].Value == "plan" {
							n.Content[k+1].Content = append(n.Content[k+1].Content, hook)
						}
					}
					return
				}
			}
			for _, c := range n.Content {
				move(c)
			}
		}
		move(r)
	})
	if strings.Contains(bodyOf(src), "on_failure") {
		t.Fatalf("hook stays:\n%s", src)
	}
	back, _ := parseFiles(t, map[string]string{"ci.yml": src})
	for _, st := range back.Pipelines[0].Jobs[0].Steps {
		if st.ID == "cleanup" && st.If != nil {
			t.Fatalf("the carry restored the hook condition on a plan step: %v", st.If)
		}
	}
}

// bodyOf is the file without its marker comments.
func bodyOf(src string) string {
	var out []string
	for _, l := range strings.Split(src, "\n") {
		if !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
