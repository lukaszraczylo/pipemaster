package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func actionStep(id, ref string, with map[string]string) *ir.Step {
	st := &ir.Step{ID: id, Kind: ir.StepAction, Action: &ir.ActionRef{Kind: ir.ActionRepo, Owner: "actions", Repo: ref[:strings.Index(ref, "@")], Ref: ref[strings.Index(ref, "@")+1:]}}
	st.With = map[string]ir.Value{}
	for k, v := range with {
		st.With[k] = ir.Value{Lit: v}
	}
	return st
}

func jobWith(steps ...*ir.Step) *ir.Job { return &ir.Job{ID: "a", Steps: steps} }

func TestCheckoutBecomesGitCloneStepAction(t *testing.T) {
	co := actionStep("co", "checkout@v4", map[string]string{"fetch-depth": "1"})
	run := &ir.Step{ID: "run", Kind: ir.StepScript, Script: []string{"ls"}, WorkDir: "src"}
	files, rep := emit(t, project(jobWith(co, run)))
	task := taskDoc(t, files, "ci-a")
	steps := list(get(task, "spec", "steps"))
	clone := steps[0].(doc)
	if get(clone, "ref", "resolver") != "git" {
		t.Fatalf("clone step = %v", clone)
	}
	rp := get(clone, "ref", "params")
	if named(t, rp, "url")["value"] != "https://github.com/tektoncd-catalog/git-clone.git" ||
		named(t, rp, "revision")["value"] != "v1.7.1" ||
		named(t, rp, "pathInRepo")["value"] != "stepaction/git-clone/git-clone.yaml" {
		t.Fatalf("resolver params = %v", rp)
	}
	params := clone["params"]
	if named(t, params, "url")["value"] != "https://github.com/$(params.ctx-repo).git" || named(t, params, "revision")["value"] != "$(params.ctx-sha)" ||
		named(t, params, "depth")["value"] != "1" {
		t.Fatalf("clone params = %v", params)
	}
	if get(task, "spec", "stepTemplate", "workingDir") != "/workspace" || steps[1].(doc)["workingDir"] != "/workspace/src" {
		t.Fatalf("working dirs: %v / %v", get(task, "spec", "stepTemplate"), steps[1])
	}
	pl := pipelineDoc(t, files, "ci")
	if !hasNamed(get(pl, "spec", "params"), "ctx-repo") || !hasNamed(get(pl, "spec", "params"), "ctx-sha") {
		t.Fatalf("pipeline params = %v", get(pl, "spec", "params"))
	}
	wantEntry(t, rep, caps.StepAction, caps.Emulated, "")
	if !strings.Contains(files[".tekton/tasks/ci-a.yaml"], "# pipemaster:arg ") {
		t.Fatalf("held-step marker missing:\n%s", files[".tekton/tasks/ci-a.yaml"])
	}
}

func TestSetupActionSelectsTheImage(t *testing.T) {
	setup := actionStep("go", "setup-go@v5", map[string]string{"go-version": "1.27"})
	run := &ir.Step{ID: "run", Kind: ir.StepScript, Script: []string{"go version"}}
	files, rep := emit(t, project(jobWith(setup, run)))
	steps := list(get(taskDoc(t, files, "ci-a"), "spec", "steps"))
	if len(steps) != 1 || steps[0].(doc)["image"] != "golang:1.27" {
		t.Fatalf("steps = %v", steps)
	}
	wantEntry(t, rep, caps.StepAction, caps.Emulated, "")
}

func TestUnknownActionIsLostWithItsRef(t *testing.T) {
	st := actionStep("x", "js-thing@v1", nil)
	files, rep := emit(t, project(jobWith(st, &ir.Step{ID: "run", Kind: ir.StepScript, Script: []string{"true"}})))
	wantEntry(t, rep, caps.StepAction, caps.Lost, "actions/js-thing@v1")
	if !strings.Contains(joinFiles(files), "# pipemaster:lost ") || !strings.Contains(joinFiles(files), "actions/js-thing@v1") {
		t.Fatalf("lost comment missing:\n%s", joinFiles(files))
	}
}

func artifactJobs() *ir.Project {
	up := actionStep("up", "upload-artifact@v4", map[string]string{"name": "bin", "path": "out/"})
	build := jobWith(up, &ir.Step{ID: "b", Kind: ir.StepScript, Script: []string{"make"}})
	build.ID = "build"
	build.Artifacts = []ir.Artifact{{Name: "bin", Paths: []string{"out/"}}}
	test := scriptJob("test", "ls bin")
	test.Needs = []ir.Need{{JobID: "build", Artifacts: true}}
	test.Cache = []ir.Cache{{Key: "k", Paths: []string{"~/.cache"}}}
	return project(build, test)
}

