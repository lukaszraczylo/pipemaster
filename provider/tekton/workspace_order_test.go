package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
)

const parallelWriter = "  - name: produce2\n    workspaces:\n    - {name: out, workspace: shared}\n    taskSpec:\n      workspaces:\n      - name: out\n      steps:\n      - {name: w, image: alpine, script: 'echo two > $(workspaces.out.path)/two.txt'}\n  - name: consume\n    runAfter: "

func TestParallelWritersNameTheWinnerByNeedOrder(t *testing.T) {
	for _, tc := range []struct{ after, winner string }{
		{"[produce, produce2]", "files of produce2, the last"},
		{"[produce2, produce]", "files of produce, the last"},
	} {
		t.Run(tc.after, func(t *testing.T) {
			src := strings.Replace(twoTaskWorkspace, "  - name: consume\n    runAfter: ", parallelWriter, 1)
			src = strings.Replace(src, "runAfter: [produce]\n", "runAfter: "+tc.after+"\n", 1)
			pl, rep := parseYAML(t, src)
			cons := job(t, pl, "consume")
			if len(cons.Needs) != 2 {
				t.Fatalf("needs = %+v", cons.Needs)
			}
			e := entryAt(rep, "pipeline/ci/job/consume", caps.JobNeedsArtifacts, caps.Emulated)
			if e == nil || !strings.Contains(e.Detail, tc.winner) {
				t.Errorf("the entry must name the winner %q: %+v", tc.winner, e)
			}
		})
	}
}
