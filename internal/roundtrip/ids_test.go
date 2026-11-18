package roundtrip_test

import (
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
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
