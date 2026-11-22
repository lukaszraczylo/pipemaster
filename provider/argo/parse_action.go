package argo

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// heldAction is one action step that the file holds as a different step, or not at all.
type heldAction struct {
	step *ir.Step
	path string
	arg  actionArg
}

// collapseActions puts back the action steps that the emitter wrote as a git artifact, a container or nothing.
// A step that the file changed after conversion stays as the file has it.
func (b *jobBuilder) collapseActions() {
	b.collapseWorkdirs()
	b.collapseNoop()
	b.collapseOutputs()
	b.collapseCache()
	b.collapseHeld()
	b.collapseContainer()
	b.collapseArtifacts()
}

// collapseHeld puts back the steps that the markers hold.
func (b *jobBuilder) collapseHeld() {
	prefix := b.path + "/step/"
	var held []heldAction
	for u, lines := range emitkit.ParseArgs([]byte(b.c.src.doc.Head)) {
		if u.Feature != caps.StepAction || !strings.HasPrefix(u.IRPath, prefix) || len(lines) == 0 {
			continue
		}
		h := heldAction{path: u.IRPath, step: &ir.Step{}}
		if json.Unmarshal([]byte(lines[0]), &h.arg) != nil || yaml.Unmarshal([]byte(h.arg.Step), h.step) != nil || h.arg.At < 0 {
			b.c.ignored(u.IRPath, caps.StepAction, "the action step marker cannot be read")
			continue
		}
		h.step.Ext = plainExt(h.step.Ext)
		held = append(held, h)
	}
	if len(held) == 0 {
		return
	}
	slices.SortFunc(held, func(x, y heldAction) int { return cmp.Or(x.arg.Seq-y.arg.Seq, strings.Compare(x.path, y.path)) })
	var front, rest []*ir.Step
	for _, st := range b.job.Steps {
		if _, ok := b.gitArts[st]; ok {
			front = append(front, st)
		} else {
			rest = append(rest, st)
		}
	}
	used := map[*ir.Step]bool{}
	var body []*ir.Step
	var restored []heldAction
	ci := 0
	for _, h := range held {
		st := *h.step
		body = append(body, rest[ci:min(max(ci, h.arg.At), len(rest))]...)
		ci = max(ci, min(h.arg.At, len(rest)))
		if why := b.heldEdit(h, front, used, rest, ci); why != "" {
			b.c.ignored(h.path, caps.StepAction, why+"; the file content is kept")
			continue
		}
		if h.arg.Ran {
			ci++
		}
		body = append(body, &st)
		b.c.restored[&st] = true
		restored = append(restored, h)
	}
	var out []*ir.Step
	for _, st := range front {
		if !used[st] {
			out = append(out, st)
		}
	}
	out = append(out, body...)
	out = append(out, rest[ci:]...)
	b.job.Steps = out
	for _, h := range restored {
		b.dropImage(h.arg.Image)
		b.dropArtifacts(h.arg.Arts)
	}
}

// heldEdit compares what the file holds for a step with what the emitter wrote. It returns why the marker cannot
// stand in for the step, or an empty text when it can. It marks the artifact step that a checkout replaces.
func (b *jobBuilder) heldEdit(h heldAction, front []*ir.Step, used map[*ir.Step]bool, rest []*ir.Step, ci int) string {
	switch {
	case h.arg.Wrote == checkoutWrote:
		i := slices.IndexFunc(front, func(s *ir.Step) bool { return !used[s] && h.arg.Art != "" && b.gitArts[s] == h.arg.Art })
		if i < 0 {
			return "the git artifact was edited or removed after pipemaster wrote the file"
		}
		used[front[i]] = true
	case h.arg.Ran:
		if ci >= len(rest) || rest[ci].ID != h.step.ID {
			return "the container that stands in for the step was edited or removed after pipemaster wrote the file"
		}
	case h.arg.Image != "":
		if !b.hasImage(h.arg.Image) {
			return "the image that the setup action gave was edited after pipemaster wrote the file"
		}
	case len(h.arg.Arts) > 0:
		for _, n := range h.arg.Arts {
			if !slices.ContainsFunc(b.job.Artifacts, func(a ir.Artifact) bool { return a.Name == n }) {
				return "the output artifact of the upload was edited or removed after pipemaster wrote the file"
			}
		}
	}
	return ""
}

