package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func imageJob(container *ir.Image, images ...string) *ir.Job {
	j := &ir.Job{ID: "build", Container: container}
	for i, ref := range images {
		st := &ir.Step{ID: "s" + string(rune('1'+i)), Kind: ir.StepScript, Script: []string{"echo " + ref}}
		if ref != "" && !strings.HasPrefix(ref, "-") {
			st.Image = &ir.Image{Ref: ref}
		}
		j.Steps = append(j.Steps, st)
	}
	return j
}

func imageBack(t *testing.T, j *ir.Job, edit func(string) string) (*ir.Job, string) {
	t.Helper()
	files, _ := emit(t, project(j))
	if edit != nil {
		files[ciFile] = edit(files[ciFile])
	}
	p, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	var detail []string
	for _, e := range rep.Entries {
		detail = append(detail, e.Detail)
	}
	return p.Pipelines[0].Jobs[0], strings.Join(detail, "\n")
}

func imageRefOf(i *ir.Image) string {
	if i == nil {
		return "<nil>"
	}
	return i.Ref
}

func TestStepImageStaysOnTheStepWhenAllStepsShareIt(t *testing.T) {
	got, _ := imageBack(t, imageJob(nil, "golang:1.25"), nil)
	if got.Container != nil || imageRefOf(got.Steps[0].Image) != "golang:1.25" {
		t.Errorf("container = %s, step image = %s; want nil and golang:1.25", imageRefOf(got.Container), imageRefOf(got.Steps[0].Image))
	}
}

func TestJobImageAndStepImageComeBackWhereTheyWere(t *testing.T) {
	got, _ := imageBack(t, imageJob(&ir.Image{Ref: "golang:1.25"}, "", "node:22"), nil)
	if imageRefOf(got.Container) != "golang:1.25" {
		t.Errorf("container = %s, want golang:1.25", imageRefOf(got.Container))
	}
	if imageRefOf(got.Steps[0].Image) != "<nil>" || imageRefOf(got.Steps[1].Image) != "node:22" {
		t.Errorf("step images = %s, %s; want nil, node:22", imageRefOf(got.Steps[0].Image), imageRefOf(got.Steps[1].Image))
	}
}

func TestSharedJobImageStaysTheJobContainer(t *testing.T) {
	got, detail := imageBack(t, imageJob(&ir.Image{Ref: "golang:1.25"}, "", ""), nil)
	if imageRefOf(got.Container) != "golang:1.25" || got.Steps[0].Image != nil {
		t.Errorf("container = %s, step image = %s", imageRefOf(got.Container), imageRefOf(got.Steps[0].Image))
	}
	if strings.Contains(detail, "marker ignored") {
		t.Errorf("unexpected entry: %s", detail)
	}
}

func TestEditedImageKeepsTheFileLayout(t *testing.T) {
	got, detail := imageBack(t, imageJob(nil, "golang:1.25"), func(s string) string {
		return replaceOnce(t, s, "image: golang:1.25", "image: golang:1.26")
	})
	if got.Steps[0].Image != nil && got.Steps[0].Image.Ref == "golang:1.25" {
		t.Errorf("the marked image came back after an edit")
	}
	if !strings.Contains(detail, "marker ignored") {
		t.Errorf("no marker ignored entry: %s", detail)
	}
}
