package tekton

import (
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	// wsRoot holds one directory per Tekton workspace, relative to the checkout of a job.
	wsRoot = "pm-workspaces"
	// dockerConfigWS is the conventional name of the workspace that holds registry credentials.
	dockerConfigWS = "dockerconfig"
	wsStrategy     = "workspace directory"
)

// runBinding is what a PipelineRun binds a pipeline workspace to.
type runBinding struct {
	kind   string
	object string
}

// externalKinds name the workspace sources that the operator provides as a cluster object.
var externalKinds = map[string]string{
	"secret": "Secret", "configMap": "ConfigMap", "persistentVolumeClaim": "PersistentVolumeClaim", "projected": "projected volume", "csi": "CSI volume",
}

// objectKeys read the name of the bound object from the key of each source.
var objectKeys = map[string]string{"secret": "secretName", "configMap": "name", "persistentVolumeClaim": "claimName"}

// passesFiles is true for the sources that tasks write, so the files can move between jobs.
func (b runBinding) passesFiles() bool {
	return b.kind != "secret" && b.kind != "configMap" && b.kind != "projected" && b.kind != "csi"
}

func (b runBinding) external() (string, bool) {
	what, ok := externalKinds[b.kind]
	if !ok {
		return "", false
	}
	if b.object != "" {
		what += " " + b.object
	}
	return what, true
}

// runBindings reads how the PipelineRuns of the project bind the workspaces of a pipeline: the embedded run, a
// PipelineRun document and the PipelineRun of a TriggerTemplate.
func (p *project) runBindings(src *pipeSrc) map[string]runBinding {
	out := map[string]runBinding{}
	add := func(spec *omap) {
		for _, e := range listOf(valueOf(spec, "workspaces")) {
			m := asMapOrNil(e)
			name, _ := text(valueOf(m, "name"))
			if name == "" {
				continue
			}
			if _, dup := out[name]; dup {
				continue
			}
			for _, kind := range slices.Sorted(maps.Keys(m.Vals)) {
				if kind == "name" || kind == "subPath" {
					continue
				}
				b := runBinding{kind: kind}
				if key, ok := objectKeys[kind]; ok {
					b.object, _ = text(valueOf(asMapOrNil(m.Vals[kind]), key))
				}
				out[name] = b
				break
			}
		}
	}
	if src.run != nil {
		spec, _ := asMap(src.run.Top.Vals["spec"])
		add(spec)
	}
	refs := func(spec *omap) bool {
		n, _ := text(valueOf(asMapOrNil(valueOf(spec, "pipelineRef")), "name"))
		return n != "" && n == src.name
	}
	for _, d := range p.resources {
		if d.Kind != kindPipelineRun {
			continue
		}
		if spec, _ := asMap(d.Top.Vals["spec"]); refs(spec) {
			add(spec)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(p.templates)) {
		spec, _ := asMap(p.templates[name].Top.Vals["spec"])
		for _, rt := range listOf(valueOf(spec, "resourcetemplates")) {
			rm := asMapOrNil(rt)
			if k, _ := text(valueOf(rm, "kind")); k != kindPipelineRun {
				continue
			}
			if rs, _ := asMap(valueOf(rm, "spec")); refs(rs) {
				add(rs)
			}
		}
	}
	return out
}

func listOf(v any) []any {
	l, _ := v.([]any)
	return l
}

// declaredWorkspaces maps each workspace that the Pipeline declares to its directory in the checkout. The
// workspace of the source and the workspace of the emitter have no directory.
func (c *plCtx) declaredWorkspaces(pipeWS any) {
	c.wsDir = map[string]string{}
	for _, e := range listOf(pipeWS) {
		name, _ := text(valueOf(asMapOrNil(e), "name"))
		if name == "" || name == workspaceName || c.srcWS[name] {
			continue
		}
		c.wsDir[name] = path.Join(wsRoot, name)
	}
}

// taskWorkspaces maps the workspaces of a PipelineTask to the checkout directory of each. A subPath that
// leaves the workspace, or that holds a variable, leaves the workspace unmapped.
func (c *plCtx) taskWorkspaces(pt *omap) (dirs map[string]string, pipeline map[string]string) {
	dirs, pipeline = map[string]string{}, map[string]string{}
	for _, e := range listOf(pt.Vals["workspaces"]) {
		m := asMapOrNil(e)
		name, _ := text(valueOf(m, "name"))
		ws, _ := text(valueOf(m, "workspace"))
		dir, ok := c.wsDir[ws]
		if name == "" || !ok {
			continue
		}
		if sub, has := text(valueOf(m, "subPath")); has && sub != "" {
			clean := path.Clean(sub)
			if strings.Contains(sub, varOpen) || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
				continue
			}
			dir = path.Join(dir, clean)
		}
		dirs[name], pipeline[name] = dir, ws
	}
	return dirs, pipeline
}

