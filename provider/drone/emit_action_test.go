package drone_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func action(id, owner, repo, ref string, with map[string]string) *ir.Step {
	st := &ir.Step{ID: id, Kind: ir.StepAction, Action: &ir.ActionRef{Kind: ir.ActionRepo, Owner: owner, Repo: repo, Ref: ref}}
	if with != nil {
		st.With = map[string]ir.Value{}
		for k, v := range with {
			st.With[k] = ir.Value{Lit: v}
		}
	}
	return st
}

func TestCheckoutIsTheImplicitClone(t *testing.T) {
	out, rep := emitText(t, gproject(ghaOrigin, gjob("build", action("co", "actions", "checkout", "v4", map[string]string{"fetch-depth": "5"}), gstep("x", "true"))))
	d := docsOf(t, out)[0]
	clone, _ := d["clone"].(map[string]any)
	if clone["depth"] != 5 || len(stepsOf(d)) != 1 {
		t.Fatalf("clone %v, steps %d\n%s", clone, len(stepsOf(d)), out)
	}
	if !hasLine(out, "# pipemaster:emulated step.action pipeline/drone/job/build/step/co") {
		t.Fatalf("an action leaves a marker:\n%s", out)
	}
	if _, has := levelOf(rep, caps.StepAction, "step/co"); !has {
		t.Fatalf("no ledger entry: %v", rep.Entries)
	}
}

func TestSetupActionSelectsTheImage(t *testing.T) {
	later := &ir.Step{ID: "x", Kind: ir.StepScript, Script: []string{"go test"}}
	out, rep := emitText(t, gproject(ghaOrigin, gjob("build", action("go", "actions", "setup-go", "v5", map[string]string{"go-version": "1.22"}), later)))
	if !strings.Contains(out, "image: golang:1.22") {
		t.Fatalf("the later step must run in the selected image:\n%s", out)
	}
	wantLevel(t, rep, caps.StepAction, "step/go", caps.Emulated)
}

func TestCacheAndArtifactsShareTheWorkspace(t *testing.T) {
	steps := []*ir.Step{
		action("c", "actions", "cache", "v4", map[string]string{"key": "k", "path": "~/.cache"}),
		action("up", "actions", "upload-artifact", "v4", map[string]string{"name": "bin", "path": "out/"}),
		action("down", "actions", "download-artifact", "v4", map[string]string{"name": "bin"}),
		gstep("x", "true"),
	}
	out, rep := emitText(t, gproject(ghaOrigin, gjob("build", steps...)))
	wantLevel(t, rep, caps.StepAction, "step/c", caps.Emulated)
	wantLevel(t, rep, caps.StepAction, "step/up", caps.Emulated)
	if lv, _ := levelOf(rep, caps.StepAction, "step/down"); lv != caps.Emulated {
		t.Fatalf("a download of an upload in the same document is emulated: %v", rep.Entries)
	}
	if n := countLines(out, "# pipemaster:emulated step.action"); n != 3 {
		t.Fatalf("want a marker per action step, got %d\n%s", n, out)
	}
}

func TestDownloadWithoutUploadIsLost(t *testing.T) {
	_, rep := emitText(t, gproject(ghaOrigin, gjob("build", action("down", "actions", "download-artifact", "v4", map[string]string{"name": "bin"}), gstep("x", "true"))))
	wantLevel(t, rep, caps.StepAction, "step/down", caps.Lost)
}

func TestUnknownActionIsLostWithItsRef(t *testing.T) {
	out, rep := emitText(t, gproject(ghaOrigin, gjob("build", action("a", "acme", "deploy-js", "v1", nil), gstep("x", "true"))))
	wantLevel(t, rep, caps.StepAction, "step/a", caps.Lost)
	if !strings.Contains(out, "# pipemaster:lost step pipeline/drone/job/build/step/a uses acme/deploy-js@v1") {
		t.Fatalf("the ref must stay in the file:\n%s", out)
	}
}

func TestUnmappedActionInputIsLost(t *testing.T) {
	_, rep := emitText(t, gproject(ghaOrigin, gjob("build", action("co", "actions", "checkout", "v4", map[string]string{"lfs": "true"}), gstep("x", "true"))))
	if !hasEntry(rep, "input lfs has no Drone equivalent") {
		t.Fatalf("report = %v", rep.Entries)
	}
}
