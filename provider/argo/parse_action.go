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
	b.collapseImages()
	b.collapseNames()
	b.collapseArtifacts()
}

// collapseHeld puts back the steps that the markers hold. Each step stands where the file has the container or the
// git artifact that the emitter wrote for it, and only when that anchor is still the one that was written.
func (b *jobBuilder) collapseHeld() {
	prefix := b.path + "/step/"
	var held []heldAction
	for u, lines := range emitkit.ParseArgs([]byte(b.c.src.doc.Head)) {
		if u.Feature != caps.StepAction || !strings.HasPrefix(u.IRPath, prefix) || len(lines) == 0 {
			continue
		}
		h := heldAction{path: u.IRPath, step: &ir.Step{}}
		if json.Unmarshal([]byte(lines[0]), &h.arg) != nil || yaml.Unmarshal([]byte(h.arg.Step), h.step) != nil {
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
	var restored []heldAction
	for _, h := range held {
		st := *h.step
		fi, why := b.heldAnchor(h, front, used)
		if why == "" {
			why = b.restoreAt(h, &st, rest, fi)
		}
		if why != "" {
			b.c.ignored(h.path, caps.StepAction, why)
			continue
		}
		if fi >= 0 {
			used[front[fi]] = true
			if h.arg.Hold == "" {
				front[fi] = &st
			}
		}
		b.c.restored[&st] = true
		restored = append(restored, h)
	}
	var out []*ir.Step
	for _, st := range front {
		if !used[st] {
			out = append(out, st)
		}
	}
	out = append(out, rest...)
	b.job.Steps = out
	for _, h := range restored {
		b.dropImage(h.arg.Image)
		b.dropArtifacts(h.arg.Arts)
		for name := range b.artifactNeeds {
			if id, ok := b.taskID(name); ok && slices.Contains(h.arg.Needs, id) {
				b.artifactNeeds[name] = false
			}
		}
	}
}

// restoreAt puts st in the place of the container of the held step, or of its git artifact when it has no container.
// It returns why the step cannot be put back, or an empty text.
func (b *jobBuilder) restoreAt(h heldAction, st *ir.Step, rest []*ir.Step, fi int) string {
	if h.arg.Hold == "" {
		if fi < 0 {
			return "the step has no container or artifact that anchors it; the file content is kept"
		}
		return ""
	}
	i := slices.IndexFunc(rest, func(s *ir.Step) bool { return s.ID == h.step.ID })
	switch {
	case i < 0:
		return "the placeholder container was removed after pipemaster wrote the file; the action is not restored"
	case b.holds[h.step.ID] != h.arg.Hold:
		return "the placeholder container was edited after pipemaster wrote the file; the file content is kept"
	}
	if extOf(rest[i].Ext)[ExtBashDefault] == true {
		keepBashDefault(st)
	}
	rest[i] = st
	return ""
}

// heldAnchor compares what the file holds for a step, apart from its container, with what the emitter wrote. It
// returns the index of the git artifact step that the checkout replaces, or -1, and why the marker cannot stand
// in for the step, or an empty text when it can.
func (b *jobBuilder) heldAnchor(h heldAction, front []*ir.Step, used map[*ir.Step]bool) (int, string) {
	switch {
	case h.arg.Wrote == checkoutWrote:
		i := slices.IndexFunc(front, func(s *ir.Step) bool { return !used[s] && h.arg.Art != "" && b.gitArts[s] == h.arg.Art })
		if i < 0 {
			return -1, "the git artifact was edited or removed after pipemaster wrote the file; the file content is kept"
		}
		return i, ""
	case h.arg.Image != "":
		if !b.hasImage(h.arg.Image) {
			return -1, "the image that the setup action gave was edited after pipemaster wrote the file; the file content is kept"
		}
	case len(h.arg.Arts) > 0:
		for i, n := range h.arg.Arts {
			if i >= len(h.arg.Outs) || b.outArts[n] != h.arg.Outs[i] {
				return -1, "the output artifact of the upload was edited or removed after pipemaster wrote the file; the file content is kept"
			}
		}
	}
	return -1, ""
}

// hasImage is true when the job container or a step runs the image.
func (b *jobBuilder) hasImage(ref string) bool {
	if c := b.job.Container; c != nil && c.Ref == ref {
		return true
	}
	return slices.ContainsFunc(b.job.Steps, func(st *ir.Step) bool { return st.Image != nil && st.Image.Ref == ref })
}

// dropImage clears the image that a setup action gave to the containers after it. A job image that the marker holds
// comes back from the marker.
func (b *jobBuilder) dropImage(ref string) {
	if ref == "" {
		return
	}
	var own containerArg
	if c := b.job.Container; c != nil && c.Ref == ref && len(c.Entrypoint) == 0 && !markerArg(b.c.src.doc.Head, b.path, caps.JobContainer, &own) {
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
// only when the containerPorts are the ones that the emitter wrote, and the entrypoint only when the image is.
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
	if len(a.Entrypoint) == 0 {
		return
	}
	if sv.Image.Ref != a.Image {
		b.c.ignored(path, caps.JobServices, "the service image was edited after pipemaster wrote the file; the file content is kept")
		return
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
		v, ok := a.Values[o.Name]
		if !ok || o.Value != "" {
			continue
		}
		if !slices.Contains(b.fileOutputs, o.Name) {
			b.c.ignored(b.path+"/output/"+o.Name, caps.JobOutputs, "the output was edited after pipemaster wrote the file; the file content is kept")
			continue
		}
		o.Value = v
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
	if !b.cacheIntact(caches) {
		b.c.ignored(b.path, caps.JobCache, "the cache mounts or claims were edited or removed after pipemaster wrote the file; the file content is kept")
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
	same := len(a.Outs) == len(a.Names)
	for i, n := range a.Names {
		same = same && i < len(a.Outs) && b.outArts[n] == a.Outs[i]
	}
	if !same || !slices.Equal(names, a.Names) || yaml.Unmarshal([]byte(a.Arts), &arts) != nil {
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
	if yaml.Unmarshal([]byte(a.Image), &img) != nil || !b.takeJobImage(cmp.Or(a.Written, img.Ref)) {
		b.c.ignored(b.path, caps.JobContainer, "the job image was edited after pipemaster wrote the file; the file content is kept")
		return
	}
	b.job.Container = &img
}

// takeJobImage is true when the file still runs the image that the job container gave. A setup action leaves
// the job image on the containers before it only, so the parser reads it as a step image then.
func (b *jobBuilder) takeJobImage(ref string) bool {
	if c := b.job.Container; c != nil {
		return c.Ref == ref
	}
	found := false
	for _, st := range b.job.Steps {
		if st.Image != nil && st.Image.Ref == ref && len(st.Image.Entrypoint) == 0 {
			st.Image = nil
			found = true
		}
	}
	return found
}

// collapseImages puts the job image and the step images where the IR had them, when every script step still runs
// the image that the emitter wrote for it.
func (b *jobBuilder) collapseImages() {
	var a imagesArg
	if !markerArg(b.c.src.doc.Head, b.path, caps.StepImage, &a) {
		return
	}
	byID := map[string]*ir.Step{}
	for _, st := range b.job.Steps {
		byID[st.ID] = st
	}
	steps := map[string]*ir.Image{}
	for id, ref := range a.Wrote {
		st := byID[id]
		got := "\x00"
		switch {
		case st == nil:
		case st.Image != nil:
			got = st.Image.Ref
		case b.job.Container != nil:
			got = b.job.Container.Ref
		default:
			got = ""
		}
		if got != readImage(ref) {
			b.c.ignored(b.path, caps.StepImage, "the image of the step "+id+" was edited or removed after pipemaster wrote the file; the file content is kept")
			return
		}
		if y, ok := a.Steps[id]; ok {
			img := &ir.Image{}
			if yaml.Unmarshal([]byte(y), img) != nil {
				b.c.ignored(b.path, caps.StepImage, "the image marker cannot be read")
				return
			}
			steps[id] = img
		}
	}
	var job *ir.Image
	if a.Job != "" {
		job = &ir.Image{}
		if yaml.Unmarshal([]byte(a.Job), job) != nil {
			b.c.ignored(b.path, caps.StepImage, "the image marker cannot be read")
			return
		}
	}
	b.job.Container = job
	for id := range a.Wrote {
		byID[id].Image = steps[id]
	}
}

// collapseNames gives the job and its script steps the display names that the markers hold.
func (b *jobBuilder) collapseNames() {
	var a nameArg
	if markerArg(b.c.src.doc.Head, b.path, caps.DisplayName, &a) {
		b.job.Name = a.Name
	}
	for _, st := range b.job.Steps {
		var s nameArg
		if st.Kind == ir.StepScript && markerArg(b.c.src.doc.Head, b.path+"/step/"+st.ID, caps.DisplayName, &s) {
			st.Name = s.Name
		}
	}
}

// staleNames reports a name marker whose job or step the file no longer holds.
func (c *plCtx) staleNames(pl *ir.Pipeline) {
	have := map[string]bool{}
	for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
		jp := "pipeline/" + c.plID + "/job/" + j.ID
		have[jp] = true
		for _, st := range j.Steps {
			have[jp+"/step/"+st.ID] = true
		}
	}
	for u := range emitkit.ParseArgs([]byte(c.src.doc.Head)) {
		if u.Feature == caps.DisplayName && !have[u.IRPath] {
			c.ignored(u.IRPath, caps.DisplayName, "the job or step was removed or renamed after pipemaster wrote the file; the name is not restored")
		}
	}
}

// keepBashDefault marks a step that the marker restored as one whose container holds the bash-if-present wrapper.
func keepBashDefault(st *ir.Step) {
	ext := ir.Ext{}
	for k, v := range st.Ext {
		ext[k] = v
	}
	own := map[string]any{}
	for k, v := range extOf(st.Ext) {
		own[k] = v
	}
	own[ExtBashDefault] = true
	ext[dialect] = own
	st.Ext = ext
}
