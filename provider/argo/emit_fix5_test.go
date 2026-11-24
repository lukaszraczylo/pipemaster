package argo_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func mixedJob() *ir.Job {
	return &ir.Job{ID: "build", Steps: []*ir.Step{
		{ID: "one", Kind: ir.StepScript, Script: []string{"echo one"}},
		actionStep("go", "actions", "setup-go", map[string]string{"go-version": "1.22"}),
		{ID: "two", Kind: ir.StepScript, Script: []string{"echo two"}},
		actionStep("up", "actions", "upload-artifact", map[string]string{"name": "bin", "path": "out"}),
		{ID: "three", Kind: ir.StepScript, Script: []string{"echo three"}},
		{ID: "four", Kind: ir.StepScript, Script: []string{"echo four"}},
	}}
}

func midCheckoutJob() *ir.Job {
	return &ir.Job{ID: "build", Steps: []*ir.Step{
		{ID: "one", Kind: ir.StepScript, Script: []string{"echo one"}},
		actionStep("co", "actions", "checkout", nil),
		{ID: "two", Kind: ir.StepScript, Script: []string{"echo two"}},
	}}
}

func jobParse(t *testing.T, job *ir.Job, edit func(*testing.T, *containerEdit)) ([]string, *ir.Job, string) {
	t.Helper()
	files, _ := emit(t, project(job))
	src := files[ciFile]
	if edit != nil {
		head, ce, tail := splitContainers(t, src)
		edit(t, ce)
		src = head + ce.text() + tail
		files[ciFile] = src
	}
	p, rep, err := parseRepo(files)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	j := p.Pipelines[0].Jobs[0]
	var ids []string
	for _, st := range j.Steps {
		id := st.ID
		if st.Kind == ir.StepAction {
			id += "!"
		}
		ids = append(ids, id)
	}
	return ids, j, joinEntries(rep)
}

func TestHeldSetupAndUploadKeepTheirPlace(t *testing.T) {
	ids, _, rep := jobParse(t, mixedJob(), nil)
	wantIDs(t, ids, "one", "go!", "two", "up!", "three", "four")
	if strings.Contains(rep, "marker ignored") {
		t.Errorf("unexpected marker ignored entry:\n%s", rep)
	}
}

func TestEditsAroundHeldSetupAndUploadLeaveThemNextToTheirContainers(t *testing.T) {
	cases := []struct {
		name string
		edit func(*testing.T, *containerEdit)
		want []string
	}{
		{"delete one", func(_ *testing.T, ce *containerEdit) { ce.remove("one") }, []string{"go!", "two", "up!", "three", "four"}},
		{"insert before one", func(t *testing.T, ce *containerEdit) { ce.insertBefore(t, "one", "new", "echo new") }, []string{"new", "one", "go!", "two", "up!", "three", "four"}},
		{"insert before setup", func(t *testing.T, ce *containerEdit) { ce.insertBefore(t, "go", "mid", "echo mid") }, []string{"one", "mid", "go!", "two", "up!", "three", "four"}},
		{"insert before upload", func(t *testing.T, ce *containerEdit) { ce.insertBefore(t, "up", "mid", "echo mid") }, []string{"one", "go!", "two", "mid", "up!", "three", "four"}},
		{"delete two", func(_ *testing.T, ce *containerEdit) { ce.remove("two") }, []string{"one", "go!", "up!", "three", "four"}},
		{"delete the later steps", func(_ *testing.T, ce *containerEdit) { ce.remove("three"); ce.remove("four") }, []string{"one", "go!", "two", "up!"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ids, _, rep := jobParse(t, mixedJob(), c.edit)
			wantIDs(t, ids, c.want...)
			if strings.Contains(rep, "marker ignored") || strings.Contains(rep, "repeats in the job") {
				t.Errorf("unexpected entry:\n%s", rep)
			}
		})
	}
}

func TestRemovedAnchorOfASetupOrUploadIsReported(t *testing.T) {
	for _, name := range []string{"go", "up"} {
		t.Run(name, func(t *testing.T) {
			ids, _, rep := jobParse(t, mixedJob(), func(_ *testing.T, ce *containerEdit) { ce.remove(name) })
			want := slices.DeleteFunc([]string{"one", "go!", "two", "up!", "three", "four"}, func(s string) bool { return s == name+"!" })
			wantIDs(t, ids, want...)
			if !strings.Contains(rep, "marker ignored: the placeholder container was removed") {
				t.Errorf("no entry for the removed container:\n%s", rep)
			}
		})
	}
}

func TestEditedAnchorOfASetupOrUploadKeepsTheFile(t *testing.T) {
	for _, name := range []string{"go", "up"} {
		t.Run(name, func(t *testing.T) {
			ids, j, rep := jobParse(t, mixedJob(), func(_ *testing.T, ce *containerEdit) {
				ce.blocks[name] = replaceFailLoud(ce.blocks[name], "- echo hi")
			})
			want := []string{"one", "go!", "two", "up!", "three", "four"}
			want[slices.Index(want, name+"!")] = name
			wantIDs(t, ids, want...)
			if st := stepByID(j, name); st == nil || st.Kind != ir.StepScript {
				t.Errorf("step %s = %+v, want the file script step", name, st)
			}
			if !strings.Contains(rep, "marker ignored: the placeholder container was edited") {
				t.Errorf("no entry for the edited container:\n%s", rep)
			}
		})
	}
}

func TestDeletingAPlaceholderDoesNotShiftOtherHeldSteps(t *testing.T) {
	job := mixedJob()
	job.Steps = slices.Insert(job.Steps, 0, actionStep("frob", "acme", "frobnicate", nil))
	ids, _, rep := jobParse(t, job, func(_ *testing.T, ce *containerEdit) { ce.remove("frob") })
	wantIDs(t, ids, "one", "go!", "two", "up!", "three", "four")
	if !strings.Contains(rep, "the placeholder container was removed") {
		t.Errorf("no entry for the removed container:\n%s", rep)
	}
}

func TestCheckoutAfterAStepKeepsItsPlace(t *testing.T) {
	ids, _, rep := jobParse(t, midCheckoutJob(), nil)
	wantIDs(t, ids, "one", "co!", "two")
	if strings.Contains(rep, "marker ignored") {
		t.Errorf("unexpected marker ignored entry:\n%s", rep)
	}
	ids, _, _ = jobParse(t, midCheckoutJob(), func(_ *testing.T, ce *containerEdit) { ce.remove("one") })
	wantIDs(t, ids, "co!", "two")
}

func TestMixedHeldJobPassesArgoLint(t *testing.T) {
	linttool.RequireArgo(t)
	for _, job := range []*ir.Job{mixedJob(), midCheckoutJob()} {
		files, _ := emit(t, project(job))
		if out, err := linttool.ArgoLint(t, ciFile, []byte(files[ciFile])); err != nil {
			t.Fatalf("argo lint: %v\n%s\n%s", err, out, files[ciFile])
		}
	}
}
