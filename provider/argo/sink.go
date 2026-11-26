package argo

// Ext keys that the parser writes. Raw keys that the parser does not read keep their Argo name.
const (
	// ExtResources is the Ext key of the first pipeline that holds the documents no IR field models, sorted by kind and name.
	ExtResources = "resources"
	// ExtTemplates is the pipeline Ext key that holds the templates that no job, the entrypoint or the exit handler reads.
	ExtTemplates = "templates"
	// ExtImplicitNeeds is the job Ext key that lists the needs that came only from a reference in a condition,
	// an argument or an artifact. The emitter writes no dependency for them.
	ExtImplicitNeeds = "implicit_needs"
	// ExtDepends is the job Ext key that holds the depends text of a task, when the IR cannot hold all of it.
	ExtDepends = "depends"
	// ExtGates is the job Ext key that lists the suspend tasks that the parser folded into Manual.
	ExtGates = "gates"
	// ExtTemplate is the step Ext key that holds the whole template of a task step.
	ExtTemplate = "template"
	// ExtCommand is the step Ext key that keeps the command list of a step that the IR holds as one script line.
	ExtCommand = "command"
	// ExtArgs is the step Ext key that keeps the args list beside ExtCommand.
	ExtArgs = "args"
	// ExtScriptLine is the step Ext key that holds the script line the parser built, to tell an edited step from an unedited one.
	ExtScriptLine = "script_line"
	// ExtBashDefault marks a step whose container holds the bash-if-present wrapper, so the step has no IR shell.
	ExtBashDefault = "bash_default"
	// ExtMountPaths is the job Ext key that maps each directory that the parser wrote in place of a volume mount
	// path to that mount path, so the Argo file gets its own text back.
	ExtMountPaths = "mount_paths"
	// ExtWorkspaceArtifacts is the job Ext key that names the artifacts and needs that the parser made for the
	// volumes of the file, which Argo shares through the volume itself.
	ExtWorkspaceArtifacts = "workspace_artifacts"
	// ExtArguments is the job Ext key that holds the task arguments that no template text reads.
	ExtArguments = "arguments"
)
