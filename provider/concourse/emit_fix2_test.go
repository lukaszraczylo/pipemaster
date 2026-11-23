package concourse_test

import (
	"slices"

	"go.yaml.in/yaml/v3"

	"fmt"
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

func TestDroppedCarryNamesEachFieldItHeld(t *testing.T) {
	j := scriptJob("test", "echo one\necho three", "echo ${{ matrix.os }}")
	j.Runner = ir.Runner{Labels: []string{"self-hosted", "linux-big"}}
	j.Env = []ir.Var{{Name: "JOBVAR", Value: ir.Value{Lit: "1"}}}
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "os", Values: []string{"a", "b"}}}}
	j.Steps[0].Image = &ir.Image{Ref: "golang:1.22"}
	j.Steps[0].Env = []ir.Var{{Name: "MODE", Value: ir.Value{Lit: "fast"}}}
	files, _ := emit(t, project(j))
	src := files[".concourse/ci.yml"]
	edited := src
	if i := strings.Index(src, "\nresources:"); i >= 0 {
		edited = src[:i] + strings.Replace(src[i:], "echo one", "echo uno", 1)
	}
	if edited == src {
		t.Fatalf("no script line to edit:\n%s", src)
	}
	_, rep := parseFiles(t, map[string]string{"ci.yml": edited})
	var text string
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "marker ignored") {
			text += e.Detail + "\n"
		}
	}
	for _, want := range []string{"not restored: ", "runs-on labels [linux-big self-hosted]", "job env JOBVAR", "step image golang:1.22", "step env MODE"} {
		if !strings.Contains(text, want) {
			t.Fatalf("no %q in the ignored-marker entries:\n%s", want, text)
		}
	}
}

func TestShellLookingLikePowershellStaysShWithAnEntry(t *testing.T) {
	for name, script := range map[string]string{
		"heredoc": "cat <<EOF\nContent-Type: text/plain\nEOF",
		"perl":    "perl -e '\n$total = 0;\nprint $total;\n'",
		"echo":    "echo 'a\nGet-Foo bar\nb'",
	} {
		t.Run(name, func(t *testing.T) {
			files, rep := emit(t, project(scriptJob("j", script)))
			body := bodyOf(files[".concourse/ci.yml"])
			if !strings.Contains(body, "path: sh") || strings.Contains(body, "platform: windows") || strings.Contains(body, "powershell") {
				t.Fatalf("an sh script was switched:\n%s", body)
			}
			_ = rep
		})
	}
	files, rep := emit(t, project(scriptJob("j", "choco install -y make")))
	if !strings.Contains(bodyOf(files[".concourse/ci.yml"]), "path: sh") || !hasFeatureEntry(rep, string(caps.StepShell), "may be PowerShell") {
		t.Fatalf("no lost entry for a script that reads as PowerShell: %v", rep.Entries)
	}
}

func TestWindowsRunnerSwitchesToPowershell(t *testing.T) {
	j := scriptJob("win", "Write-Host hi")
	j.Runner = ir.Runner{Labels: []string{"windows-latest"}}
	files, rep := emit(t, project(j))
	body := bodyOf(files[".concourse/ci.yml"])
	if !strings.Contains(body, "path: powershell") || !strings.Contains(body, "platform: windows") || !hasFeatureEntry(rep, string(caps.StepShell), "powershell -Command") {
		t.Fatalf("windows runner not honoured: %v\n%s", rep.Entries, body)
	}
}

func TestMixedJobSetsPlatformPerTask(t *testing.T) {
	j := scriptJob("mix", "echo linux")
	j.Steps = append(j.Steps, &ir.Step{ID: "mix-ps", Kind: ir.StepScript, Script: []string{"Write-Host hi"}, Shell: "pwsh"})
	files, rep := emit(t, project(j))
	body := bodyOf(files[".concourse/ci.yml"])
	if strings.Count(body, "platform: windows") != 1 || strings.Count(body, "platform: linux") != 1 {
		t.Fatalf("platform is not per task:\n%s", body)
	}
	if !hasFeatureEntry(rep, string(caps.StepShell), "other tasks of the job") {
		t.Fatalf("no entry for the mixed job: %v", rep.Entries)
	}
}

func TestNamedPowershellShellIsHonoured(t *testing.T) {
	j := scriptJob("win", "Write-Host hi")
	j.Steps[0].Shell = "pwsh"
	files, _ := emit(t, project(j))
	if !strings.Contains(bodyOf(files[".concourse/ci.yml"]), "path: pwsh") {
		t.Fatalf("shell pwsh not honoured:\n%s", files[".concourse/ci.yml"])
	}
	back, _ := parseFiles(t, files)
	if back.Pipelines[0].Jobs[0].Steps[0].Shell != "pwsh" {
		t.Fatalf("shell lost: %q", back.Pipelines[0].Jobs[0].Steps[0].Shell)
	}
}