func TestArtifactsUseTheSharedWorkspace(t *testing.T) {
	p := artifactJobs()
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}}
	files, rep := emit(t, p)
	pl := pipelineDoc(t, files, "ci")
	if !hasNamed(get(pl, "spec", "workspaces"), "pipemaster-workspace") {
		t.Fatalf("pipeline workspaces = %v", get(pl, "spec", "workspaces"))
	}
	pt := list(get(pl, "spec", "tasks"))[0].(doc)
	if w := named(t, pt["workspaces"], "pipemaster-workspace"); w["workspace"] != "pipemaster-workspace" {
		t.Fatalf("binding = %v", w)
	}
	task := taskDoc(t, files, "ci-build")
	if m := named(t, get(task, "spec", "workspaces"), "pipemaster-workspace"); m["mountPath"] != "/pipemaster" {
		t.Fatalf("mount = %v", m)
	}
	var names []string
	for _, s := range list(get(task, "spec", "steps")) {
		names = append(names, s.(doc)["name"].(string))
	}
	if !strings.Contains(strings.Join(names, ","), "pipemaster-artifacts-save") {
		t.Fatalf("steps = %v", names)
	}
	wantEntry(t, rep, caps.JobArtifacts, caps.Emulated, "")
	wantEntry(t, rep, caps.JobCache, caps.Emulated, "nothing persists")
	tpl := docs(t, files[templateFile])[0]
	if get(list(get(list(get(tpl, "spec", "resourcetemplates"))[0], "spec", "workspaces"))[0], "volumeClaimTemplate", "spec", "accessModes") == nil {
		t.Fatalf("template has no volumeClaimTemplate: %v", tpl)
	}
}

func TestWorkspaceMarkerRestoresJobSettings(t *testing.T) {
	files, _ := emit(t, artifactJobs())
	back, rep := reparse(t, files)
	build := back.Pipelines[0].Jobs[0]
	test := back.Pipelines[0].Jobs[1]
	if len(build.Artifacts) != 1 || build.Artifacts[0].Name != "bin" || len(test.Cache) != 1 || !test.Needs[0].Artifacts {
		t.Fatalf("restored jobs = %+v / %+v", build, test)
	}
	for _, s := range build.Steps {
		if strings.HasPrefix(s.ID, "pipemaster-artifacts") {
			t.Fatalf("copy step %s was not dropped", s.ID)
		}
	}
	if hasEntry(rep, "marker ignored") || ext(build.Ext)["workspaces"] != nil || ext(build.Ext)["task_workspaces"] != nil {
		t.Fatalf("workspace noise left: %v %v", ext(build.Ext), rep.Entries)
	}
	if ext(back.Pipelines[0].Ext)["workspaces"] != nil {
		t.Fatalf("pipeline workspace noise: %v", ext(back.Pipelines[0].Ext))
	}
}

func TestEditedWorkspaceIsNotRestored(t *testing.T) {
	for name, edit := range map[string]func(string) string{
		"copy step": func(s string) string {
			i := strings.LastIndex(s, "mkdir -p '/pipemaster/artifacts/bin'")
			return s[:i] + "mkdir -p /elsewhere" + s[i+len("mkdir -p '/pipemaster/artifacts/bin'"):]
		},
		"mount path": func(s string) string { return strings.Replace(s, "mountPath: /pipemaster", "mountPath: /data", 1) },
	} {
		t.Run(name, func(t *testing.T) {
			files, _ := emit(t, artifactJobs())
			f := ".tekton/tasks/ci-build.yaml"
			files[f] = edit(files[f])
			if files[f] == "" {
				t.Fatal("edit emptied the file")
			}
			back, rep := reparse(t, files)
			if len(back.Pipelines[0].Jobs[0].Artifacts) != 0 || !hasEntry(rep, "marker ignored") {
				t.Fatalf("edited workspace was restored: %+v %v", back.Pipelines[0].Jobs[0], rep.Entries)
			}
		})
	}
}

func TestDroppedStepReportsItsSettingsLost(t *testing.T) {
	st := &ir.Step{ID: "p", Kind: ir.StepPlugin, Image: &ir.Image{Ref: "plugins/docker"}, With: map[string]ir.Value{"repo": {Lit: "x"}}, If: eqStr([]string{"event"}, "push")}
	_, rep := emit(t, project(jobWith(st)))
	wantEntry(t, rep, caps.StepIf, caps.Lost, "setting is lost")
	wantEntry(t, rep, caps.StepImage, caps.Lost, "setting is lost")
}
