package roundtrip_test

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

func TestSanitizeID(t *testing.T) {
	cases := []struct{ target, in, want string }{
		{provider.GHA, "build linux", "build-linux"},
		{provider.GHA, "build", "build"},
		{provider.GHA, "1st", "_1st"},
		{provider.GHA, "a.b:c", "a-b-c"},
		{provider.GHA, "", "_"},
		{provider.GitLab, "build linux", "build linux"},
	}
	for _, c := range cases {
		if got := roundtrip.SanitizeID(c.target, c.in); got != c.want {
			t.Errorf("SanitizeID(%s, %q) = %q, want %q", c.target, c.in, got, c.want)
		}
	}
}

func TestAssignIDsCollisions(t *testing.T) {
	ids := []string{"build linux", "build-linux", "build.linux", "build:linux", "ok"}
	want := map[string]string{
		"build-linux": "build-linux", "ok": "ok",
		"build linux": "build-linux-2", "build.linux": "build-linux-3", "build:linux": "build-linux-4",
	}
	for range 5 {
		got := roundtrip.AssignIDs(provider.GHA, ids)
		for id, w := range want {
			if got[id] != w {
				t.Fatalf("AssignIDs[%q] = %q, want %q", id, got[id], w)
			}
		}
	}
	if got := roundtrip.AssignIDs(provider.GitLab, ids); got["build linux"] != "build linux" {
		t.Errorf("GitLab target must keep ids, got %q", got["build linux"])
	}
}

const spacedJobs = `stages: [build]
"build linux":
  stage: build
  script: [make linux]
"build-linux":
  stage: build
  script: [make other]
"build.linux":
  stage: build
  script: [make dot]
`

func jobIDs(p *ir.Project) []string {
	var out []string
	for _, n := range ir.Nodes(p) {
		if n.Job != nil && n.Step == nil {
			out = append(out, n.Job.ID)
		}
	}
	return out
}

func TestSanitizeIDs(t *testing.T) {
	src := fstest.MapFS{".gitlab-ci.yml": {Data: []byte(spacedJobs)}}
	a, b := prov(t, provider.GitLab), prov(t, provider.GHA)
	r := hops(t, src, a, b, false, true)

	mid, _, err := b.Parse(r.toFS)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, id := range jobIDs(mid) {
		got[id] = true
	}
	for _, want := range []string{"build-linux", "build-linux-2", "build-linux-3"} {
		if !got[want] {
			t.Errorf("GHA job ids %v lack %q", got, want)
		}
	}
	if same, diff := ir.Equal(r.src, r.back); !same {
		t.Errorf("ids were not restored on return:\n%s", diff)
	}
	if ids := jobIDs(r.back); len(ids) != 3 || ids[0] != "build linux" {
		t.Errorf("restored ids = %v", ids)
	}
}

// refProject is a GitLab-origin project whose job ids need renaming on GHA ("1build" starts with a digit)
// or on GitLab ("image" is a reserved key), with an output consumed through a needs reference.
func refProject(producer, consumer string) *ir.Project {
	step := func(id string, lines ...string) []*ir.Step {
		return []*ir.Step{{ID: id, Kind: ir.StepScript, Script: lines}}
	}
	return &ir.Project{Pipelines: []*ir.Pipeline{{
		ID: "gitlab-ci", Triggers: []ir.Trigger{{Kind: ir.TriggerPush}},
		Jobs: []*ir.Job{
			{ID: producer, Steps: step("script", "make"), Outputs: []ir.Output{{Name: "tag", Value: "v1"}}},
			{ID: consumer, Needs: []ir.Need{{JobID: producer}},
				Steps: step("script", "echo ${{ needs['"+producer+"'].outputs.tag }}")},
		},
	}}}
}

func gitlabSourceFS(t *testing.T, p *ir.Project) fstest.MapFS {
	t.Helper()
	files, _, err := prov(t, provider.GitLab).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	return toFS(files)
}

