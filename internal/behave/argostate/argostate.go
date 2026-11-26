// Package argostate reads what the harness needs to know about an Argo Workflow while it runs.
package argostate

import (
	"encoding/json"
	"fmt"
)

// State is what the harness needs to know about a Workflow while it runs.
type State struct {
	Phase string
	// Suspended is true when a suspend node waits to be resumed.
	Suspended bool
	// Busy is true when a node that runs work has not finished.
	Busy bool
	// Failed is true when a node that runs work failed.
	Failed bool
}

// Idle is true when a suspend node holds the Workflow and no other node runs. Argo schedules no new step then:
// "Once suspended, a Workflow will not schedule any new steps until it is resumed."
// (https://argo-workflows.readthedocs.io/en/latest/walk-through/suspending/).
func (s State) Idle() bool { return s.Phase == "Running" && s.Suspended && !s.Busy }

// Done is true when the Workflow reached a final phase.
func (s State) Done() bool {
	return s.Phase == "Succeeded" || s.Phase == "Failed" || s.Phase == "Error"
}

var workNodes = map[string]bool{"Pod": true, "Container": true, "Plugin": true, "HTTP": true}

// Parse reads the JSON of a Workflow.
func Parse(raw string) (State, error) {
	var wf struct {
		Status struct {
			Nodes map[string]struct {
				Type  string `json:"type"`
				Phase string `json:"phase"`
			} `json:"nodes"`
			Phase string `json:"phase"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &wf); err != nil {
		return State{}, fmt.Errorf("read the workflow: %w", err)
	}
	st := State{Phase: wf.Status.Phase}
	for _, n := range wf.Status.Nodes {
		switch {
		case n.Type == "Suspend" && n.Phase == "Running":
			st.Suspended = true
		case workNodes[n.Type] && (n.Phase == "Running" || n.Phase == "Pending" || n.Phase == ""):
			st.Busy = true
		case workNodes[n.Type] && (n.Phase == "Failed" || n.Phase == "Error"):
			st.Failed = true
		}
	}
	return st, nil
}
