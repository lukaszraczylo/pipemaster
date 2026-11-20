package woodpecker_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func use(owner, repo, ref string, with map[string]string) *ir.Step {
	st := &ir.Step{ID: owner + "-" + repo, Kind: ir.StepAction, Action: &ir.ActionRef{Kind: ir.ActionRepo, Owner: owner, Repo: repo, Ref: ref}}
	if len(with) > 0 {
		st.With = map[string]ir.Value{}
		for k, v := range with {
			st.With[k] = ir.Value{Lit: v}
		}
	}
	return st
}

func TestCheckoutIsTheImplicitClone(t *testing.T) {
	j := wpJob("ci", use("actions", "checkout", "v4", map[string]string{"fetch-depth": "1"}), wpStep("s", "make"))
	body, rep := one(t, wpProject(ghaOrigin, j))
	if strings.Contains(body, "checkout") && !strings.Contains(body, "# pipemaster") {
		t.Errorf("checkout must not be a step:\n%s", body)
	}
	if !strings.Contains(body, "depth: 1") || !strings.Contains(body, "image: docker.io/woodpeckerci/plugin-git") {
		t.Errorf("fetch-depth must reach clone.git.settings.depth:\n%s", body)
	}
	if reportHas(rep, caps.StepAction, caps.Lost) {
		t.Errorf("a mapped checkout input is not lost: %+v", rep.Entries)
	}
}

func TestCheckoutInputWithoutEquivalentIsReported(t *testing.T) {
	j := wpJob("ci", use("actions", "checkout", "v4", map[string]string{"submodules": "recursive"}), wpStep("s", "make"))
	_, rep := one(t, wpProject(ghaOrigin, j))
	if !reportDetail(rep, caps.StepAction, "submodules") {
		t.Errorf("an unmapped input must be reported: %+v", rep.Entries)
	}
}

func TestCacheIsSharedWorkspace(t *testing.T) {
	j := wpJob("ci", use("actions", "cache", "v4", map[string]string{"path": "~/.cache", "key": "k"}), wpStep("s", "make"))
	body, rep := one(t, wpProject(ghaOrigin, j))
	if strings.Contains(body, "actions/cache") && !strings.Contains(body, "# pipemaster") {
		t.Errorf("the cache action is not a step:\n%s", body)
	}
	if !reportHas(rep, caps.StepAction, caps.Emulated) || !strings.Contains(body, "pipemaster:emulated step.action") {
		t.Errorf("want an emulated entry and a marker:\n%s\n%+v", body, rep.Entries)
	}
}

func TestArtifactsWithinOneWorkflow(t *testing.T) {
	j := wpJob("ci",
		use("actions", "upload-artifact", "v4", map[string]string{"name": "bin", "path": "out"}),
		use("actions", "download-artifact", "v4", map[string]string{"name": "bin"}),
	)
	j.Steps = append(j.Steps, wpStep("s", "ls"))
	_, rep := one(t, wpProject(ghaOrigin, j))
	if reportHas(rep, caps.StepAction, caps.Lost) || !reportHas(rep, caps.StepAction, caps.Emulated) {
		t.Errorf("an upload and download in one workflow are emulated: %+v", rep.Entries)
	}
}

func TestArtifactsAcrossWorkflowsAreLost(t *testing.T) {
	a := wpJob("a", use("actions", "upload-artifact", "v4", map[string]string{"name": "bin", "path": "out"}), wpStep("s", "make"))
	b := wpJob("b", use("actions", "download-artifact", "v4", map[string]string{"name": "bin"}), wpStep("s", "ls"))
	b.Needs = []ir.Need{{JobID: "a", Artifacts: true}}
	files, rep := emitFiles(t, wpProject(ghaOrigin, a, b))
	if !strings.Contains(files[dirPrefix+"b.yaml"], "# pipemaster:lost step") {
		t.Errorf("the lost download needs a comment:\n%s", files[dirPrefix+"b.yaml"])
	}
	found := false
	for _, e := range rep.Entries {
		if strings.HasSuffix(e.IRPath, "job/b/step/actions-download-artifact") && e.Feature == caps.StepAction && e.Level == caps.Lost {
			found = true
		}
	}
	if !found || !reportHas(rep, caps.JobNeedsArtifacts, caps.Lost) {
		t.Errorf("want a lost download and a lost needs.artifacts: %+v", rep.Entries)
	}
}

func TestSetupActionSelectsTheImageOfLaterSteps(t *testing.T) {
	first := wpStep("before", "x")
	first.Image = nil
	later := wpStep("after", "go test")
	later.Image = nil
	j := wpJob("ci", first, use("actions", "setup-go", "v5", map[string]string{"go-version": "1.23"}), later)
	body, rep := one(t, wpProject(ghaOrigin, j))
	if !strings.Contains(body, "image: ubuntu:latest") || !strings.Contains(body, "image: golang:1.23") {
		t.Errorf("only the later step takes the image:\n%s", body)
	}
	if strings.Index(body, "golang:1.23") < strings.Index(body, "name: after") {
		t.Errorf("the earlier step must keep the default image:\n%s", body)
	}
	if !reportHas(rep, caps.StepAction, caps.Emulated) {
		t.Errorf("entries: %+v", rep.Entries)
	}
}

func TestUnknownActionIsLostWithItsReference(t *testing.T) {
	j := wpJob("ci", use("acme", "deploy", "v9", nil), wpStep("s", "make"))
	body, rep := one(t, wpProject(ghaOrigin, j))
	if !strings.Contains(body, "# pipemaster:lost step pipeline/woodpecker/job/ci/step/acme-deploy uses acme/deploy@v9") {
		t.Errorf("the reference must stay in a comment:\n%s", body)
	}
	if !reportHas(rep, caps.StepAction, caps.Lost) {
		t.Errorf("entries: %+v", rep.Entries)
	}
}

func TestOnlyActionStepsLeaveAPlaceholder(t *testing.T) {
	j := wpJob("ci", use("actions", "checkout", "v4", nil))
	body, rep := one(t, wpProject(ghaOrigin, j))
	if !strings.Contains(body, "name: noop") || !reportHas(rep, caps.StepScript, caps.Emulated) {
		t.Errorf("Woodpecker needs one step:\n%s", body)
	}
}

func TestLongAxisIsExpanded(t *testing.T) {
	var vals []string
	for _, v := range "abcdefghijk" {
		vals = append(vals, string(v))
	}
	j := wpJob("ci", wpStep("s", "x"))
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "A", Values: vals}}}
	body, rep := one(t, wpProject(ghaOrigin, j))
	if strings.Count(body, "- A:") != 11 || !reportHas(rep, caps.JobMatrix, caps.Emulated) {
		t.Errorf("an axis over the 10-value limit becomes include entries:\n%s", body)
	}
}

func TestPlatformLabelFromTemplate(t *testing.T) {
	j := wpJob("ci", wpStep("s", "x"))
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "platform", Values: []string{"linux/amd64", "linux/arm64"}}}}
	j.Runner = ir.Runner{Labels: []string{"${{ matrix.platform }}"}}
	body, _ := one(t, wpProject("woodpecker", j))
	if !strings.Contains(body, "platform: ${platform}") {
		t.Errorf("the platform label must substitute the matrix axis:\n%s", body)
	}
}