// hasImage is true when the job container or a step runs the image.
func (b *jobBuilder) hasImage(ref string) bool {
	if c := b.job.Container; c != nil && c.Ref == ref {
		return true
	}
	return slices.ContainsFunc(b.job.Steps, func(st *ir.Step) bool { return st.Image != nil && st.Image.Ref == ref })
}

// dropImage clears the image that a setup action gave to the containers after it.
func (b *jobBuilder) dropImage(ref string) {
	if ref == "" {
		return
	}
	if c := b.job.Container; c != nil && c.Ref == ref && len(c.Entrypoint) == 0 {
		b.job.Container = nil
	}
	for _, st := range b.job.Steps {
		if st.Image != nil && st.Image.Ref == ref && len(st.Image.Entrypoint) == 0 {
			st.Image = nil
		}
	}
}

// plainExt reads Ext that came through YAML as the maps that the parsers hold.
func plainExt(e ir.Ext) ir.Ext {
	if e == nil {
		return nil
	}
	b, err := yaml.Marshal(e)
	if err != nil {
		return e
	}
	var out ir.Ext
	if yaml.Unmarshal(b, &out) != nil {
		return e
	}
	return out
}

// collapseNoop drops the container that stands in for a job with no step.
func (b *jobBuilder) collapseNoop() {
	var none struct{}
	if !markerArg(b.c.src.doc.Head, b.path, caps.StepScript, &none) {
		return
	}
	last := len(b.job.Steps) - 1
	if last < 0 || b.job.Steps[last].ID != mainStep || !slices.Equal(b.job.Steps[last].Script, []string{placeholderScript}) {
		b.c.ignored(b.path, caps.StepScript, "the no-op container was edited after pipemaster wrote it")
		return
	}
	b.job.Steps = b.job.Steps[:last]
}

// placeholderMarker drops the task that stands in for a pipeline with no job.
func (c *plCtx) placeholderMarker(pl *ir.Pipeline) {
	var a placeholderArg
	path := "pipeline/" + c.plID
	if !markerArg(c.src.doc.Head, path, caps.StepScript, &a) {
		return
	}
	i := slices.IndexFunc(pl.Jobs, func(j *ir.Job) bool { return j.ID == a.Task })
	if i < 0 || len(pl.Jobs[i].Steps) != 1 || !slices.Equal(pl.Jobs[i].Steps[0].Script, []string{placeholderScript}) {
		c.ignored(path, caps.StepScript, "the placeholder task was edited or removed after pipemaster wrote it")
		return
	}
	pl.Jobs = slices.Delete(pl.Jobs, i, i+1)
}

// serviceMarker gives a sidecar back the host ports and the entrypoint that the marker holds. The ports come back
// only when the containerPorts are the ones that the emitter wrote.
func (b *jobBuilder) serviceMarker(sv *ir.Service) {
	var a serviceArg
	path := b.path + "/service/" + sv.Name
	if !markerArg(b.c.src.doc.Head, path, caps.JobServices, &a) {
		return
	}
	var containers []string
	for _, p := range a.Ports {
		containers = append(containers, p[strings.LastIndex(p, ":")+1:])
	}
	if len(a.Ports) > 0 {
		if slices.Equal(containers, sv.Ports) {
			sv.Ports = a.Ports
		} else {
			b.c.ignored(path, caps.JobServices, "the service ports were edited after pipemaster wrote them")
		}
	}
	sv.Image.Entrypoint = a.Entrypoint
}

