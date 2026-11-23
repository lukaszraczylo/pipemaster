package concourse

import (
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
)

// Ext keys that later tasks rely on.
const (
	// ExtResources is the pipeline Ext key of every resource definition, verbatim, in source order.
	ExtResources = "resources"
	// ExtResourceTypes, ExtVarSources, ExtGroups, ExtDisplay, ExtUserData and ExtPrototypes keep the top-level
	// key of the same name verbatim.
	ExtResourceTypes = "resource_types"
	ExtVarSources    = "var_sources"
	ExtGroups        = "groups"
	ExtDisplay       = "display"
	ExtUserData      = "user_data"
	ExtPrototypes    = "prototypes"
	// ExtExtra is the pipeline Ext key of the unknown top-level keys, by name.
	ExtExtra = "extra"
	// ExtAnchorKeys is the pipeline Ext key that lists the ExtExtra keys that only hold YAML anchors.
	ExtAnchorKeys = "anchor_keys"
	// ExtFiles is the Ext key of the first pipeline that holds the YAML files that are no pipeline, by path.
	ExtFiles = "files"
	// ExtGets is the job Ext key of the source gets (git and time resources) of the leading plan block, verbatim.
	ExtGets = "gets"
	// ExtTriggers is the job Ext key of the trigger resource names of a job that the pipeline union is wider than.
	ExtTriggers = "triggers"
	// ExtGetsParallel is the job Ext key that says the source gets sat in one in_parallel step.
	ExtGetsParallel = "gets_in_parallel"
	// ExtResourcesRef is the trigger Ext key that lists the resource names the trigger came from.
	ExtResourcesRef = "resources"
	// ExtCron is the trigger Ext key of the cron the parser derived from a time resource; a trigger that still has it
	// restores the resource source unchanged.
	ExtCron = "cron"
	// ExtUnreadable is the Ext key of an api trigger that says why the source trigger could not be read.
	ExtUnreadable = "unreadable"
	// ExtStep and ExtKind keep a step the IR cannot model: the verbatim step and its kind key.
	ExtStep = "step"
	ExtKind = "kind"
	// ExtCommand, ExtArgs and ExtScriptLine keep run.path, run.args and the script line built from them.
	ExtCommand    = "command"
	ExtArgs       = "args"
	ExtScriptLine = "script_line"
	// ExtShellFlags keeps the flags before the script in a shell -c run.
	ExtShellFlags = "shell_flags"
	// ExtEntrypoint marks a task with no run.path, which runs the image entrypoint.
	ExtEntrypoint = "entrypoint"
	// ExtConfig keeps the task config keys with no IR field.
	ExtConfig = "config"
	// ExtParams keeps the task params with no Value form.
	ExtParams = "params"
	// ExtRun keeps the run keys with no IR field.
	ExtRun = "run"
	// ExtImageResource keeps an image_resource the IR cannot hold as a reference.
	ExtImageResource = "image_resource"
)

type sink = ynode.Sink

func newSink(rep *report.Report, file, path string) *sink {
	return ynode.NewSink(dialect, rep, file, path)
}