func TestFinallyStepsWithTheSameIDStayParseable(t *testing.T) {
	named := func(id, script string) *ir.Job {
		j := scriptJob(id, script)
		j.Steps[0].ID = "script"
		return j
	}
	p := project(named("build", "make"))
	p.Pipelines[0].Finally = []*ir.Job{named("cleanup", "rm -rf x"), named("report", "echo done")}
	files, _ := emit(t, p)
	got, _ := reparse(t, files)
	seen := map[string]bool{}
	for _, j := range got.Pipelines[0].Jobs {
		clear(seen)
		for _, s := range j.Steps {
			if seen[s.ID] {
				t.Fatalf("job %s holds the step id %s twice", j.ID, s.ID)
			}
			seen[s.ID] = true
		}
	}
}

func TestAlwaysOnAPlanStepIsReportedLost(t *testing.T) {
	j := &ir.Job{ID: "build", Steps: []*ir.Step{
		{ID: "first", Kind: ir.StepScript, Script: []string{"make"}},
		{ID: "mid", Kind: ir.StepScript, Script: []string{"echo mid"}, If: callOf("always")},
		{ID: "last", Kind: ir.StepScript, Script: []string{"echo last"}},
	}}
	_, rep := emit(t, project(j))
	for _, e := range rep.Entries {
		if e.Feature == caps.StepIf && e.Level == caps.Lost && e.IRPath == "pipeline/ci/job/build/step/mid" {
			return
		}
	}
	t.Fatalf("no lost step.if entry for the always() step: %v", rep.Entries)
}

func TestForeignPluginStepIsLostAsAnAction(t *testing.T) {
	j := &ir.Job{ID: "build", Steps: []*ir.Step{
		{ID: "build", Kind: ir.StepScript, Script: []string{"make"}},
		{ID: "publish", Kind: ir.StepPlugin},
	}}
	_, rep := emit(t, project(j))
	for _, e := range rep.Entries {
		if e.Feature == caps.StepAction && e.Level == caps.Lost && e.IRPath == "pipeline/ci/job/build/step/publish" {
			return
		}
	}
	t.Fatalf("no lost step.action entry for the plugin step: %v", rep.Entries)
}

func finallyProject() *ir.Project {
	p := project(scriptJob("build", "make"))
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main")}
	p.Pipelines[0].Finally = []*ir.Job{scriptJob("cleanup", "rm -rf x"), scriptJob("report", "echo done")}
	return p
}

func TestFinallyJobsComeBackAsFinallyJobs(t *testing.T) {
	files, _ := emit(t, finallyProject())
	got, rep := reparse(t, files)
	pl := got.Pipelines[0]
	if len(pl.Finally) != 2 || len(pl.Jobs[0].Steps) != 1 {
		t.Fatalf("finally = %d, build steps = %d; want 2 and 1", len(pl.Finally), len(pl.Jobs[0].Steps))
	}
	if hasFeatureEntry(rep, string(caps.PipelineFinally), "marker ignored") {
		t.Fatalf("an unedited file reported an ignored marker: %v", rep.Entries)
	}
}

func TestEditedFinallyHookIsKept(t *testing.T) {
	files, _ := emit(t, finallyProject())
	src := files[".concourse/ci.yml"]
	i := strings.LastIndex(src, "- rm -rf x")
	src = src[:i] + "- rm -rf y" + src[i+len("- rm -rf x"):]
	got, rep := parseFiles(t, map[string]string{"ci.yml": src})
	pl := got.Pipelines[0]
	if len(pl.Finally) != 0 || len(pl.Jobs[0].Steps) != 3 {
		t.Fatalf("finally = %d, build steps = %d; want 0 and 3", len(pl.Finally), len(pl.Jobs[0].Steps))
	}
	if !hasFeatureEntry(rep, string(caps.PipelineFinally), "marker ignored") {
		t.Fatalf("no marker ignored entry: %v", rep.Entries)
	}
}

func TestCarryDropsTheExtOfFinallyJobs(t *testing.T) {
	p := finallyProject()
	for _, f := range p.Pipelines[0].Finally {
		f.Ext = ir.Ext{"gitlab": map[string]any{"stage": ".post"}}
		f.Steps[0].Ext = ir.Ext{"gitlab": map[string]any{"section": "script"}}
	}
	files, _ := emit(t, p)
	got, _ := reparse(t, files)
	for _, f := range got.Pipelines[0].Finally {
		if f.Ext != nil || f.Steps[0].Ext != nil {
			t.Fatalf("finally job %s kept the Ext of another provider: %v %v", f.ID, f.Ext, f.Steps[0].Ext)
		}
	}
}

const resourceOrderFile = `resources:
  - name: tick
    type: time
    source: {interval: 1h}
  - name: src
    type: git
    source:
      uri: https://example.com/x.git
      branch: main
jobs:
  - name: build
    plan:
      - get: tick
        trigger: true
      - get: src
        trigger: true
      - task: t
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: alpine}
          run:
            path: sh
            args: [-ec, make]
`

