package gitlab_test

import (
	"reflect"
	"testing"
)

func TestMarkerCollapsesContinueOnError(t *testing.T) {
	pl, rep := pipeline(t, `
j:
  script:
    # pipemaster:emulated step.continue_on_error pipeline/gitlab-ci/job/j/step/script
    - make test || true
    - make lint || true
`)
	st := job(t, pl, "j").Steps[0]
	if !st.ContinueOnError || !reflect.DeepEqual(st.Script, []string{"make test", "make lint"}) {
		t.Errorf("step = %+v", st)
	}
	if hasEntry(rep, "marker not collapsed") {
		t.Errorf("a collapsed marker is not reported: %+v", rep.Entries)
	}
}

func TestUnknownMarkerReported(t *testing.T) {
	pl, rep := pipeline(t, `
j:
  script:
    # pipemaster:emulated job.retry pipeline/gitlab-ci/job/j
    - make
`)
	if got := job(t, pl, "j").Steps[0].Script; !reflect.DeepEqual(got, []string{"make"}) {
		t.Errorf("script = %v", got)
	}
	if !hasEntry(rep, "marker not collapsed") {
		t.Errorf("report = %+v", rep.Entries)
	}
}

func TestMarkerOnMissingStepReported(t *testing.T) {
	_, rep := pipeline(t, "# pipemaster:emulated step.continue_on_error pipeline/gitlab-ci/job/j/step/nope\nj:\n  script: [make]\n")
	if !hasEntry(rep, "marker not collapsed") {
		t.Errorf("report = %+v", rep.Entries)
	}
}
