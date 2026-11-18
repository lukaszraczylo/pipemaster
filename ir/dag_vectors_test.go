package ir_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/lukaszraczylo/pipemaster/internal/vectors"
	"github.com/lukaszraczylo/pipemaster/ir"
)

type dagNeed struct {
	Job      string `json:"job"`
	Optional bool   `json:"optional"`
}

type dagJob struct {
	ID    string    `json:"id"`
	Needs []dagNeed `json:"needs"`
}

type dagErr struct {
	Error string `json:"error"`
}

func TestDagVectors(t *testing.T) {
	cases := vectors.Load(t, "dag")
	for i, c := range cases {
		var in []dagJob
		if err := json.Unmarshal(c.In, &in); err != nil {
			t.Fatalf("case %d: decode in: %v", i, err)
		}
		var wantErr dagErr
		var wantLayers [][]string
		if len(c.Out) > 0 && c.Out[0] == '{' {
			if err := json.Unmarshal(c.Out, &wantErr); err != nil {
				t.Fatalf("case %d: decode out: %v", i, err)
			}
		} else if err := json.Unmarshal(c.Out, &wantLayers); err != nil {
			t.Fatalf("case %d: decode out: %v", i, err)
		}
		jobs := make([]*ir.Job, len(in))
		for k, j := range in {
			needs := make([]ir.Need, len(j.Needs))
			for n, nd := range j.Needs {
				needs[n] = ir.Need{JobID: nd.Job, Optional: nd.Optional}
			}
			jobs[k] = &ir.Job{ID: j.ID, Needs: needs}
		}
		got, err := ir.Layers(jobs)
		switch wantErr.Error {
		case "":
			if err != nil {
				t.Fatalf("case %d: in=%s: Go error %v, Lean layers %s", i, c.In, err, c.Out)
			}
			if diff := cmp.Diff(wantLayers, got); diff != "" {
				t.Fatalf("case %d: in=%s: layers differ (-lean +go):\n%s", i, c.In, diff)
			}
		case "cycle":
			if !errors.Is(err, ir.ErrCycle) {
				t.Fatalf("case %d: in=%s: Lean cycle, Go got %v / %v", i, c.In, got, err)
			}
		case "unknown_need":
			if !errors.Is(err, ir.ErrUnknownNeed) {
				t.Fatalf("case %d: in=%s: Lean unknown_need, Go got %v / %v", i, c.In, got, err)
			}
		default:
			t.Fatalf("case %d: unknown Lean error kind %q", i, wantErr.Error)
		}
	}
	t.Logf("%d cases", len(cases))
}
