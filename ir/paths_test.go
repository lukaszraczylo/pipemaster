package ir

import (
	"reflect"
	"testing"
)

func TestExtNodesPaths(t *testing.T) {
	p := &Project{Pipelines: []*Pipeline{{
		ID:       "ci",
		Triggers: []Trigger{{}, {}},
		Jobs:     []*Job{{ID: "build", Steps: []*Step{{ID: "s1"}}}},
		Finally:  []*Job{{ID: "notify"}},
	}}}
	var got []string
	for _, n := range ExtNodes(p) {
		got = append(got, n.Path)
	}
	want := []string{
		"pipeline/ci", "pipeline/ci/trigger/0", "pipeline/ci/trigger/1",
		"pipeline/ci/job/build", "pipeline/ci/job/build/step/s1", "pipeline/ci/job/notify",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}
