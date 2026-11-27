package emitkit

import (
	"fmt"
	"strings"

	"github.com/lukaszraczylo/pipemaster/ir"
)

// GitLab Ext keys that mark a job whose work lives outside its own steps.
const (
	bridgeOwn      = "gitlab"
	bridgeTrigger  = "trigger"
	bridgeNoScript = "_no_script"
)

// BridgeWork says why a job with no steps still stands for work: a GitLab trigger job starts another pipeline,
// and a job with no script overrides a job that an include or extends supplies. The text names what the reader
// must do by hand. It returns false for a job that holds no such work.
func BridgeWork(j *ir.Job) (string, bool) {
	own, _ := j.Ext[bridgeOwn].(map[string]any)
	if t, ok := own[bridgeTrigger]; ok {
		return triggerReason(t), true
	}
	if b, _ := own[bridgeNoScript].(bool); b {
		return "the job has no script here; its script comes from an include or extends that the converter did not read, so add the script by hand", true
	}
	return "", false
}

func triggerReason(t any) string {
	m, ok := t.(map[string]any)
	if !ok {
		return fmt.Sprintf("the job starts the pipeline %v; start it with the target's own trigger", t)
	}
	if p, ok := m["project"].(string); ok {
		return "the job starts the pipeline of project " + p + " and the target has no cross-project trigger; start that pipeline by hand or with an API call"
	}
	if inc := includeFiles(m["include"]); inc != "" {
		return "the job starts the child pipeline " + inc + " and the target has no child pipeline; convert that file and run it as its own pipeline"
	}
	return "the job starts another pipeline and the target has no trigger; start it by hand"
}

func includeFiles(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		for _, k := range []string{"local", "file", "template", "artifact"} {
			if s, ok := x[k].(string); ok {
				return s
			}
		}
	case []any:
		var out []string
		for _, e := range x {
			if s := includeFiles(e); s != "" {
				out = append(out, s)
			}
		}
		return strings.Join(out, ", ")
	}
	return ""
}

// ExtCallUnrunnable is the Ext key on a call step whose child the rewrite read but could not run inline.
const ExtCallUnrunnable = "pipemaster.call_unrunnable"

// MarkCallUnrunnable marks the call step of a job whose child was read but cannot run inline, so CallReason
// gives the matrix reason instead of the remote-file one.
func MarkCallUnrunnable(j *ir.Job) {
	for _, st := range j.Steps {
		if st != nil && st.Kind == ir.StepPipelineCall {
			if st.Ext == nil {
				st.Ext = ir.Ext{}
			}
			st.Ext[ExtCallUnrunnable] = true
		}
	}
}

// CallReason says why a call of another workflow has no target form: the converter reads a local reusable
// workflow that is in the input, so a call that reaches the emitter is remote or its file is missing, unless the
// rewrite marked it unrunnable. It is empty for a step that is no call.
func CallReason(st *ir.Step, target string) string {
	if st == nil || st.Kind != ir.StepPipelineCall || st.Action == nil {
		return ""
	}
	if _, ok := st.Ext[ExtCallUnrunnable]; ok {
		return "the step calls the reusable workflow " + callRef(st.Action) + "; the workflow is in the input, but its matrix meets a matrix of the call job and " + target + " cannot run it inline, so the work of that workflow is missing"
	}
	return "the step calls the reusable workflow " + callRef(st.Action) + "; the file is remote or not in the input, and " + target + " has no call of another pipeline, so the work of that workflow is missing"
}

func callRef(a *ir.ActionRef) string {
	if a.Kind == ir.ActionLocal {
		return a.Path
	}
	s := a.Owner + "/" + a.Repo
	if a.Path != "" {
		s += "/" + a.Path
	}
	if a.Ref != "" {
		s += "@" + a.Ref
	}
	return s
}
