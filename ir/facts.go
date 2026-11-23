package ir

// RulesOutTags reports whether no run of the pipeline can start on a tag ref. The facts a condition may use then
// hold: a run that is not a pull request has a branch ref. It is conservative, and false when unsure:
//   - push, pr and schedule triggers qualify. A tag push is its own trigger kind, a pull request ref is a merge
//     ref, and a schedule runs on a branch.
//   - a manual trigger does not: GHA starts a dispatch from a tag as readily as from a branch.
//   - an api, pipeline_done or resource trigger does not: its ref depends on the sender.
//   - a pull_request_target trigger does not: its ref is the base branch, so ref_name is a branch name there.
//   - an event the IR keeps only in Ext (release, create, ...) does not: it can carry a tag ref.
//   - a pipeline with no trigger has no information to rely on.
func (p *Pipeline) RulesOutTags() bool {
	if len(p.Triggers) == 0 || keepsEvents(p.Ext) {
		return false
	}
	for i := range p.Triggers {
		t := &p.Triggers[i]
		switch t.Kind {
		case TriggerPush, TriggerSchedule:
		case TriggerPR:
			if namesEvent(t.Ext, eventPRTarget) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

const (
	eventPRTarget = "pull_request_target"
	extEventKey   = "event"
	extEventsKey  = "events"
)

// keepsEvents reports an Ext family that holds events the IR has no trigger for.
func keepsEvents(ext Ext) bool {
	for _, fam := range ext {
		if m, ok := fam.(map[string]any); ok {
			if evs, has := m[extEventsKey]; has && evs != nil {
				return true
			}
		}
	}
	return false
}

// namesEvent reports an Ext family that says the trigger is the named event.
func namesEvent(ext Ext, name string) bool {
	for _, fam := range ext {
		if m, ok := fam.(map[string]any); ok && m[extEventKey] == name {
			return true
		}
	}
	return false
}
