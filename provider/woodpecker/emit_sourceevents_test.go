package woodpecker_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

// A source with no trigger runs on cron, custom and the deployment events, so the workflow lists the Woodpecker
// events for them and no event of the source is dropped without a report entry.
func TestNoTriggerSourceKeepsItsOtherEvents(t *testing.T) {
	cases := []struct {
		origin string
		lost   string
		want   []string
	}{
		{origin: "drone", want: []string{"push", "pull_request", "tag", "cron", "manual", "deployment"}},
		{origin: "gitlab", want: []string{"cron", "manual"}, lost: "pipeline, parent_pipeline"},
	}
	for _, c := range cases {
		t.Run(c.origin, func(t *testing.T) {
			p := wpProject(c.origin, wpJob("build", wpStep("s", "make")))
			p.Pipelines[0].Triggers = nil
			if c.origin == "gitlab" {
				p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag}}
			}
			files, rep := emitFiles(t, p)
			var doc string
			for _, f := range files {
				doc = f
			}
			for _, ev := range c.want {
				if !strings.Contains(doc, ev) {
					t.Errorf("event %s missing:\n%s", ev, doc)
				}
			}
			if c.lost != "" && !hasEntry(rep, c.lost) {
				t.Errorf("no entry names the events %q: %+v", c.lost, rep.Entries)
			}
		})
	}
}
