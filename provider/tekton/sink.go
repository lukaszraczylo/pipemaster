package tekton

import (
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	strategyExt = "kept in Ext[" + dialect + "]"

	// ExtResources is the Ext key of the first pipeline that holds the documents no IR field models, sorted by kind and name.
	ExtResources = "resources"
	// ExtImplicitNeeds is the job Ext key that lists the needs that came only from a result or status reference.
	// The emitter writes no runAfter for them, because the reference orders the tasks.
	ExtImplicitNeeds = "implicit_needs"
	// ExtLegacyParams is the job Ext key that lists the Task params that the text reads as $(inputs.params.x).
	ExtLegacyParams = "legacy_params"
	// ExtBoundParams is the job Ext key that maps each Task param that the Task text reads to the value that the
	// PipelineTask binds, so the Tekton file keeps the Task param names.
	ExtBoundParams = "bound_params"
	// ExtCommand is the step Ext key that keeps the command list of a step that the IR holds as one script line.
	ExtCommand = "command"
	// ExtArgs is the step Ext key that keeps the args list beside ExtCommand.
	ExtArgs = "args"
	// ExtScriptLine is the step Ext key that holds the script line the parser built, to tell an edited step from an unedited one.
	ExtScriptLine = "script_line"
	// ExtBashDefault marks a script that holds the bash-if-present preamble, so the step has no IR shell.
	ExtBashDefault = "bash_default"
)

type sink = ynode.Sink

func newSink(rep *report.Report, file, path string) *sink {
	return ynode.NewSink(dialect, rep, file, path)
}