func TestSanitizeIDsRewritesReferences(t *testing.T) {
	src := gitlabSourceFS(t, refProject("1build", "image"))
	a, b := prov(t, provider.GitLab), prov(t, provider.GHA)

	r := hops(t, src, a, b, false, true)
	if same, diff := ir.Equal(r.src, r.back); !same {
		t.Fatalf("with metadata, ids and references were not restored:\n%s", diff)
	}
	var gha string
	for _, f := range r.toFiles {
		gha += string(f)
	}
	if !strings.Contains(gha, "needs._1build.outputs.tag") || strings.Contains(gha, "needs.1build") {
		t.Errorf("the GHA file must reference the sanitised id:\n%s", gha)
	}

	// Without metadata the sanitised id stays, and the reference still names it.
	r = hops(t, src, a, b, false, false)
	for _, n := range ir.Nodes(r.back) {
		if n.Job != nil && n.Step == nil && n.Job.ID == "1build" {
			t.Errorf("job id %q is invalid on GHA and was not sanitised", n.Job.ID)
		}
	}
	if same, _ := ir.Equal(r.src, r.back); same {
		t.Error("without metadata the sanitised id cannot return, but the projects compared equal")
	}
}

func TestReservedJobKeyRestoresWithAndWithoutMetadata(t *testing.T) {
	// "image" is valid on GHA and reserved on GitLab.
	ghaSrc := fstest.MapFS{".github/workflows/ci.yml": {Data: []byte(`name: ci
on: push
jobs:
  image:
    runs-on: ubuntu-latest
    outputs:
      tag: ${{ steps.t.outputs.tag }}
    steps:
      - id: t
        run: echo "tag=v1" >> "$GITHUB_OUTPUT"
  deploy:
    needs: image
    runs-on: ubuntu-latest
    steps:
      - run: echo ${{ needs.image.outputs.tag }}
`)}}
	a, b := prov(t, provider.GHA), prov(t, provider.GitLab)
	for _, withMeta := range []bool{true, false} {
		r := hops(t, ghaSrc, a, b, false, withMeta)
		if withMeta {
			if same, diff := ir.Equal(r.src, r.back); !same {
				t.Errorf("reserved job key not restored:\n%s", diff)
			}
		}
		got := jobIDs(r.back)
		slices.Sort(got)
		if !slices.Equal(got, []string{"deploy", "image"}) {
			t.Errorf("withMeta=%v: job ids = %v, want [deploy image]", withMeta, got)
		}
		for _, j := range r.back.Pipelines[0].Jobs {
			if j.ID == "deploy" && (len(j.Needs) != 1 || j.Needs[0].JobID != "image") {
				t.Errorf("withMeta=%v: needs = %+v, want image", withMeta, j.Needs)
			}
		}
		if !strings.Contains(string(r.toFiles[".gitlab-ci.yml"]), "image-job:") {
			t.Errorf("withMeta=%v: the job was not written under another key", withMeta)
		}
	}
}

func TestRewriteNeedsRefsSwapsAtOnce(t *testing.T) {
	pl := &ir.Pipeline{Jobs: []*ir.Job{{Steps: []*ir.Step{{Script: []string{
		"${{ needs.a.outputs.x }} ${{ needs.b.outputs.y }} ${{ needs['a'].outputs.x }} needs.ab.outputs.z literalneeds.a",
	}}}}}}
	emitkit.RewriteNeedsRefs(pl, map[string]string{"a": "b", "b": "a"})
	want := "${{ needs.b.outputs.x }} ${{ needs.a.outputs.y }} ${{ needs['b'].outputs.x }} needs.ab.outputs.z literalneeds.a"
	if got := pl.Jobs[0].Steps[0].Script[0]; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestLiteralShellVarSurvivesRoundTrip(t *testing.T) {
	src := fstest.MapFS{".github/workflows/ci.yml": {Data: []byte(`name: ci
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        os: [a, b]
        include:
          - os: c
    steps:
      - run: echo ${{ matrix.os }}
      - run: os=literal; echo $os
`)}}
	a, b := prov(t, provider.GHA), prov(t, provider.GitLab)
	r := hops(t, src, a, b, false, false)
	var got []string
	for _, st := range r.back.Pipelines[0].Jobs[0].Steps {
		got = append(got, st.Script...)
	}
	want := []string{"echo ${{ matrix.os }}", "os=literal; echo $os"}
	if !slices.Equal(got, want) {
		t.Errorf("scripts = %q, want %q", got, want)
	}
}
