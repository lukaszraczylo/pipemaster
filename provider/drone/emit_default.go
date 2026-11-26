package drone

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	dexpr "github.com/lukaszraczylo/pipemaster/expr/drone"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// sourceDefaultEvents are the Drone events of a document for a source that names no event. A document with no
// trigger runs on every Drone event, including cron, custom, promote and rollback, which such a source never
// starts. The list is the one the Woodpecker emitter writes for the same source.
var sourceDefaultEvents = []string{eventPush, eventPR, eventTag}

// defaultEventsArg marks a document whose event list the emitter wrote for a source with no event, so the parser
// reads the list back as no trigger.
type defaultEventsArg struct {
	Events []string `json:"default_events"`
}

// namesNoEvents is true for a source whose pipelines start by a request and not by an event (Tekton, Argo, Concourse).
func (e *emitter) namesNoEvents() bool {
	return e.origin == provider.Tekton || e.origin == provider.Argo || e.origin == provider.Concourse
}

// defaultEvents is the condition of a document of a pipeline that has no trigger, for a source that names no event.
func (e *emitter) defaultEvents(pl *ir.Pipeline) (condSet, bool, error) {
	if len(pl.Triggers) > 0 || !e.namesNoEvents() {
		return condSet{}, false, nil
	}
	e.emulate("pipeline/"+pl.ID, caps.TriggerPush, "trigger event "+strings.Join(sourceDefaultEvents, ", "),
		"the source names no event; a Drone document with no trigger runs on every event, so the list holds the events of a push, a pull request and a tag only")
	if err := e.markArg(caps.TriggerPush, "pipeline/"+pl.ID, defaultEventsArg{Events: sourceDefaultEvents}); err != nil {
		return condSet{}, false, err
	}
	return condSet{c: dexpr.Conditions{Event: dexpr.Filter{Include: slices.Clone(sourceDefaultEvents)}}}, true, nil
}

// isDefaultEvents is true when every document carries the marker of the default event list and the pipeline
// triggers read from the file are the triggers of that list.
func isDefaultEvents(docs []*doc, list []ir.Trigger) bool {
	want := []ir.TriggerKind{ir.TriggerPush, ir.TriggerPR, ir.TriggerTag}
	got := make([]ir.TriggerKind, len(list))
	for i, t := range list {
		got[i] = t.Kind
	}
	if len(docs) == 0 || !slices.Equal(got, want) {
		return false
	}
	for _, d := range docs {
		u, ok := d.marks.markedWith(caps.TriggerPush, pipelinePth)
		var a defaultEventsArg
		if !ok || len(d.marks.args[u]) != 1 || json.Unmarshal([]byte(d.marks.args[u][0]), &a) != nil || !slices.Equal(a.Events, sourceDefaultEvents) {
			return false
		}
	}
	return true
}
