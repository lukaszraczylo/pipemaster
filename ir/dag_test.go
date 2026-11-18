package ir_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"pgregory.net/rapid"
)

func jobNeeding(id string, needs ...string) *ir.Job {
	j := &ir.Job{ID: id}
	for _, n := range needs {
		j.Needs = append(j.Needs, ir.Need{JobID: n})
	}
	return j
}

func TestLayersDiamond(t *testing.T) {
	got, err := ir.Layers([]*ir.Job{
		jobNeeding("d", "b", "c"), jobNeeding("c", "a"), jobNeeding("b", "a"), jobNeeding("a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"a"}, {"b", "c"}, {"d"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestLayersEmpty(t *testing.T) {
	got, err := ir.Layers(nil)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v err %v, want empty non-nil slice", got, err)
	}
}

// genDAG draws jobs j0..jn-1 where each job needs only lower-indexed jobs.
func genDAG(t *rapid.T) []*ir.Job {
	n := rapid.IntRange(1, 12).Draw(t, "n")
	jobs := make([]*ir.Job, n)
	for i := range jobs {
		jobs[i] = &ir.Job{ID: fmt.Sprintf("j%02d", i)}
		for k := 0; k < i; k++ {
			if rapid.Bool().Draw(t, "edge") {
				jobs[i].Needs = append(jobs[i].Needs, ir.Need{JobID: jobs[k].ID})
			}
		}
	}
	return jobs
}

func layerIndex(layers [][]string) map[string]int {
	idx := map[string]int{}
	for i, l := range layers {
		for _, id := range l {
			idx[id] = i
		}
	}
	return idx
}

func TestLayersRespectsEveryEdge(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		jobs := genDAG(t)
		layers, err := ir.Layers(jobs)
		if err != nil {
			t.Fatal(err)
		}
		idx := layerIndex(layers)
		if len(idx) != len(jobs) {
			t.Fatalf("placed %d of %d jobs", len(idx), len(jobs))
		}
		for _, j := range jobs {
			for _, n := range j.Needs {
				if idx[n.JobID] >= idx[j.ID] {
					t.Fatalf("need %s (layer %d) not before %s (layer %d)", n.JobID, idx[n.JobID], j.ID, idx[j.ID])
				}
			}
		}
		for _, l := range layers {
			if len(l) == 0 {
				t.Fatal("empty layer")
			}
			for i := 1; i < len(l); i++ {
				if l[i-1] >= l[i] {
					t.Fatalf("layer not sorted: %v", l)
				}
			}
		}
	})
}

func TestLayersCountIsLongestPathPlusOne(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		jobs := genDAG(t)
		depth := map[string]int{}
		longest := 0
		for _, j := range jobs { // generated in topological order
			d := 1
			for _, n := range j.Needs {
				if depth[n.JobID]+1 > d {
					d = depth[n.JobID] + 1
				}
			}
			depth[j.ID] = d
			if d > longest {
				longest = d
			}
		}
		layers, err := ir.Layers(jobs)
		if err != nil {
			t.Fatal(err)
		}
		if len(layers) != longest {
			t.Fatalf("got %d layers, longest path has %d nodes", len(layers), longest)
		}
	})
}

func TestLayersCycle(t *testing.T) {
	_, err := ir.Layers([]*ir.Job{jobNeeding("a", "b"), jobNeeding("b", "a"), jobNeeding("c")})
	if !errors.Is(err, ir.ErrCycle) {
		t.Fatalf("want ErrCycle, got %v", err)
	}
	if !strings.Contains(err.Error(), `"a"`) {
		t.Fatalf("message must name a job: %v", err)
	}
}

func TestLayersSelfCycle(t *testing.T) {
	if _, err := ir.Layers([]*ir.Job{jobNeeding("a", "a")}); !errors.Is(err, ir.ErrCycle) {
		t.Fatalf("want ErrCycle, got %v", err)
	}
}

func TestLayersUnknownNeed(t *testing.T) {
	_, err := ir.Layers([]*ir.Job{jobNeeding("a", "ghost")})
	if !errors.Is(err, ir.ErrUnknownNeed) {
		t.Fatalf("want ErrUnknownNeed, got %v", err)
	}
	if !strings.Contains(err.Error(), `"a"`) || !strings.Contains(err.Error(), `"ghost"`) {
		t.Fatalf("message must name both jobs: %v", err)
	}
}

func TestLayersOptionalUnknownNeedAddsNoEdge(t *testing.T) {
	a := &ir.Job{ID: "a", Needs: []ir.Need{{JobID: "ghost", Optional: true}}}
	got, err := ir.Layers([]*ir.Job{a})
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"a"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestValidateOptionalUnknownNeedOK(t *testing.T) {
	a := &ir.Job{ID: "a", Needs: []ir.Need{{JobID: "ghost", Optional: true}}}
	if err := ir.Validate(one(a)); err != nil {
		t.Fatalf("optional unknown need must validate, got %v", err)
	}
	b := &ir.Job{ID: "b", Needs: []ir.Need{{JobID: "ghost"}}}
	if err := ir.Validate(one(b)); err == nil {
		t.Fatal("required unknown need must still fail")
	}
}
