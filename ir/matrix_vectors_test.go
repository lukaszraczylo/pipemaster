package ir_test

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/lukaszraczylo/pipemaster/internal/vectors"
	"github.com/lukaszraczylo/pipemaster/ir"
)

type matrixAxis struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

type matrixIn struct {
	Axes    []matrixAxis  `json:"axes"`
	Include [][][2]string `json:"include"`
	Exclude [][][2]string `json:"exclude"`
}

func toCombo(pairs [][2]string) ir.Combo {
	c := make(ir.Combo, len(pairs))
	for i, p := range pairs {
		c[i] = ir.KV{Key: p[0], Value: p[1]}
	}
	return c
}

func toPairs(c ir.Combo) [][2]string {
	out := make([][2]string, len(c))
	for i, kv := range c {
		out[i] = [2]string{kv.Key, kv.Value}
	}
	return out
}

func TestMatrixVectors(t *testing.T) {
	cases := vectors.Load(t, "matrix")
	for i, c := range cases {
		var in matrixIn
		if err := json.Unmarshal(c.In, &in); err != nil {
			t.Fatalf("case %d: decode in: %v", i, err)
		}
		var want [][][2]string
		if err := json.Unmarshal(c.Out, &want); err != nil {
			t.Fatalf("case %d: decode out: %v", i, err)
		}
		m := &ir.Matrix{}
		for _, a := range in.Axes {
			m.Axes = append(m.Axes, ir.Axis{Name: a.Name, Values: a.Values})
		}
		for _, inc := range in.Include {
			m.Include = append(m.Include, toCombo(inc))
		}
		for _, exc := range in.Exclude {
			m.Exclude = append(m.Exclude, toCombo(exc))
		}
		got := ir.Expand(m)
		gotPairs := make([][][2]string, len(got))
		for k, g := range got {
			gotPairs[k] = toPairs(g)
		}
		if len(want) == 0 && len(gotPairs) == 0 {
			continue
		}
		if diff := cmp.Diff(want, gotPairs); diff != "" {
			t.Fatalf("case %d: in=%s: combos differ (-lean +go):\n%s", i, c.In, diff)
		}
	}
	t.Logf("%d cases", len(cases))
}
