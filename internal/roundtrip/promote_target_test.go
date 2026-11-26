package roundtrip_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
)

const dronePromoteTarget = "kind: pipeline\ntype: docker\nname: ci\ntrigger:\n  event:\n    - promote\n    - rollback\n  target:\n    - production\nsteps:\n  - name: deploy\n    image: alpine:3.20\n    commands:\n      - echo deploy\n"

// A Drone file that filters to promote and rollback names events, so a report must not say that its file names no trigger.
func TestDronePromoteTargetReportNamesTheFilter(t *testing.T) {
	tests := []struct {
		to      string
		feature caps.Feature
		want    string
	}{
		{"gha", caps.TriggerAPI, "the source runs on the events promote, rollback and its file has no trigger form for them"},
		{"gitlab", caps.TriggerAPI, "the source runs on the events promote, rollback and its file has no trigger form for them"},
		{"woodpecker", caps.JobIf, "the Drone target filter (https://docs.drone.io/pipeline/triggers/) has no Woodpecker form"},
	}
	for _, tc := range tests {
		t.Run(tc.to, func(t *testing.T) {
			_, rep := emitFiles(t, "drone", tc.to, map[string]string{".drone.yml": dronePromoteTarget})
			found := false
			for _, e := range rep.Entries {
				if e.Feature == tc.feature && strings.Contains(e.Detail, tc.want) {
					found = true
				}
				if strings.Contains(e.Detail, "when its file names no trigger") {
					t.Errorf("a file that names promote and rollback is not a file with no trigger: %s", e.Detail)
				}
			}
			if !found {
				t.Errorf("no %s entry with %q in %+v", tc.feature, tc.want, rep.Entries)
			}
		})
	}
}
