//go:build behave

package behave

import "testing"

func jobWith(name string, passed []string, trigger bool) jobState {
	j := jobState{Name: name}
	j.Inputs = append(j.Inputs, struct {
		Resource string   `json:"resource"`
		Passed   []string `json:"passed"`
		Trigger  bool     `json:"trigger"`
	}{Resource: "source", Passed: passed, Trigger: trigger})
	return j
}

func finished(j jobState, status string) jobState {
	j.FinishedBuild = &struct {
		Status string `json:"status"`
	}{Status: status}
	return j
}

func TestPipelineSettled(t *testing.T) {
	a := finished(jobWith("a", nil, true), "succeeded")
	b := jobWith("b", []string{"a"}, true)
	for _, tc := range []struct {
		name   string
		jobs   []jobState
		builds []buildInfo
		want   bool
	}{
		{"a build runs", []jobState{a}, []buildInfo{{JobName: "a", Status: "started"}}, false},
		{"downstream job has no build yet", []jobState{a, b}, []buildInfo{{JobName: "a", Status: "succeeded"}}, false},
		{"downstream job built", []jobState{a, finished(b, "succeeded")}, []buildInfo{{JobName: "a", Status: "succeeded"}, {JobName: "b", Status: "succeeded"}}, true},
		{"upstream failed so downstream never starts", []jobState{finished(jobWith("a", nil, true), "failed"), b}, []buildInfo{{JobName: "a", Status: "failed"}}, true},
		{"new inputs wait", []jobState{func() jobState { j := a; j.HasNewInputs = true; return j }()}, []buildInfo{{JobName: "a", Status: "succeeded"}}, false},
		{"manual job has no trigger", []jobState{a, jobWith("m", nil, false)}, []buildInfo{{JobName: "a", Status: "succeeded"}}, true},
		{"no job state settles on builds", nil, []buildInfo{{JobName: "a", Status: "succeeded"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pipelineSettled(tc.jobs, tc.builds); got != tc.want {
				t.Fatalf("pipelineSettled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRegistryFailure(t *testing.T) {
	for log, want := range map[string]bool{
		`Get "https://index.docker.io/v2/": dial tcp: lookup index.docker.io: no such host`: true,
		`registry-1.docker.io: toomanyrequests: too many requests`:                          true,
		`image not found in registry`:                                                       false,
		`MARK a`:                                                                            false,
	} {
		if got := registryFailure(log); got != want {
			t.Errorf("registryFailure(%q) = %v, want %v", log, got, want)
		}
	}
}