// collapseOutputs gives an output the value that the marker holds, when the file holds none for it.
func (b *jobBuilder) collapseOutputs() {
	var a outputsArg
	if !markerArg(b.c.src.doc.Head, b.path, caps.JobOutputs, &a) {
		return
	}
	for i := range b.job.Outputs {
		o := &b.job.Outputs[i]
		if v, ok := a.Values[o.Name]; ok && o.Value == "" {
			o.Value = v
		}
	}
}

// dropArtifacts removes the job artifacts that an upload step wrote.
func (b *jobBuilder) dropArtifacts(names []string) {
	b.job.Artifacts = slices.DeleteFunc(b.job.Artifacts, func(a ir.Artifact) bool { return slices.Contains(names, a.Name) })
	if len(b.job.Artifacts) == 0 {
		b.job.Artifacts = nil
	}
}

// collapseWorkdirs gives a step the relative working directory that the marker holds, when the file still says what was written.
func (b *jobBuilder) collapseWorkdirs() {
	for _, st := range b.job.Steps {
		var a workdirArg
		path := b.path + "/step/" + st.ID
		if !markerArg(b.c.src.doc.Head, path, caps.StepWorkdir, &a) {
			continue
		}
		if st.WorkDir != a.Wrote {
			b.c.ignored(path, caps.StepWorkdir, "the working directory was edited after pipemaster wrote it")
			continue
		}
		st.WorkDir = a.WorkDir
	}
}

// collapseCache gives the job the caches that the marker holds. The file holds the paths as volume mounts only.
func (b *jobBuilder) collapseCache() {
	var a cacheArg
	if !markerArg(b.c.src.doc.Head, b.path, caps.JobCache, &a) {
		return
	}
	var caches []ir.Cache
	if yaml.Unmarshal([]byte(a.Caches), &caches) != nil {
		b.c.ignored(b.path, caps.JobCache, "the cache marker cannot be read")
		return
	}
	b.job.Cache = caches
}

// collapseArtifacts gives the job the artifacts that the marker holds, when the file still has the output
// artifacts that stand for them.
func (b *jobBuilder) collapseArtifacts() {
	var a artifactsArg
	if !markerArg(b.c.src.doc.Head, b.path, caps.JobArtifacts, &a) {
		return
	}
	var names []string
	for _, art := range b.job.Artifacts {
		names = append(names, art.Name)
	}
	var arts []ir.Artifact
	if !slices.Equal(names, a.Names) || yaml.Unmarshal([]byte(a.Arts), &arts) != nil {
		b.c.ignored(b.path, caps.JobArtifacts, "the output artifacts were edited after pipemaster wrote them; the file content is kept")
		return
	}
	b.job.Artifacts = arts
}

// disabledMarker gives the job the condition and the matrix that the emitter left out of a disabled task.
func (b *jobBuilder) disabledMarker() bool {
	var a disabledArg
	if !markerArg(b.c.src.doc.Head, b.path, caps.JobIf, &a) {
		return false
	}
	var held ir.Job
	if yaml.Unmarshal([]byte(a.Job), &held) != nil {
		b.c.ignored(b.path, caps.JobIf, "the condition marker cannot be read")
		return false
	}
	b.job.If = held.If
	if held.Matrix != nil {
		b.setMatrix(held.Matrix)
	}
	return true
}

// collapseContainer gives the job image the entrypoint that the marker holds.
func (b *jobBuilder) collapseContainer() {
	var a containerArg
	if !markerArg(b.c.src.doc.Head, b.path, caps.JobContainer, &a) {
		return
	}
	var img ir.Image
	if yaml.Unmarshal([]byte(a.Image), &img) != nil || b.job.Container == nil || b.job.Container.Ref != img.Ref {
		b.c.ignored(b.path, caps.JobContainer, "the job image was edited after pipemaster wrote the file; the file content is kept")
		return
	}
	b.job.Container = &img
}