func TestOwnResourcesKeepTheirOrder(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"ci.yml": resourceOrderFile})
	files, _ := emit(t, p)
	back, _ := reparse(t, files)
	var names []string
	for _, r := range back.Pipelines[0].Ext["concourse"].(map[string]any)["resources"].([]any) {
		names = append(names, r.(map[string]any)["name"].(string))
	}
	if len(names) != 2 || names[0] != "tick" || names[1] != "src" {
		t.Fatalf("resource order = %v; want [tick src]", names)
	}
}

func TestTaskNameIsNoDisplayName(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"ci.yml": resourceOrderFile})
	for _, st := range p.Pipelines[0].Jobs[0].Steps {
		if st.Name != "" {
			t.Fatalf("step %s has the name %q; a name equal to the ID is no display name", st.ID, st.Name)
		}
	}
}

func TestForeignPluginStepKeepsItsPlaceAndComesBack(t *testing.T) {
	plugin := &ir.Step{ID: "publish", Kind: ir.StepPlugin, Image: &ir.Image{Ref: "plugins/docker"}, With: map[string]ir.Value{"repo": {Lit: "example/app"}}}
	j := &ir.Job{ID: "build", Steps: []*ir.Step{{ID: "build", Kind: ir.StepScript, Script: []string{"make"}}, plugin}}
	files, _ := emit(t, project(j))
	got, rep := reparse(t, files)
	steps := got.Pipelines[0].Jobs[0].Steps
	if len(steps) != 2 || steps[1].Kind != ir.StepPlugin || steps[1].Image == nil || steps[1].Image.Ref != "plugins/docker" {
		t.Fatalf("steps = %+v; want the plugin step back in place", steps)
	}
	if hasFeatureEntry(rep, string(caps.StepAction), "marker ignored") {
		t.Fatalf("an unedited file reported an ignored marker: %v", rep.Entries)
	}
}

func TestResourceUsedOnlyByAVerbatimStepKeepsItsOrder(t *testing.T) {
	src := `resources:
  - name: tick
    type: time
    source: {interval: 1h}
  - name: other
    type: git
    source: {uri: "https://example.com/o.git", branch: main}
  - name: src
    type: git
    source: {uri: "https://example.com/x.git", branch: main}
jobs:
  - name: build
    plan:
      - get: src
        trigger: true
      - get: other
      - task: t
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: alpine}}
          run: {path: sh, args: [-ec, make]}
`
	p, _ := parseFiles(t, map[string]string{"ci.yml": src})
	files, _ := emit(t, p)
	var order []string
	for _, r := range list(pipelineDoc(t, files)["resources"]) {
		order = append(order, dict(r)["name"].(string))
	}
	if strings.Join(order, ",") != "other,src" {
		t.Fatalf("resources = %v; want the file order other,src", order)
	}
}

func TestManualJobWithItsEventConditionReportsNoLostCondition(t *testing.T) {
	j := scriptJob("deploy", "make deploy")
	j.Manual = true
	j.If = eqRef("event", "manual")
	_, rep := emit(t, project(j))
	for _, e := range rep.Entries {
		if e.Feature == caps.JobIf && e.Level == caps.Lost {
			t.Fatalf("the manual condition of a manual job is reported lost: %+v", e)
		}
	}
}

const noLeadingGetFile = `resources:
  - name: src
    type: git
    source:
      uri: https://example.com/x.git
      branch: main
jobs:
  - name: build
    plan:
      - get: src
        trigger: true
      - task: t
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: alpine}}
          run: {path: sh, args: [-ec, make]}
  - name: ship
    plan:
      - task: t
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: alpine}}
          run: {path: sh, args: [-ec, ship]}
`

func TestJobWithNoLeadingSourceGetGetsNoneBack(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"ci.yml": noLeadingGetFile})
	files, _ := emit(t, p)
	back, _ := reparse(t, files)
	if len(back.Pipelines[0].Jobs) != 2 {
		t.Fatalf("jobs = %d", len(back.Pipelines[0].Jobs))
	}
	for i, j := range back.Pipelines[0].Jobs {
		if got, want := fmt.Sprint(j.Ext), fmt.Sprint(p.Pipelines[0].Jobs[i].Ext); got != want {
			t.Fatalf("job %s Ext = %s; want %s", j.ID, got, want)
		}
	}
}

func TestSingleGetInParallelKeepsItsWrapper(t *testing.T) {
	src := strings.Replace(noLeadingGetFile, "      - task: t\n        config:\n          platform: linux\n          image_resource: {type: registry-image, source: {repository: alpine}}\n          run: {path: sh, args: [-ec, ship]}\n",
		"      - in_parallel:\n          - get: src\n            passed: [build]\n      - task: t\n        config:\n          platform: linux\n          image_resource: {type: registry-image, source: {repository: alpine}}\n          run: {path: sh, args: [-ec, ship]}\n", 1)
	p, _ := parseFiles(t, map[string]string{"ci.yml": src})
	files, _ := emit(t, p)
	back, _ := reparse(t, files)
	for i, j := range back.Pipelines[0].Jobs {
		if got, want := fmt.Sprint(j.Ext), fmt.Sprint(p.Pipelines[0].Jobs[i].Ext); got != want {
			t.Fatalf("job %s Ext = %s; want %s", j.ID, got, want)
		}
	}
}