// reportWorkspaces gives each mapped workspace of a job its verdict.
func (b *jobBuilder) reportWorkspaces() {
	declared := b.mounts
	for _, name := range slices.Sorted(maps.Keys(b.tx.dataWS)) {
		pipe, dir := b.wsPipe[name], b.tx.dataWS[name]
		bind := b.c.runWS[pipe]
		if bind.kind == "" && name == dockerConfigWS {
			bind = runBinding{kind: "secret"}
		}
		detail := "workspace " + pipe + " is the directory " + dir + " of the job checkout; $(workspaces." + name + ".path) reads " + dir
		if mp := declared[name]; mp != "" && mp != "/workspace/"+name {
			detail += "; the Task mounts it at " + mp + ", and a literal use of that path is not rewritten"
		}
		level, strategy := caps.Emulated, wsStrategy
		if what, ok := bind.external(); ok {
			level, strategy = caps.External, "workspace bound to a "+what
			detail = "workspace " + pipe + " is bound to " + what + " at run time; the operator must provide " + what +
				" and put its files in " + dir + " before the step runs: a GitHub Actions secret written to the files, a GitLab file CI/CD variable, or a Drone or Woodpecker secret written to the files"
			if name == dockerConfigWS {
				detail += "; the files are the registry credentials (config.json)"
			}
		} else if !bind.passesFiles() {
			level = caps.External
		}
		b.c.p.rep.Add(report.Entry{IRPath: b.path, Feature: caps.ExtPassthrough, Level: level, Strategy: strategy, Detail: detail, SourceLoc: b.c.file})
	}
}

// linkWorkspaces turns the workspaces that tasks share into artifacts.
func (c *plCtx) linkWorkspaces(jobs []*ir.Job) {
	dirs := map[string]string{}
	for pipe, dir := range c.wsDir {
		dirs[pipe] = dir
	}
	emitkit.SharedDirs{
		Dir: dirs, Binds: c.binds, Skip: func(n string) bool { b, ok := c.runWS[n]; return ok && !b.passesFiles() },
		Dialect: dialect, Key: ExtWorkspaceArtifacts, Noun: "task", Medium: "the workspace volume", Pipeline: c.plID, File: c.file,
	}.Link(jobs, c.p.rep)
}

// declaredMounts reads the mountPath that a Task declares for each of its workspaces.
func declaredMounts(v any) map[string]string {
	out := map[string]string{}
	for _, e := range listOf(v) {
		m := asMapOrNil(e)
		name, _ := text(valueOf(m, "name"))
		if mp, ok := text(valueOf(m, "mountPath")); ok && name != "" {
			out[name] = mp
		}
	}
	return out
}

// workspaceNames lists the values of key in a list of mappings.
func workspaceNames(v any, key string) []string {
	var out []string
	for _, e := range listOf(v) {
		if n, _ := text(valueOf(asMapOrNil(e), key)); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// keepWorkspaces keeps a workspace list in Ext. The verdict of each workspace that the job maps comes from
// reportWorkspaces; any other workspace is lost with its binding.
func (b *jobBuilder) keepWorkspaces(key string, v any, names []string, line int) {
	b.k.Out[key] = plain(v)
	var rest []string
	for _, n := range names {
		_, data := b.tx.dataWS[n]
		if !data && !b.tx.srcWS[n] {
			rest = append(rest, n)
		}
	}
	if len(rest) > 0 || len(names) == 0 {
		b.k.Note(caps.ExtPassthrough, "workspaces "+strings.Join(rest, ", ")+" have no IR field and the Pipeline binds them to no directory; kept in Ext", line)
	}
}

// keepPipelineWorkspaces keeps the Pipeline workspace list in Ext and reports each workspace that no task maps.
func (c *plCtx) keepPipelineWorkspaces(v any, line int) {
	c.k.Out["workspaces"] = plain(v)
	used := map[string]bool{}
	for _, pipes := range c.binds {
		for _, p := range pipes {
			used[p] = true
		}
	}
	var rest []string
	for _, n := range workspaceNames(v, "name") {
		if !used[n] && !c.srcWS[n] {
			rest = append(rest, n)
		}
	}
	if len(rest) > 0 {
		c.k.Note(caps.ExtPassthrough, "pipeline workspaces "+strings.Join(rest, ", ")+" are bound by no task, so they have no directory; kept in Ext", line)
	}
}
