package roundtrip_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/internal/vectors"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// leg emits proj through target, parses the files back and re-attaches the metadata of mp, as hops does.
func leg(t *testing.T, label string, proj, mp *ir.Project, source string, target provider.Provider) *ir.Project {
	t.Helper()
	files, rep, err := target.Emit(proj)
	if err != nil {
		t.Fatalf("%s: emit %s: %v", label, target.ID(), err)
	}
	m := roundtrip.NewMeta(source, mp, target, rep)
	back, _, err := target.Parse(toFS(files))
	if err != nil {
		t.Fatalf("%s: parse %s: %v", label, target.ID(), err)
	}
	roundtrip.Restore(back, m)
	return back
}

// plain emits proj through target and parses it back with no metadata.
func plain(t *testing.T, label string, proj *ir.Project, target provider.Provider) *ir.Project {
	t.Helper()
	files, _, err := target.Emit(proj)
	if err != nil {
		t.Fatalf("%s: emit %s: %v", label, target.ID(), err)
	}
	back, _, err := target.Parse(toFS(files))
	if err != nil {
		t.Fatalf("%s: parse %s: %v", label, target.ID(), err)
	}
	return back
}

// TestCrossVectors checks both two-hop round trips against the Lean model (cross_roundtrip, cross_roundtrip_rev).
func TestCrossVectors(t *testing.T) {
	cases := vectors.Load(t, "cross")
	if len(cases) < vectors.MinAdapterCases {
		t.Fatalf("%d cases, want at least %d", len(cases), vectors.MinAdapterCases)
	}
	gh, gl := prov(t, provider.GHA), prov(t, provider.GitLab)
	for i, c := range cases {
		label := fmt.Sprintf("case %d %s", i, c.In)
		var out struct {
			Cross json.RawMessage `json:"cross"`
			Rev   json.RawMessage `json:"rev"`
		}
		if err := json.Unmarshal(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		fresh := func() *ir.Project {
			p, err := ir.FromCore(c.In)
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			return p
		}
		viaGitlab := leg(t, label, fresh(), fresh(), provider.GHA, gl)
		cross, err := ir.CoreOf(plain(t, label, viaGitlab, gh))
		if err != nil {
			t.Fatalf("%s: cross: %v", label, err)
		}
		vectors.DiffCore(t, label+" cross", out.Cross, cross)

		viaGha := leg(t, label, fresh(), fresh(), provider.GitLab, gh)
		files, _, err := gl.Emit(viaGha)
		if err != nil {
			t.Fatalf("%s: emit gitlab: %v", label, err)
		}
		back, _, err := gl.Parse(toFS(files))
		if err != nil {
			t.Fatalf("%s: parse gitlab: %v", label, err)
		}
		_, ownRep, err := gl.Emit(fresh())
		if err != nil {
			t.Fatalf("%s: emit own: %v", label, err)
		}
		roundtrip.Restore(back, roundtrip.NewMeta(provider.GHA, fresh(), gl, ownRep))
		rev, err := ir.CoreOf(back)
		if err != nil {
			t.Fatalf("%s: rev: %v", label, err)
		}
		vectors.DiffCore(t, label+" rev", out.Rev, rev)
		if t.Failed() {
			return
		}
	}
}
