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
